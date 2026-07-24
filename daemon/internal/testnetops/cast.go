package testnetops

import (
	"bytes"
	"context"
	"encoding/json"
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

// EstimateGasCost prices one native-USDC transfer at the current RPC gas price and adds
// 20% headroom. Arc charges gas in native USDC, so this cost must be reserved separately
// from the value sent or the configured funder reserve is not actually preserved.
func (f *CastFunder) EstimateGasCost(ctx context.Context, address string, amount *big.Int) (*big.Int, error) {
	from, err := f.Address(ctx)
	if err != nil {
		return nil, err
	}
	gasUnits, err := f.castDecimal(
		ctx, "estimate", address,
		"--value", amount.String(),
		"--rpc-url", f.rpcURL,
		"--from", from,
	)
	if err != nil {
		return nil, fmt.Errorf("estimating transfer gas: %w", err)
	}
	gasPrice, err := f.castDecimal(ctx, "gas-price", "--rpc-url", f.rpcURL)
	if err != nil {
		return nil, fmt.Errorf("reading gas price: %w", err)
	}
	cost := new(big.Int).Mul(gasUnits, gasPrice)
	// ceil(cost * 1.2) so rounding can never erase the safety margin.
	cost.Mul(cost, big.NewInt(12))
	cost.Add(cost, big.NewInt(9))
	return cost.Quo(cost, big.NewInt(10)), nil
}

func (f *CastFunder) castDecimal(ctx context.Context, args ...string) (*big.Int, error) {
	var stdout bytes.Buffer
	command := exec.CommandContext(ctx, "cast", args...)
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	value := strings.TrimSpace(stdout.String())
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || parsed.Sign() < 0 {
		return nil, fmt.Errorf("cast returned invalid decimal quantity %q", value)
	}
	return parsed, nil
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
	return parseCastTransactionHash(stdout.Bytes())
}

func parseCastTransactionHash(raw []byte) (string, error) {
	var receipt struct {
		TransactionHash string `json:"transactionHash"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return "", fmt.Errorf("decoding cast receipt: %w", err)
	}
	hash := strings.ToLower(strings.TrimSpace(receipt.TransactionHash))
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return "", fmt.Errorf("cast receipt has invalid transaction hash %q", receipt.TransactionHash)
	}
	if _, ok := new(big.Int).SetString(hash[2:], 16); !ok {
		return "", fmt.Errorf("cast receipt has invalid transaction hash %q", receipt.TransactionHash)
	}
	return hash, nil
}
