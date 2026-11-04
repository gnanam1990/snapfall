// Command circle-setup does the one-time Circle developer-controlled wallet setup and checks
// it, without the entity secret ever reaching a terminal, a log or a chat.
//
//	circle-setup secret    generate the entity secret into a 0600 file, register it with
//	                       Circle, save the recovery file next to it
//	circle-setup wallets   find or create the treasury and relayer EOA wallets on the
//	                       deployment's Arc network; prints their ids and addresses
//	circle-setup check     read-only: API key, wallet ids, lane checks, balances
//
// Nothing here moves funds or signs a transaction. The API key comes from CIRCLE_API_KEY;
// `wallets` also needs CIRCLE_ENTITY_SECRET; `check` reads the wallet ids from
// CIRCLE_TREASURY_WALLET_ID and CIRCLE_BROADCASTER_WALLET_ID.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/circlewallet"
)

const usage = `usage: circle-setup [-deployment F] [-dir D] [-register-existing] [-mainnet] <secret|wallets|check>`

func main() {
	deployment := flag.String("deployment", "deployments/arc-testnet.json", "chain deployment config (decides the Arc network)")
	dir := flag.String("dir", "", "secret: directory for the entity secret and recovery file (default ~/.snapfall/circle-<testnet|mainnet>)")
	registerExisting := flag.Bool("register-existing", false, "secret: register the secret already in -dir instead of generating one (after a failed registration)")
	mainnet := flag.Bool("mainnet", false, "wallets: required to create wallets on Arc mainnet")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var err error
	switch flag.Arg(0) {
	case "secret":
		err = runSecret(ctx, os.Stdout, *deployment, *dir, *registerExisting)
	case "wallets":
		err = runWallets(ctx, os.Stdout, *deployment, *mainnet)
	case "check":
		err = runCheck(ctx, os.Stdout, *deployment)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "circle-setup:", err)
		os.Exit(1)
	}
}

// arcNetwork is what setup needs from the deployment: the network and the USDC predeploy.
// It uses LoadNetwork, not Load: the treasury address Load requires is what `wallets`
// creates, and Arc mainnet has no contract addresses yet.
type arcNetwork struct {
	net        chaincfg.Network
	usdc       chaincfg.Contract
	blockchain string // Circle's name: ARC-TESTNET or ARC
	env        string // testnet or mainnet
}

func network(deployment string) (arcNetwork, error) {
	n, usdc, err := chaincfg.LoadNetwork(deployment, os.LookupEnv)
	if err != nil {
		return arcNetwork{}, err
	}
	bc, ok := circlewallet.BlockchainFor(n.ChainID)
	if !ok {
		return arcNetwork{}, fmt.Errorf("chain id %d is not an Arc network Circle wallets serve", n.ChainID)
	}
	env := "testnet"
	if bc == "ARC" {
		env = "mainnet"
	}
	return arcNetwork{net: n, usdc: usdc, blockchain: bc, env: env}, nil
}

// sameEnvironment refuses a testnet key against mainnet and the reverse: the wallets and the
// entity secret of the two Circle environments are separate.
func sameEnvironment(keyEnv, netEnv string) error {
	if keyEnv == "" {
		return fmt.Errorf("%s does not start with TEST_API_KEY or LIVE_API_KEY; cannot tell which Circle environment it is for", circlewallet.EnvAPIKey)
	}
	if keyEnv != netEnv {
		return fmt.Errorf("%s is a %s key but the deployment is Arc %s", circlewallet.EnvAPIKey, keyEnv, netEnv)
	}
	return nil
}

// registrar is the part of the Circle API `secret` uses (an interface so the file handling
// is tested without a network).
type registrar interface {
	Environment() string
	Ping(context.Context) error
	RegisterEntitySecret(context.Context, []byte) (string, error)
}

func runSecret(ctx context.Context, out io.Writer, deployment, dir string, registerExisting bool) error {
	an, err := network(deployment)
	if err != nil {
		return err
	}
	netEnv := an.env
	admin, err := circlewallet.AdminFromEnv()
	if err != nil {
		return err
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".snapfall", "circle-"+netEnv)
	}
	return setupSecret(ctx, out, admin, netEnv, dir, registerExisting, rand.Reader)
}

