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
	source          *fakeBalances
	address         string
	gasCosts        []*big.Int
	estimateCalls   int
	actualGasCost   *big.Int
	sends           []Funding
	submittedBudget []GasBudget
}

func (f *fakeFunder) Address(context.Context) (string, error) {
	return f.address, nil
}

func (f *fakeFunder) EstimateGasBudget(context.Context, string, *big.Int) (GasBudget, error) {
	cost := new(big.Int)
	if len(f.gasCosts) > 0 {
		index := f.estimateCalls
		if index >= len(f.gasCosts) {
			index = len(f.gasCosts) - 1
		}
		cost.Set(f.gasCosts[index])
	}
	f.estimateCalls++
	return GasBudget{
		GasLimit:     big.NewInt(1),
		MaxFeePerGas: new(big.Int).Set(cost),
		MaxCost:      new(big.Int).Set(cost),
	}, nil
}

func (f *fakeFunder) Send(_ context.Context, address string, amount *big.Int, budget GasBudget) (string, error) {
	f.source.balances[f.address].Sub(f.source.balances[f.address], amount)
	if f.actualGasCost != nil {
		f.source.balances[f.address].Sub(f.source.balances[f.address], f.actualGasCost)
	}
	if f.source.balances[address] == nil {
		f.source.balances[address] = new(big.Int)
	}
	f.source.balances[address].Add(f.source.balances[address], amount)
	f.sends = append(f.sends, Funding{Address: address, Amount: new(big.Int).Set(amount)})
	f.submittedBudget = append(f.submittedBudget, budget)
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
	funder := &fakeFunder{
		source: source, address: "funder",
		gasCosts: []*big.Int{usdc(t, "0.01")}, actualGasCost: usdc(t, "0.01"),
	}
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
	if got := FormatUSDC(source.balances["funder"]); got != "0.89" {
		t.Fatalf("funder balance %s, want 0.89", got)
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

func TestEnsureWalletsIncludesGasWhenPreservingReserve(t *testing.T) {
	source := &fakeBalances{
		chainID: 5042002,
		balances: map[string]*big.Int{
			"customer": usdc(t, "20"),
			"funder":   usdc(t, "5.35"),
		},
	}
	funder := &fakeFunder{
		source: source, address: "funder",
		gasCosts: []*big.Int{usdc(t, "0.01")}, actualGasCost: usdc(t, "0.01"),
	}
	_, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "customer", Minimum: usdc(t, "25.10")},
	}, funder, usdc(t, "0.25"))
	if err == nil || !strings.Contains(err.Error(), "estimated gas") {
		t.Fatalf("expected gas-aware reserve failure, got %v", err)
	}
	if len(funder.sends) != 0 {
		t.Fatalf("sent funds before gas reserve guard: %+v", funder.sends)
	}
}

func TestEnsureWalletsRejectsDuplicateDestinations(t *testing.T) {
	source := &fakeBalances{chainID: 5042002, balances: map[string]*big.Int{"same": new(big.Int)}}
	_, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "one", Address: "SAME", Minimum: usdc(t, "1")},
		{Role: "two", Address: "same", Minimum: usdc(t, "2")},
	}, nil, new(big.Int))
	if err == nil || !strings.Contains(err.Error(), "share address") {
		t.Fatalf("expected duplicate destination failure, got %v", err)
	}
}

func TestEnsureWalletsRejectsFunderDestinationCollision(t *testing.T) {
	source := &fakeBalances{chainID: 5042002, balances: map[string]*big.Int{"funder": usdc(t, "1")}}
	funder := &fakeFunder{source: source, address: "FUNDER"}
	_, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "funder", Minimum: usdc(t, "2")},
	}, funder, new(big.Int))
	if err == nil || !strings.Contains(err.Error(), "self-funding") {
		t.Fatalf("expected funder collision failure, got %v", err)
	}
}

func TestEnsureWalletsRefreshesCappedGasBudgetBeforeSending(t *testing.T) {
	source := &fakeBalances{
		chainID: 5042002,
		balances: map[string]*big.Int{
			"customer": usdc(t, "20"),
			"funder":   usdc(t, "6"),
		},
	}
	funder := &fakeFunder{
		source: source, address: "funder",
		gasCosts:      []*big.Int{usdc(t, "0.01"), usdc(t, "0.02")},
		actualGasCost: usdc(t, "0.02"),
	}
	_, err := EnsureWallets(context.Background(), source, 5042002, []Wallet{
		{Role: "externalCustomer", Address: "customer", Minimum: usdc(t, "25.10")},
	}, funder, usdc(t, "0.25"))
	if err != nil {
		t.Fatal(err)
	}
	if funder.estimateCalls != 2 {
		t.Fatalf("gas budget estimates = %d, want preflight plus pre-send refresh", funder.estimateCalls)
	}
	if len(funder.submittedBudget) != 1 ||
		funder.submittedBudget[0].MaxCost.Cmp(usdc(t, "0.02")) != 0 {
		t.Fatalf("submitted budgets = %+v, want refreshed 0.02 maximum", funder.submittedBudget)
	}
	if source.balances["funder"].Cmp(usdc(t, "0.25")) < 0 {
		t.Fatalf("funder balance %s fell below reserve", FormatUSDC(source.balances["funder"]))
	}
}

func TestUSDCFormattingPreservesLeadingFractionalZeroes(t *testing.T) {
	if got := FormatUSDC(usdc(t, "0.000001")); got != "0.000001" {
		t.Fatalf("formatted %q, want 0.000001", got)
	}
}
