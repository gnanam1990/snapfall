// Command chainops drives the customer/ops half of the on-chain lifecycle — the calls
// that are NOT the daemon's to make: pool seeding (LP), customer funding, job creation,
// work/delivery progression. Every submission prints its transaction hash, explorer
// link, gas used, and — loudly — REVERTED when a receipt carries status 0.
//
// Keys come from env only (-key-env names the variable), format-validated by the chain
// client, never logged. Run this from the shell that holds the keys.
//
// -signer circle signs through the Circle developer-controlled wallet the daemon's treasury
// lane uses (CIRCLE_API_KEY, CIRCLE_ENTITY_SECRET, CIRCLE_TREASURY_WALLET_ID). It exists
// because startWork, recordExpense and submitDelivery must come from the job's operator: a
// daemon whose treasury is a Circle wallet needs these calls signed by that same address.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/cctp"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/circlewallet"
)

func main() {
	deployment := flag.String("deployment", "deployments/arc-testnet.json", "chain deployment config")
	keyEnv := flag.String("key-env", "TREASURY_PRIVATE_KEY", "environment variable holding the signing key")
	signerKind := flag.String("signer", "key", `"key" (the -key-env private key) or "circle" (the Circle treasury wallet)`)
	var dest destination
	flag.StringVar(&dest.rpc, "dest-rpc", "", "payout: destination-chain RPC; with -dest-chain-id and -dest-key-env, chainops also completes the mint")
	flag.Uint64Var(&dest.chainID, "dest-chain-id", 0, "payout: destination chain id (the mint refuses an endpoint on any other chain)")
	flag.StringVar(&dest.keyEnv, "dest-key-env", "", "payout: env var holding a key with gas on the destination chain (it only relays; it never holds the USDC)")
	flag.Parse()
	if err := run(*deployment, *keyEnv, *signerKind, dest, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "chainops:", err)
		os.Exit(1)
	}
}

// signer is what a submission needs: *chain.Client and *circlewallet.Wallet both satisfy it.
type signer interface {
	Submit(ctx context.Context, to common.Address, calldata []byte) (chain.Receipt, error)
	Address() common.Address
}

// destination is where a payout's mint is completed, when chainops is asked to do it.
type destination struct {
	rpc     string
	chainID uint64
	keyEnv  string
}