func setupSecret(ctx context.Context, out io.Writer, reg registrar, netEnv, dir string, registerExisting bool, entropy io.Reader) error {
	if err := sameEnvironment(reg.Environment(), netEnv); err != nil {
		return err
	}
	secretPath := filepath.Join(dir, "entity-secret.hex")
	recoveryPath := filepath.Join(dir, "recovery-file.dat")
	// Both checks come before any request: a registration whose recovery file then cannot be
	// written would leave a secret nobody can reset.
	if _, err := os.Lstat(recoveryPath); err == nil {
		return fmt.Errorf("%s already exists; this command never overwrites a recovery file", recoveryPath)
	}
	_, statErr := os.Lstat(secretPath)
	secretExists := statErr == nil
	switch {
	case secretExists && !registerExisting:
		return fmt.Errorf("%s already exists; this command never overwrites an entity secret (if an earlier run failed at registration, rerun with -register-existing)", secretPath)
	case !secretExists && registerExisting:
		return fmt.Errorf("-register-existing: there is no secret at %s", secretPath)
	}
	if err := reg.Ping(ctx); err != nil {
		return fmt.Errorf("the API key does not work, nothing was written: %w", err)
	}
	if err := privateDir(dir); err != nil {
		return err
	}

	var secret []byte
	if registerExisting {
		raw, err := readPrivate(secretPath)
		if err != nil {
			return err
		}
		if secret, err = circlewallet.ParseEntitySecret(string(raw)); err != nil {
			return fmt.Errorf("%s: %w", secretPath, err)
		}
	} else {
		secret = make([]byte, 32)
		if _, err := io.ReadFull(entropy, secret); err != nil {
			return fmt.Errorf("generating the entity secret: %w", err)
		}
		// Written BEFORE registering: a registered secret that was never saved is lost.
		if err := writePrivate(secretPath, []byte(hex.EncodeToString(secret)+"\n")); err != nil {
			return err
		}
		fmt.Fprintf(out, "entity secret written to %s (0600, not printed)\n", secretPath)
	}

	recovery, err := reg.RegisterEntitySecret(ctx, secret)
	if err != nil {
		return fmt.Errorf("%w\nthe secret at %s is NOT registered; fix the cause and rerun with -register-existing", err, secretPath)
	}
	if err := writePrivate(recoveryPath, []byte(recovery)); err != nil {
		return fmt.Errorf("the entity secret IS registered but its recovery file could not be saved (%w); download it from the Circle console now", err)
	}
	fmt.Fprintf(out, "registered with Circle (%s)\nrecovery file written to %s (0600) — back it up offline; it is the only way to reset the secret\n\n", netEnv, recoveryPath)
	fmt.Fprintf(out, "add to your env file (the value never needs to be typed or shown):\n  export %s=\"$(cat %s)\"\n", circlewallet.EnvEntitySecret, secretPath)
	return nil
}

// privateDir creates dir 0700, or accepts an existing one only if no one else can read it.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %o); chmod 700 it first", dir, fi.Mode().Perm())
	}
	return nil
}

// writePrivate creates path 0600, refusing to replace anything already there.
func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists; refusing to overwrite it", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readPrivate(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %o); chmod 600 it first", path, fi.Mode().Perm())
	}
	return os.ReadFile(path)
}

func runWallets(ctx context.Context, out io.Writer, deployment string, mainnet bool) error {
	an, err := network(deployment)
	if err != nil {
		return err
	}
	bc, netEnv := an.blockchain, an.env
	if netEnv == "mainnet" && !mainnet {
		return fmt.Errorf("the deployment is Arc mainnet; pass -mainnet to create wallets there")
	}
	admin, err := circlewallet.AdminFromEnv()
	if err != nil {
		return err
	}
	if err := sameEnvironment(admin.Environment(), netEnv); err != nil {
		return err
	}
	secretHex, ok := os.LookupEnv(circlewallet.EnvEntitySecret)
	if !ok {
		return fmt.Errorf("%s is not set", circlewallet.EnvEntitySecret)
	}
	secret, err := circlewallet.ParseEntitySecret(secretHex)
	if err != nil {
		return err
	}
	got, err := admin.EnsureWallets(ctx, secret, bc, circlewallet.Roles)
	printProvisioned(out, bc, got)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nadd to your env file (wallet ids are not secrets):\n")
	for _, p := range got {
		fmt.Fprintf(out, "  export %s=%s\n", p.Role.Env, p.Wallet.ID)
	}
	fmt.Fprintf(out, "\nboth addresses pay gas in USDC on Arc: fund them before the first live call\n")
	return nil
}

func printProvisioned(out io.Writer, bc string, got []circlewallet.Provisioned) {
	for _, p := range got {
		verb := "reused "
		if p.Created {
			verb = "created"
		}
		fmt.Fprintf(out, "%-18s %s  %s  %s  %s\n", p.Role.Name, verb, bc, p.Wallet.ID, p.Wallet.Address)
	}
}

