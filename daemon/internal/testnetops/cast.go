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

// EstimateGasBudget caps gas units and fee per gas with 20% headroom on each.
// Passing those same caps to Send makes their product a hard native-USDC fee bound.
func (f *CastFunder) EstimateGasBudget(ctx context.Context, address string, amount *big.Int) (GasBudget, error) {
	from, err := f.Address(ctx)
	if err != nil {
		return GasBudget{}, err
	}
	gasUnits, err := f.castDecimal(
		ctx, "estimate", address,
		"--value", amount.String(),
		"--rpc-url", f.rpcURL,
		"--from", from,
	)
	if err != nil {
		return GasBudget{}, fmt.Errorf("estimating transfer gas: %w", err)
	}
	gasPrice, err := f.castDecimal(ctx, "gas-price", "--rpc-url", f.rpcURL)
	if err != nil {
		return GasBudget{}, fmt.Errorf("reading gas price: %w", err)
	}
	gasLimit := addTwentyPercent(gasUnits)
	maxFeePerGas := addTwentyPercent(gasPrice)
	return GasBudget{
		GasLimit:     gasLimit,
		MaxFeePerGas: maxFeePerGas,
		MaxCost:      new(big.Int).Mul(gasLimit, maxFeePerGas),
	}, nil
}

func addTwentyPercent(value *big.Int) *big.Int {
	result := new(big.Int).Mul(value, big.NewInt(12))
	result.Add(result, big.NewInt(9))
	return result.Quo(result, big.NewInt(10))
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
func (f *CastFunder) Send(
	ctx context.Context,
	address string,
	amount *big.Int,
	budget GasBudget,
) (string, error) {
	if err := validateGasBudget(budget); err != nil {
		return "", fmt.Errorf("invalid gas budget: %w", err)
	}
	var stdout bytes.Buffer
	command := exec.CommandContext(
		ctx, "cast", "send", address,
		"--value", amount.String(),
		"--gas-limit", budget.GasLimit.String(),
		"--gas-price", budget.MaxFeePerGas.String(),
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
