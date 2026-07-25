package demoseed

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fresh is the exposure of a pool with nothing lent out yet.
func fresh() Exposure { return Exposure{} }

// lent builds a live exposure from decimal USDC strings.
func lent(t *testing.T, org, total string) Exposure {
	t.Helper()
	return Exposure{OrgOutstandingMicros: usdc(t, org), TotalOutstandingMicros: usdc(t, total)}
}

func usdc(t *testing.T, v string) *big.Int {
	t.Helper()
	out, err := ParseUSDC(v)
	if err != nil {
		t.Fatalf("ParseUSDC(%q): %v", v, err)
	}
	return out
}

// The blocker this package exists for: the demo's 25.00 job at 50% draws 12.50, and a
// 100.00 pool is NOT enough — the exposure cap reverts it. 125.00 is the exact floor.
func TestTheHundredUSDCSeedIsTheBlocker(t *testing.T) {
	price := usdc(t, "25")
	advance := AdvanceFor(price, 5000)
	if got, want := advance.String(), usdc(t, "12.5").String(); got != want {
		t.Fatalf("advance = %s, want %s", got, want)
	}

	required := RequiredPoolTVL(advance, fresh())
	if got, want := required.String(), usdc(t, "125").String(); got != want {
		t.Fatalf("required TVL = %s, want exactly 125.00 (advance x 10)", got)
	}

	// A 100.00 seed is short, and the plan must say so by asking for a top-up.
	short, err := BuildPlan(price, 5000, usdc(t, "100"), usdc(t, "100"), fresh())
	if err != nil {
		t.Fatal(err)
	}
	if short.SeedAlreadySufficient() {
		t.Fatal("a 100.00 pool was reported sufficient for a 12.50 advance; the snap would revert CapExceeded")
	}
	if got, want := short.TargetTVLMicros.String(), required.String(); got != want {
		t.Fatalf("target TVL = %s, want the cap floor %s when the desired seed is below it", got, want)
	}
	if got, want := short.DepositMicros.String(), usdc(t, "25").String(); got != want {
		t.Fatalf("deposit = %s, want 25.00 to reach the 125.00 floor", got)
	}
}

// The contract compares the POST-issuance position, so the floor must be a ceiling
// division: one micro short still reverts.
func TestRequiredTVLRoundsUpNeverDown(t *testing.T) {
	// 3 micros at a 10% cap needs 30 exactly.
	if got := RequiredPoolTVL(big.NewInt(3), fresh()).String(); got != "30" {
		t.Fatalf("RequiredPoolTVL(3) = %s, want 30", got)
	}
	// An advance whose x10_000/1_000 division has a remainder must round up. 1 micro -> 10.
	if got := RequiredPoolTVL(big.NewInt(1), fresh()).String(); got != "10" {
		t.Fatalf("RequiredPoolTVL(1) = %s, want 10", got)
	}
	// Sanity: the returned floor always satisfies the contract's own inequality.
	for _, principal := range []string{"0.000001", "0.5", "12.5", "13.75", "99.999999"} {
		p := usdc(t, principal)
		tvl := RequiredPoolTVL(p, fresh())
		lhs := new(big.Int).Mul(p, big.NewInt(BpsDenominator))
		rhs := new(big.Int).Mul(big.NewInt(OrgExposureCapBps), tvl)
		if lhs.Cmp(rhs) > 0 {
			t.Fatalf("principal %s: floor %s still trips the cap (%s > %s)", principal, tvl, lhs, rhs)
		}
		// And one micro less does NOT satisfy it, proving the floor is tight.
		lower := new(big.Int).Sub(tvl, big.NewInt(1))
		rhsLower := new(big.Int).Mul(big.NewInt(OrgExposureCapBps), lower)
		if lhs.Cmp(rhsLower) <= 0 {
			t.Fatalf("principal %s: floor %s is not tight; %s would also pass", principal, tvl, lower)
		}
	}
}

// After one accepted job the flywheel raises the rate to 55%, which raises the advance to
// 13.75 and the floor to 137.50 — above the 125.00 that satisfied the first run. A seed that
// assumed 50% forever would under-seed the SECOND cycle.
func TestFlywheelRaisesTheFloorForTheNextRun(t *testing.T) {
	price := usdc(t, "25")

	first, err := BuildPlan(price, 5000, big.NewInt(0), usdc(t, "150"), fresh())
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan(price, 5500, first.TargetTVLMicros, usdc(t, "150"), fresh())
	if err != nil {
		t.Fatal(err)
	}

	if got, want := second.AdvanceMicros.String(), usdc(t, "13.75").String(); got != want {
		t.Fatalf("advance at 55%% = %s, want %s", got, want)
	}
	if got, want := second.RequiredTVLMicros.String(), usdc(t, "137.5").String(); got != want {
		t.Fatalf("required TVL at 55%% = %s, want %s", got, want)
	}
	// The demo default of 150 covers it, so no extra deposit is needed on the second run.
	if !second.SeedAlreadySufficient() {
		t.Fatalf("150.00 should already cover the 55%% cycle, got deposit %s", second.DepositMicros)
	}
}