func runCheck(ctx context.Context, out io.Writer, deployment string) error {
	an, err := network(deployment)
	if err != nil {
		return err
	}
	bc, netEnv := an.blockchain, an.env
	admin, err := circlewallet.AdminFromEnv()
	if err != nil {
		return err
	}
	reader, err := chain.NewReadOnly(an.net.RPCURL, an.net.ChainID)
	if err != nil {
		return err
	}
	usdc := common.HexToAddress(an.usdc.Address)
	c := &checker{out: out}

	c.step("api key", func() (string, error) {
		if err := sameEnvironment(admin.Environment(), netEnv); err != nil {
			return "", err
		}
		if err := admin.Ping(ctx); err != nil {
			return "", err
		}
		return netEnv + " key, accepted by Circle", nil
	})
	c.step("entity secret", func() (string, error) {
		v, ok := os.LookupEnv(circlewallet.EnvEntitySecret)
		if !ok {
			return "", fmt.Errorf("%s is not set", circlewallet.EnvEntitySecret)
		}
		if _, err := circlewallet.ParseEntitySecret(v); err != nil {
			return "", err
		}
		return "set and well-formed (only the first signed call proves it is the registered one)", nil
	})

	addrs := map[string]string{}
	ids := map[string]string{}
	for i, r := range circlewallet.Roles {
		required := i == 0 // the treasury is the daemon's signer; the relayer is optional
		c.step(r.Name, func() (string, error) {
			id := strings.TrimSpace(os.Getenv(r.Env))
			if id == "" {
				if !required {
					return "not set (" + r.Env + "); fine unless the sidecar self-relays through Circle", nil
				}
				return "", fmt.Errorf("%s is not set", r.Env)
			}
			if other, dup := ids[id]; dup {
				return "", fmt.Errorf("%s is the same wallet as %s; use two wallets", r.Env, other)
			}
			ids[id] = r.Env
			w, err := admin.Wallet(ctx, id)
			if err != nil {
				return "", err
			}
			if err := circlewallet.CheckLane(w, bc); err != nil {
				return "", err
			}
			addrs[r.Name] = w.Address
			return fmt.Sprintf("%s  EOA %s  LIVE  %s", id, bc, w.Address), nil
		})
		c.step(r.Name+" balance", func() (string, error) {
			a, ok := addrs[r.Name]
			if !ok {
				return "skipped", nil
			}
			ret, err := reader.CallView(ctx, usdc, chain.CalldataBalanceOf(common.HexToAddress(a)))
			if err != nil {
				return "", fmt.Errorf("reading USDC.balanceOf from the RPC: %w", err)
			}
			bal, err := chain.DecodeUint256(ret)
			if err != nil {
				return "", err
			}
			note := ""
			if bal.Sign() == 0 {
				note = "  (empty: it cannot pay gas yet)"
			}
			return fmt.Sprintf("%s USDC on chain%s", usdcString(bal), note), nil
		})
	}
	c.step("operator address", func() (string, error) {
		want := strings.TrimSpace(os.Getenv("SNAPFALL_TREASURY_ADDRESS"))
		got, ok := addrs[circlewallet.Roles[0].Name]
		switch {
		case !ok:
			return "skipped", nil
		case want == "":
			return "SNAPFALL_TREASURY_ADDRESS not set; jobs must name " + got + " as their operator", nil
		case !common.IsHexAddress(want) || common.HexToAddress(want) != common.HexToAddress(got):
			return "", fmt.Errorf("SNAPFALL_TREASURY_ADDRESS is %s but the Circle treasury wallet is %s; jobs created for one cannot be advanced by the other", want, got)
		}
		return "SNAPFALL_TREASURY_ADDRESS matches the Circle treasury wallet", nil
	})
	if c.failed > 0 {
		return fmt.Errorf("%d check(s) failed", c.failed)
	}
	fmt.Fprintln(out, "\nall checks passed (read-only: nothing was signed or sent)")
	return nil
}

type checker struct {
	out    io.Writer
	failed int
}

func (c *checker) step(name string, f func() (string, error)) {
	msg, err := f()
	if err != nil {
		c.failed++
		fmt.Fprintf(c.out, "FAIL  %-26s %v\n", name, err)
		return
	}
	fmt.Fprintf(c.out, "ok    %-26s %s\n", name, msg)
}

// usdcString renders a 6-decimal ERC-20 amount exactly.
func usdcString(v *big.Int) string {
	s := new(big.Int).Abs(v).String()
	for len(s) < 7 {
		s = "0" + s
	}
	return s[:len(s)-6] + "." + s[len(s)-6:]
}
