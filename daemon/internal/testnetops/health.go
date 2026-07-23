// Package testnetops provides guarded Arc testnet wallet-health operations.
package testnetops

import (
	"context"
	"fmt"
	"math/big"
	"strings"
)

var nativeUSDCScale = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// BalanceSource reads native USDC balances from one chain.
type BalanceSource interface {
	ChainID(context.Context) (uint64, error)
	Balance(context.Context, string) (*big.Int, error)
}

// Funder sends native USDC from a securely configured signer.
type Funder interface {
	Address(context.Context) (string, error)
	Send(context.Context, string, *big.Int) (string, error)
}

// Wallet describes the minimum native USDC required by one runtime role.
type Wallet struct {
	Role    string
	Address string
	Minimum *big.Int
}

// Funding records one top-up performed by EnsureWallets.
type Funding struct {
	Address string
	Amount  *big.Int
	TxHash  string
}

// WalletStatus is the before/after result for one wallet.
type WalletStatus struct {
	Role    string
	Address string
	Before  *big.Int
	After   *big.Int
	Minimum *big.Int
	Funding *Funding
}

// Report is healthy only when every wallet meets its configured minimum.
type Report struct {
	Healthy bool
	Wallets []WalletStatus
}

// EnsureWallets checks every wallet and, when a funder is supplied, tops up exact deficits.
// All preflight checks complete before the first send, including chain identity and the
// funder's post-funding gas reserve.
func EnsureWallets(
	ctx context.Context,
	source BalanceSource,
	expectedChainID uint64,
	wallets []Wallet,
	funder Funder,
	funderReserve *big.Int,
) (Report, error) {
	chainID, err := source.ChainID(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("reading chain ID: %w", err)
	}
	if chainID != expectedChainID {
		return Report{}, fmt.Errorf("RPC chain ID %d does not match deployment chain ID %d", chainID, expectedChainID)
	}
	if funderReserve == nil || funderReserve.Sign() < 0 {
		return Report{}, fmt.Errorf("funder reserve must be non-negative")
	}

	report := Report{Healthy: true, Wallets: make([]WalletStatus, 0, len(wallets))}
	totalDeficit := new(big.Int)
	for _, wallet := range wallets {
		if strings.TrimSpace(wallet.Role) == "" || strings.TrimSpace(wallet.Address) == "" {
			return Report{}, fmt.Errorf("wallet role and address are required")
		}
		if wallet.Minimum == nil || wallet.Minimum.Sign() < 0 {
			return Report{}, fmt.Errorf("wallet %s minimum must be non-negative", wallet.Role)
		}
		balance, err := source.Balance(ctx, wallet.Address)
		if err != nil {
			return Report{}, fmt.Errorf("reading %s balance: %w", wallet.Role, err)
		}
		status := WalletStatus{
			Role: wallet.Role, Address: wallet.Address, Before: balance,
			After: new(big.Int).Set(balance), Minimum: new(big.Int).Set(wallet.Minimum),
		}
		if balance.Cmp(wallet.Minimum) < 0 {
			report.Healthy = false
			totalDeficit.Add(totalDeficit, new(big.Int).Sub(wallet.Minimum, balance))
		}
		report.Wallets = append(report.Wallets, status)
	}
	if report.Healthy || funder == nil {
		return report, nil
	}

	funderAddress, err := funder.Address(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("resolving funder address: %w", err)
	}
	funderBalance, err := source.Balance(ctx, funderAddress)
	if err != nil {
		return Report{}, fmt.Errorf("reading funder balance: %w", err)
	}
	required := new(big.Int).Add(new(big.Int).Set(totalDeficit), funderReserve)
	if funderBalance.Cmp(required) < 0 {
		return Report{}, fmt.Errorf(
			"funder has %s USDC; %s required to cover deficits and preserve %s USDC reserve",
			FormatUSDC(funderBalance), FormatUSDC(required), FormatUSDC(funderReserve),
		)
	}

	for i := range report.Wallets {
		status := &report.Wallets[i]
		if status.Before.Cmp(status.Minimum) >= 0 {
			continue
		}
		deficit := new(big.Int).Sub(status.Minimum, status.Before)
		txHash, err := funder.Send(ctx, status.Address, deficit)
		if err != nil {
			return Report{}, fmt.Errorf("funding %s: %w", status.Role, err)
		}
		after, err := source.Balance(ctx, status.Address)
		if err != nil {
			return Report{}, fmt.Errorf("verifying %s balance: %w", status.Role, err)
		}
		if after.Cmp(status.Minimum) < 0 {
			return Report{}, fmt.Errorf(
				"funding %s did not reach minimum: got %s, want %s USDC",
				status.Role, FormatUSDC(after), FormatUSDC(status.Minimum),
			)
		}
		status.After = after
		status.Funding = &Funding{
			Address: status.Address, Amount: new(big.Int).Set(deficit), TxHash: strings.TrimSpace(txHash),
		}
	}
	report.Healthy = true
	return report, nil
}

// ParseUSDC converts a non-negative decimal USDC amount into Arc's 18-decimal native units.
func ParseUSDC(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return nil, fmt.Errorf("invalid USDC amount %q", value)
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, fmt.Errorf("invalid USDC amount %q", value)
	}
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok {
		return nil, fmt.Errorf("invalid USDC amount %q", value)
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 18 {
		return nil, fmt.Errorf("USDC amount %q has more than 18 decimals", value)
	}
	for _, char := range fraction {
		if char < '0' || char > '9' {
			return nil, fmt.Errorf("invalid USDC amount %q", value)
		}
	}
	fraction += strings.Repeat("0", 18-len(fraction))
	fractionValue := new(big.Int)
	if fraction != "" {
		fractionValue.SetString(fraction, 10)
	}
	return new(big.Int).Add(new(big.Int).Mul(whole, nativeUSDCScale), fractionValue), nil
}

// FormatUSDC formats Arc native units without trailing fractional zeroes.
func FormatUSDC(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	sign := ""
	value := new(big.Int).Set(amount)
	if value.Sign() < 0 {
		sign = "-"
		value.Abs(value)
	}
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(value, nativeUSDCScale, fraction)
	if fraction.Sign() == 0 {
		return sign + whole.String()
	}
	rawFraction := fraction.String()
	fractionText := strings.Repeat("0", 18-len(rawFraction)) + rawFraction
	fractionText = strings.TrimRight(fractionText, "0")
	return sign + whole.String() + "." + fractionText
}