func run(deployment, keyEnv, signerKind string, dest destination, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(`usage: chainops [-key-env VAR | -signer circle] <command> ...
  seed-pool <usdc>                                approve + deposit into FloatPool (LP)
  transfer <to> <usdc>                            USDC transfer (e.g. gas+funds to the customer wallet)
  create-job <jobid32> <customer> <usdc> <budget> JobVault.createJob (operator)
  fund-job <jobid32> <usdc>                       approve + JobVault.fund (CUSTOMER key)
  start-work <jobid32>                            JobVault.startWork (operator; AFTER the advance)
  record-expense <jobid32> <usdc> <receipt32>     JobVault.recordExpense (operator)
  submit-delivery <jobid32> <hash32>              JobVault.submitDelivery (operator)
  force-advance <jobid32>                         requestAdvance with FIXED gas (estimate bypass) — the
                                                  deliberate mined-revert demonstrator for receipt discipline
  payout <domain> <recipient> <usdc>              CCTP: burn USDC on Arc for <recipient> on <domain>
                                                  (testnet: 0 Eth Sepolia, 3 Arb Sepolia, 6 Base Sepolia),
                                                  wait for Circle's attestation, mint if -dest-* are set
  payout-mint <domain> <arc-burn-tx> <recipient>  CCTP: fetch the attestation for an earlier payout burn,
                                                  mint if -dest-* are set
  job-status <jobid32>                            read JobVault state (no key use)
  advance-of <jobid32>                            read FloatPool.openAdvanceOf (no key use)`)
	}
	dep, err := chaincfg.Load(deployment, os.LookupEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	// raw stays nil on the Circle path: force-advance needs a fixed-gas raw signature.
	var (
		s      signer
		reader *chain.Client
		raw    *chain.Client
	)
	switch signerKind {
	case "key":
		raw, err = chain.NewFromEnv(keyEnv, dep.Network.RPCURL, dep.Network.ChainID)
		if err != nil {
			return err
		}
		s, reader = raw, raw
	case "circle":
		reader, err = chain.NewReadOnly(dep.Network.RPCURL, dep.Network.ChainID)
		if err != nil {
			return err
		}
		w, err := circlewallet.NewFromEnv(dep.Network.ChainID, reader)
		if err != nil {
			return err
		}
		if err := w.Open(ctx); err != nil {
			return err
		}
		fmt.Printf("signer: Circle developer-controlled wallet %s\n", w.Address().Hex())
		s = w
	default:
		return fmt.Errorf("-signer %q: want key or circle", signerKind)
	}
	fp := common.HexToAddress(dep.Contracts.FloatPool.Address)
	jv := common.HexToAddress(dep.Contracts.JobVault.Address)
	usdc := common.HexToAddress(dep.Contracts.USDC.Address)
	explorer := strings.TrimSuffix(dep.Network.ExplorerURL, "/")

	show := func(label string, r chain.Receipt, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		verdict := "OK"
		if r.Reverted {
			verdict = "*** REVERTED (mined and failed — this action did NOT happen) ***"
		}
		fmt.Printf("%-16s %s\n  tx   %s/tx/%s\n  block %d  gas %d\n", label, verdict, explorer, r.TxHash, r.Block, r.GasUsed)
		return nil
	}

	cmd := args[0]
	arg := func(i int) string {
		if len(args) <= i {
			fmt.Fprintln(os.Stderr, "missing argument")
			os.Exit(2)
		}
		return args[i]
	}

	// payout support: the Arc deployment's CCTP side and its attestation service.
	mint := func(domain uint32, burnTx string, to common.Address) error {
		net, err := cctp.ForArc(dep.Network.ChainID)
		if err != nil {
			return err
		}
		iris := cctp.Iris{Base: net.IrisBase}
		fmt.Printf("waiting for Circle's attestation of %s ...\n", burnTx)
		att, err := iris.Await(ctx, cctp.ArcDomain, burnTx, domain, to, 10*time.Minute)
		if err != nil {
			return err
		}
		fmt.Printf("attested: %s USDC (atomic %s) for %s on %s\n  message     0x%x\n  attestation 0x%x\n",
			formatMicros(att.Amount), att.Amount, to.Hex(), net.Domains[domain], att.Message, att.Attestation)
		data, err := cctp.CalldataReceiveMessage(att.Message, att.Attestation)
		if err != nil {
			return err
		}
		if dest.rpc == "" || dest.chainID == 0 || dest.keyEnv == "" {
			fmt.Printf("not minting here (no -dest-rpc/-dest-chain-id/-dest-key-env). Anyone with gas on %s can call\n"+
				"  MessageTransmitterV2 %s receiveMessage(message, attestation)\nto complete it; the USDC goes to %s either way.\n",
				net.Domains[domain], net.MessageTransmitter.Hex(), to.Hex())
			return nil
		}
		relayer, err := chain.NewFromEnv(dest.keyEnv, dest.rpc, dest.chainID)
		if err != nil {
			return err
		}
		r, err := relayer.Submit(ctx, net.MessageTransmitter, data)
		if err != nil {
			return fmt.Errorf("mint on %s: %w", net.Domains[domain], err)
		}
		verdict := "OK, minted"
		if r.Reverted {
			verdict = "*** REVERTED (mined and failed; the mint did NOT happen, the attestation is still valid) ***"
		}
		fmt.Printf("receiveMessage   %s\n  tx   %s on chain %d\n  block %d  gas %d\n", verdict, r.TxHash, dest.chainID, r.Block, r.GasUsed)
		return nil
	}
	payoutTarget := func(domainArg, toArg string) (uint32, common.Address, error) {
		net, err := cctp.ForArc(dep.Network.ChainID)
		if err != nil {
			return 0, common.Address{}, err
		}
		d, err := strconv.ParseUint(domainArg, 10, 32)
		if err != nil {
			return 0, common.Address{}, fmt.Errorf("domain %q is not a number", domainArg)
		}
		if _, ok := net.Domains[uint32(d)]; !ok {
			return 0, common.Address{}, fmt.Errorf("domain %d is not a payout destination on this network (known: %v)", d, net.Domains)
		}
		if !common.IsHexAddress(toArg) || common.HexToAddress(toArg) == (common.Address{}) {
			return 0, common.Address{}, fmt.Errorf("recipient %q is not a usable address (malformed or zero)", toArg)
		}
		return uint32(d), common.HexToAddress(toArg), nil
	}

	switch cmd {
	case "payout":
		domain, to, err := payoutTarget(arg(1), arg(2))
		if err != nil {
			return err
		}
		amt, err := usdcMicros(arg(3))
		if err != nil {
			return err
		}
		if amt.Sign() == 0 {
			return fmt.Errorf("a payout of zero burns nothing")
		}
		net, err := cctp.ForArc(dep.Network.ChainID)
		if err != nil {
			return err
		}
		// Standard finality: Arc finalizes on inclusion, so Fast buys nothing leaving Arc. The fee
		// is still read, never assumed, and the burn's cap covers all of it.
		bps, err := cctp.Iris{Base: net.IrisBase}.FeeBps(ctx, cctp.ArcDomain, domain, cctp.StandardFinality)
		if err != nil {
			return err
		}
		maxFee := cctp.MaxFee(amt, bps)
		fmt.Printf("payout: burning %s USDC on Arc for %s on %s (fee %d bps, at most %s USDC)\n",
			formatMicros(amt), to.Hex(), net.Domains[domain], bps, formatMicros(maxFee))
		burn, err := cctp.CalldataDepositForBurn(amt, domain, to, usdc, maxFee, cctp.StandardFinality)
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, usdc, chain.CalldataApprove(net.TokenMessenger, amt))
		if err := show("approve", r, err); err != nil {
			return err
		}
		if r.Reverted {
			return fmt.Errorf("approve reverted; nothing was burned")
		}
		r, err = s.Submit(ctx, net.TokenMessenger, burn)
		if err := show("depositForBurn", r, err); err != nil {
			return err
		}
		if r.Reverted {
			return fmt.Errorf("the burn reverted; no USDC left Arc")
		}
		return mint(domain, r.TxHash, to)
	case "payout-mint":
		domain, to, err := payoutTarget(arg(1), arg(3))
		if err != nil {
			return err
		}
		burnTx := arg(2)
		if len(common.FromHex(burnTx)) != 32 {
			return fmt.Errorf("%q is not a transaction hash", burnTx)
		}
		return mint(domain, burnTx, to)
	case "seed-pool":
		amt, err := usdcMicros(arg(1))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, usdc, chain.CalldataApprove(fp, amt))
		if err := show("approve", r, err); err != nil {
			return err
		}
		r, err = s.Submit(ctx, fp, chain.CalldataDeposit(amt, s.Address()))
		return show("deposit", r, err)
	case "transfer":
		amt, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, usdc, chain.CalldataTransfer(common.HexToAddress(arg(1)), amt))
		return show("transfer", r, err)
	case "create-job":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		pay, err := usdcMicros(arg(3))
		if err != nil {
			return err
		}
		budget, err := usdcMicros(arg(4))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, jv, chain.CalldataCreateJob(id, common.HexToAddress(arg(2)), s.Address(), pay, budget, keccakHash("snapfall-demo-terms"), 1800000000))
		return show("create-job", r, err)
	case "fund-job":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		pay, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, usdc, chain.CalldataApprove(jv, pay))
		if err := show("approve", r, err); err != nil {
			return err
		}
		r, err = s.Submit(ctx, jv, chain.CalldataFund(id))
		return show("fund", r, err)
	case "start-work":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, jv, chain.CalldataStartWork(id))
		return show("start-work", r, err)
	case "record-expense":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		amt, err := usdcMicros(arg(2))
		if err != nil {
			return err
		}
		receipt, err := chain.JobID32(arg(3))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, jv, chain.CalldataRecordExpense(id, amt, receipt))
		return show("record-expense", r, err)
	case "submit-delivery":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		h, err := chain.JobID32(arg(2))
		if err != nil {
			return err
		}
		r, err := s.Submit(ctx, jv, chain.CalldataSubmitDelivery(id, h))
		return show("submit-delivery", r, err)
	case "force-advance":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		if raw == nil {
			return fmt.Errorf("force-advance needs -signer key: it signs a fixed-gas transaction locally, which the Circle lane does not do")
		}
		fmt.Println("force-advance: bypassing gas estimation — a doomed call (duplicate advance, over-cap) will MINE AND REVERT; that is the point.")
		r, err := raw.SubmitWithGas(ctx, fp, chain.CalldataRequestAdvance(id), 300_000)
		return show("force-advance", r, err)
	case "job-status":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		ret, err := reader.CallView(ctx, jv, chain.CalldataJobStatus(id))
		if err != nil {
			return err
		}
		st, err := chain.DecodeJobStatus(ret)
		if err != nil {
			return err
		}
		names := []string{"Created", "Funded", "InProgress", "Delivered", "Accepted", "Refunded", "Cancelled"}
		name := "?"
		if int(st) < len(names) {
			name = names[st]
		}
		fmt.Printf("job-status: %d (%s)\n", st, name)
		return nil
	case "advance-of":
		id, err := chain.JobID32(arg(1))
		if err != nil {
			return err
		}
		ret, err := reader.CallView(ctx, fp, chain.CalldataOpenAdvanceOf(id))
		if err != nil {
			return err
		}
		p, fee, open, err := chain.DecodeOpenAdvance(ret)
		if err != nil {
			return err
		}
		fmt.Printf("advance-of: principal=%s fee=%s open=%v\n", p, fee, open)
		return nil
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func keccakHash(s string) [32]byte {
	var out [32]byte
	sum, _ := chain.JobID32("0x" + fmt.Sprintf("%064x", big.NewInt(0).SetBytes([]byte(s))))
	copy(out[:], sum[:])
	return out
}

// usdcMicros parses a human USDC amount into 6dp micros, exactly — no floats.
func usdcMicros(v string) (*big.Int, error) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 2)
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok || whole.Sign() < 0 {
		return nil, fmt.Errorf("invalid USDC amount %q", v)
	}
	out := new(big.Int).Mul(whole, big.NewInt(1_000_000))
	if len(parts) == 2 {
		frac := parts[1]
		if frac == "" || len(frac) > 6 {
			return nil, fmt.Errorf("invalid USDC amount %q", v)
		}
		f, ok := new(big.Int).SetString(frac+strings.Repeat("0", 6-len(frac)), 10)
		if !ok {
			return nil, fmt.Errorf("invalid USDC amount %q", v)
		}
		out.Add(out, f)
	}
	return out, nil
}

// formatMicros renders atomic USDC (6dp) as a decimal for humans; the atomic value is printed
// beside it wherever exactness matters.
func formatMicros(v *big.Int) string {
	q, r := new(big.Int).QuoRem(v, big.NewInt(1_000_000), new(big.Int))
	return fmt.Sprintf("%s.%06d", q, r.Int64())
}
