package testnetops

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"
)

// CastFunder sends native USDC using a named encrypted Foundry keystore account.
// It never accepts a raw private key.
type CastFunder struct {
	account string
	rpcURL  string
}

// NewCastFunder configures a secure Foundry-keystore signer.
func NewCastFunder(account, rpcURL string) (*CastFunder, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, fmt.Errorf("funder account is required; import one with cast wallet import")
	}
	return &CastFunder{account: account, rpcURL: rpcURL}, nil
}

// Address resolves the public address stored in the named keystore.
func (f *CastFunder) Address(ctx context.Context) (string, error) {
	var stdout bytes.Buffer
	command := exec.CommandContext(ctx, "cast", "wallet", "address", "--account", f.account)
	command.Stdin = os.Stdin
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("cast wallet address: %w", err)
	}
	address := strings.TrimSpace(stdout.String())
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return "", fmt.Errorf("cast returned invalid funder address %q", address)
	}
	return strings.ToLower(address), nil
}

// Send transfers native USDC. Cast inherits the terminal so it can securely prompt for the
// encrypted keystore password; no password or private key appears in the process arguments.
func (f *CastFunder) Send(ctx context.Context, address string, amount *big.Int) (string, error) {
	var stdout bytes.Buffer
	command := exec.CommandContext(
		ctx, "cast", "send", address,
		"--value", amount.String(),
		"--rpc-url", f.rpcURL,
		"--account", f.account,
		"--json",
	)
	command.Stdin = os.Stdin
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("cast send: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