// The floor must win over a too-small desired cushion even at a high rate.
func TestCapFloorOverridesDesiredSeed(t *testing.T) {
	plan, err := BuildPlan(usdc(t, "100"), 8500, big.NewInt(0), usdc(t, "150"), fresh())
	if err != nil {
		t.Fatal(err)
	}
	// 100 at 85% = 85 advance -> floor 850, far above the 150 cushion.
	if got, want := plan.TargetTVLMicros.String(), usdc(t, "850").String(); got != want {
		t.Fatalf("target = %s, want the %s floor rather than the 150.00 cushion", got, want)
	}
}

// Seeding must be idempotent: re-running against an already-deep pool asks for nothing, so
// repeated takes do not burn testnet USDC or drift the pool upward every time.
func TestSeedingIsIdempotent(t *testing.T) {
	plan, err := BuildPlan(usdc(t, "25"), 5000, usdc(t, "150"), usdc(t, "150"), fresh())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.SeedAlreadySufficient() || plan.DepositMicros.Sign() != 0 {
		t.Fatalf("expected no deposit against an already-seeded pool, got %s", plan.DepositMicros)
	}

	over, err := BuildPlan(usdc(t, "25"), 5000, usdc(t, "500"), usdc(t, "150"), fresh())
	if err != nil {
		t.Fatal(err)
	}
	if over.DepositMicros.Sign() != 0 {
		t.Fatalf("an over-full pool must never ask for a deposit, got %s", over.DepositMicros)
	}
}

func TestBuildPlanRejectsNonsense(t *testing.T) {
	if _, err := BuildPlan(big.NewInt(0), 5000, big.NewInt(0), nil, fresh()); err == nil {
		t.Fatal("a zero job price must be refused")
	}
	if _, err := BuildPlan(usdc(t, "25"), 0, big.NewInt(0), nil, fresh()); err == nil {
		t.Fatal("a zero advance rate must be refused")
	}
	if _, err := BuildPlan(usdc(t, "25"), 5000, big.NewInt(-1), nil, fresh()); err == nil {
		t.Fatal("a negative pool TVL must be refused")
	}
	// A price so small the advance truncates to zero cannot be seeded meaningfully.
	if _, err := BuildPlan(big.NewInt(1), 5000, big.NewInt(0), nil, fresh()); err == nil {
		t.Fatal("a price whose advance rounds to zero must be refused")
	}
}

// A fresh job id per run is what makes reset -> spine -> reset -> spine work: createJob
// reverts with JobExists on reuse and the chain has no delete.
func TestJobIDIsStablePerRunAndUniqueAcrossRuns(t *testing.T) {
	a := JobID("run-1")
	again := JobID("run-1")
	b := JobID("run-2")

	if a != again {
		t.Fatal("the same run id must derive the same job id")
	}
	if a == b {
		t.Fatal("different run ids must derive different job ids, or the second take reverts JobExists")
	}
	var zero [32]byte
	if a == zero {
		t.Fatal("job id must not be the zero word")
	}
}

func TestParseAndFormatUSDCRoundTrip(t *testing.T) {
	cases := map[string]string{
		"150":      "150000000",
		"12.5":     "12500000",
		"0.040000": "40000",
		"0.06":     "60000",
		"25.00":    "25000000",
	}
	for in, wantMicros := range cases {
		got, err := ParseUSDC(in)
		if err != nil {
			t.Fatalf("ParseUSDC(%q): %v", in, err)
		}
		if got.String() != wantMicros {
			t.Fatalf("ParseUSDC(%q) = %s, want %s", in, got, wantMicros)
		}
	}

	if _, err := ParseUSDC("1.1234567"); err == nil {
		t.Fatal("7 decimal places must be refused rather than truncated")
	}
	if _, err := ParseUSDC("-5"); err == nil {
		t.Fatal("a negative amount must be refused")
	}
	if _, err := ParseUSDC("abc"); err == nil {
		t.Fatal("garbage must be refused")
	}

	if got := FormatUSDC(usdc(t, "12.5")); got != "12.50" {
		t.Fatalf("FormatUSDC(12.5) = %q, want \"12.50\"", got)
	}
	if got := FormatUSDC(usdc(t, "150")); got != "150.00" {
		t.Fatalf("FormatUSDC(150) = %q, want \"150.00\"", got)
	}
	if got := FormatUSDC(usdc(t, "0.04")); got != "0.04" {
		t.Fatalf("FormatUSDC(0.04) = %q, want \"0.04\"", got)
	}
}

