package testnetops

import (
	"context"
	"math/big"
	"strings"
	"testing"
)

type fakeBalances struct {
	chainID  uint64
	balances map[string]*big.Int
}

func (f *fakeBalances) ChainID(context.Context) (uint64, error) {
	return f.chainID, nil
}

func (f *fakeBalances) Balance(_ context.Context, address string) (*big.Int, error) {
	balance := f.balances[address]
	if balance == nil {
		return new(big.Int), nil
	}
	return new(big.Int).Set(balance), nil
}

type fakeFunder struct {
	source  *fakeBalances
	address string
	sends   []Funding
}

func (f *fakeFunder) Address(context.Context) (string, error) {
	return f.address, nil
}

func (f *fakeFunder) Send(_ context.Context, address string, amount *big.Int) (string, error) {
	f.source.balances[f.address].Sub(f.source.balances[f.address], amount)
	if f.source.balances[address] == nil {
		f.source.balances[address] = new(big.Int)
	}
	f.source.balances[address].Add(f.source.balances[address], amount)
	f.sends = append(f.sends, Funding{Address: address, Amount: new(big.Int).Set(amount)})
	return "0xfeed", nil
}

func usdc(t *testing.T, value string) *big.Int {
	t.Helper()
	amount, err := ParseUSDC(value)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func TestEnsureWalletsReportsHealthyWithoutFunding(t *testing.T) {
	source := &fakeBalances{
		chainID: 5042002,
		balances: map[string]*big.Int{
			"customer": usdc(t, "25.10"),
			"treasury": usdc(t, "0"),
		},
	}
	report, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "customer", Minimum: usdc(t, "25.10")},
		{Role: "operatorTreasury", Address: "treasury", Minimum: usdc(t, "0")},
	}, nil, usdc(t, "0.25"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || len(report.Wallets) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestEnsureWalletsFundsExactDeficitAndPreservesReserve(t *testing.T) {
	source := &fakeBalances{
		chainID: 5042002,
		balances: map[string]*big.Int{
			"customer": usdc(t, "20"),
			"funder":   usdc(t, "6"),
		},
	}
	funder := &fakeFunder{source: source, address: "funder"}
	report, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "customer", Minimum: usdc(t, "25.10")},
	}, funder, usdc(t, "0.25"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || len(funder.sends) != 1 {
		t.Fatalf("report = %+v, sends = %+v", report, funder.sends)
	}
	if got := FormatUSDC(funder.sends[0].Amount); got != "5.1" {
		t.Fatalf("funded %s, want 5.1", got)
	}
	if got := FormatUSDC(source.balances["funder"]); got != "0.9" {
		t.Fatalf("funder balance %s, want 0.9", got)
	}
}

func TestEnsureWalletsFailsWhenFunderCannotKeepReserve(t *testing.T) {
	source := &fakeBalances{
		chainID: 5042002,
		balances: map[string]*big.Int{
			"customer": usdc(t, "20"),
			"funder":   usdc(t, "5.20"),
		},
	}
	funder := &fakeFunder{source: source, address: "funder"}
	_, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "customer", Minimum: usdc(t, "25.10")},
	}, funder, usdc(t, "0.25"))
	if err == nil || !strings.Contains(err.Error(), "reserve") {
		t.Fatalf("expected reserve failure, got %v", err)
	}
	if len(funder.sends) != 0 {
		t.Fatalf("sent funds before reserve guard: %+v", funder.sends)
	}
}

func TestEnsureWalletsRejectsWrongChainBeforeFunding(t *testing.T) {
	source := &fakeBalances{chainID: 1, balances: map[string]*big.Int{}}
	_, err := EnsureWallets(context.Background(), source, 5042002, nil, nil, new(big.Int))
	if err == nil || !strings.Contains(err.Error(), "chain ID") {
		t.Fatalf("expected chain mismatch, got %v", err)
	}
}

func TestUSDCFormattingPreservesLeadingFractionalZeroes(t *testing.T) {
	if got := FormatUSDC(usdc(t, "0.000001")); got != "0.000001" {
		t.Fatalf("formatted %q, want 0.000001", got)
	}
}