// The caps above are copied from the frozen contract; if someone edits FloatPool.sol these
// constants must be revisited, so pin them against the source rather than trusting a comment.
func TestPoolCapsMatchContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "src", "FloatPool.sol")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract source not available (%v)", err)
	}
	text := string(src)

	for _, want := range []struct {
		name  string
		value int
	}{
		{"ORG_EXPOSURE_CAP_BPS", OrgExposureCapBps},
		{"UTILIZATION_CAP_BPS", UtilizationCapBps},
	} {
		re := regexp.MustCompile(want.name + `\s*=\s*(\d+)`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("could not find %s in %s", want.name, path)
		}
		if m[1] != strings.TrimSpace(itoa(want.value)) {
			t.Fatalf("%s is %s in the contract but %d in demoseed", want.name, m[1], want.value)
		}
	}
}

func itoa(v int) string { return big.NewInt(int64(v)).String() }

// The review finding on PR #41: sizing the pool from the NEW principal alone under-seeds a pool
// that already carries exposure, because FloatPool checks the position AFTER issuance. The
// command would print "ready" and requestAdvance would still revert CapExceeded.
func TestFloorAccountsForExistingExposure(t *testing.T) {
	advance := usdc(t, "12.5")

	// Nothing lent yet: 12.50 needs 125.00, the number the demo runbook quotes.
	if got, want := RequiredPoolTVL(advance, fresh()).String(), usdc(t, "125").String(); got != want {
		t.Fatalf("fresh floor = %s, want %s", got, want)
	}

	// The same org already owes 12.50 from a take that was never settled. The org cap sees
	// 25.00 after issuance, so the pool needs 250.00, double what the old arithmetic asked for.
	withOrg := RequiredPoolTVL(advance, lent(t, "12.5", "12.5"))
	if got, want := withOrg.String(), usdc(t, "250").String(); got != want {
		t.Fatalf("floor with 12.50 already owed by this org = %s, want %s", got, want)
	}

	// Prove it against the contract's own inequality, which the old signature could not see.
	orgAfter := new(big.Int).Add(usdc(t, "12.5"), advance)
	if lhs, rhs := new(big.Int).Mul(orgAfter, big.NewInt(BpsDenominator)),
		new(big.Int).Mul(big.NewInt(OrgExposureCapBps), usdc(t, "125")); lhs.Cmp(rhs) <= 0 {
		t.Fatal("125.00 should NOT satisfy the org cap once 12.50 is already outstanding; the test is not proving anything")
	}
}

// The utilization cap is a second, independent constraint. One org can sit well inside its 10%
// exposure limit while the pool as a whole is over 80% lent out.
func TestUtilizationCapCanBindInsteadOfOrgCap(t *testing.T) {
	advance := usdc(t, "12.5")

	// This org owes nothing, but other orgs owe 500.00. Post-issuance total is 512.50, so the
	// 80% cap demands ceil(512.50 * 1.25) = 640.625 -> 640.63 rather than the org cap's 125.00.
	floor := RequiredPoolTVL(advance, lent(t, "0", "500"))
	if got, want := floor.String(), usdc(t, "640.625").String(); got != want {
		t.Fatalf("floor = %s, want the utilization floor %s", got, want)
	}

	plan, err := BuildPlan(usdc(t, "25"), 5000, usdc(t, "150"), usdc(t, "150"), lent(t, "0", "500"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.BindingCap != "utilization" {
		t.Fatalf("binding cap = %q, want \"utilization\" so the printed plan explains the number", plan.BindingCap)
	}
	if plan.SeedAlreadySufficient() {
		t.Fatal("a 150.00 pool that is already 500.00 lent out cannot fund another advance")
	}

	// And when this org is the one carrying the exposure, the org cap binds again.
	orgBound, err := BuildPlan(usdc(t, "25"), 5000, usdc(t, "150"), usdc(t, "150"), lent(t, "12.5", "12.5"))
	if err != nil {
		t.Fatal(err)
	}
	if orgBound.BindingCap != "org exposure" {
		t.Fatalf("binding cap = %q, want \"org exposure\"", orgBound.BindingCap)
	}
}

// Both floors must satisfy the contract's inequalities exactly, and be tight: one micro less
// must fail. This is the property that makes "ready" trustworthy.
func TestBothFloorsAreTightAgainstTheContract(t *testing.T) {
	cases := []Exposure{
		fresh(),
		lent(t, "0", "500"),
		lent(t, "12.5", "12.5"),
		lent(t, "12.5", "137.5"),
		lent(t, "0.000001", "0.000001"),
	}
	for _, e := range cases {
		for _, principal := range []string{"0.000001", "12.5", "13.75", "85"} {
			p := usdc(t, principal)
			tvl := RequiredPoolTVL(p, e)
			orgAfter := new(big.Int).Add(orZero(e.OrgOutstandingMicros), p)
			totalAfter := new(big.Int).Add(orZero(e.TotalOutstandingMicros), p)

			trips := func(after *big.Int, capBps int64, assets *big.Int) bool {
				lhs := new(big.Int).Mul(after, big.NewInt(BpsDenominator))
				rhs := new(big.Int).Mul(big.NewInt(capBps), assets)
				return lhs.Cmp(rhs) > 0
			}
			if trips(orgAfter, OrgExposureCapBps, tvl) {
				t.Fatalf("exposure %v principal %s: floor %s still trips the org cap", e, principal, tvl)
			}
			if trips(totalAfter, UtilizationCapBps, tvl) {
				t.Fatalf("exposure %v principal %s: floor %s still trips the utilization cap", e, principal, tvl)
			}
			// Tightness: one micro less trips at least one cap.
			lower := new(big.Int).Sub(tvl, big.NewInt(1))
			if !trips(orgAfter, OrgExposureCapBps, lower) && !trips(totalAfter, UtilizationCapBps, lower) {
				t.Fatalf("exposure %v principal %s: floor %s is not tight; %s would also pass", e, principal, tvl, lower)
			}
		}
	}
}

// The third revert path, InsufficientLiquidity, needs no term of its own: it asks for
// TVL >= totalOutstanding_after and the utilization floor already asks for 1.25x that. If
// UTILIZATION_CAP_BPS ever rises above 10_000 this stops being true, so pin it.
func TestUtilizationFloorCoversLiquidity(t *testing.T) {
	if UtilizationCapBps > BpsDenominator {
		t.Fatal("a utilization cap above 100% would let the floor fall below the pool's idle cash; " +
			"RequiredPoolTVL then needs an explicit liquidity term")
	}
	for _, e := range []Exposure{fresh(), lent(t, "0", "500"), lent(t, "12.5", "137.5")} {
		p := usdc(t, "12.5")
		tvl := RequiredPoolTVL(p, e)
		totalAfter := new(big.Int).Add(orZero(e.TotalOutstandingMicros), p)
		if tvl.Cmp(totalAfter) < 0 {
			t.Fatalf("floor %s is below outstanding-after %s; requestAdvance would revert InsufficientLiquidity", tvl, totalAfter)
		}
	}
}

// A torn read (org owing more than the whole pool) cannot have come from one block, and
// planning on it would produce a confident number from inconsistent inputs.
func TestBuildPlanRejectsInconsistentExposure(t *testing.T) {
	if _, err := BuildPlan(usdc(t, "25"), 5000, big.NewInt(0), nil, lent(t, "100", "50")); err == nil {
		t.Fatal("an org owing more than the whole pool must be refused as a torn read")
	}
	neg := Exposure{OrgOutstandingMicros: big.NewInt(-1), TotalOutstandingMicros: big.NewInt(0)}
	if _, err := BuildPlan(usdc(t, "25"), 5000, big.NewInt(0), nil, neg); err == nil {
		t.Fatal("a negative org outstanding must be refused")
	}
}

// A nil Exposure must behave exactly like a fresh pool rather than panicking, because a
// read that returned nothing should not silently become a smaller floor.
func TestNilExposureIsTreatedAsFresh(t *testing.T) {
	advance := usdc(t, "12.5")
	if got, want := RequiredPoolTVL(advance, Exposure{}).String(), RequiredPoolTVL(advance, lent(t, "0", "0")).String(); got != want {
		t.Fatalf("nil exposure floor = %s, want %s", got, want)
	}
	plan, err := BuildPlan(usdc(t, "25"), 5000, big.NewInt(0), usdc(t, "150"), Exposure{})
	if err != nil {
		t.Fatal(err)
	}
	// The plan must still report a concrete exposure so the printed output has no nil holes.
	if plan.Exposure.OrgOutstandingMicros == nil || plan.Exposure.TotalOutstandingMicros == nil {
		t.Fatal("plan exposure must be materialized, not nil")
	}
}
