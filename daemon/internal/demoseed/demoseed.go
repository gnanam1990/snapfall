// Package demoseed holds the arithmetic behind the demo seed (V12).
//
// It exists as its own package because one of these numbers is a live demo blocker: the
// FloatPool refuses an advance that would push a single org past its exposure cap, so a pool
// seeded too thin makes the 0:30 "snap" beat revert on camera with CapExceeded. That
// constraint is arithmetic, not chain state, so it is unit-tested here rather than discovered
// during a recording take.
//
// Everything is integer micros (atomic USDC, 6dp). No floats touch a money figure.
package demoseed

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/crypto"
)

// FloatPool constants, mirrored from the frozen contract (contracts/src/FloatPool.sol).
// They are duplicated deliberately: the seed must be computable without an RPC round trip,
// and PoolCapsMatchContract in the test file pins them against the source of truth.
const (
	// OrgExposureCapBps caps one org's outstanding principal at 10% of pool assets.
	OrgExposureCapBps = 1000
	// UtilizationCapBps caps total outstanding at 80% of pool assets.
	UtilizationCapBps = 8000
	// BpsDenominator is the basis-point denominator used by the contract.
	BpsDenominator = 10000
)

// AdvanceFor returns the advance a job of priceMicros draws at rateBps, exactly as
// FloatPool.requestAdvance computes it: principal = customerPayment * rateBps / 10_000,
// truncating like Solidity integer division.
func AdvanceFor(priceMicros *big.Int, rateBps uint64) *big.Int {
	out := new(big.Int).Mul(priceMicros, big.NewInt(int64(rateBps)))
	return out.Div(out, big.NewInt(BpsDenominator))
}

// capFloor is the minimum totalAssets that keeps a post-issuance outstanding figure inside a
// basis-point cap.
//
// The contract compares outstanding*10_000 against capBps*totalAssets and reverts when the
// former is strictly greater, so the position survives while
// TVL >= ceil(outstanding * 10_000 / capBps). The ceiling matters: integer division that
// rounded down would return a TVL one micro too small and still revert.
func capFloor(afterMicros *big.Int, capBps int64) *big.Int {
	num := new(big.Int).Mul(afterMicros, big.NewInt(BpsDenominator))
	quo, rem := new(big.Int).QuoRem(num, big.NewInt(capBps), new(big.Int))
	if rem.Sign() != 0 {
		quo.Add(quo, big.NewInt(1))
	}
	return quo
}

// Exposure is the pool's live lending position, read from the chain: what this org already owes
// and what every org together already owes.
type Exposure struct {
	OrgOutstandingMicros   *big.Int
	TotalOutstandingMicros *big.Int
}

// RequiredPoolTVL is the minimum pool TVL that lets an advance of principalMicros land on top
// of the exposure the pool ALREADY carries.
//
// FloatPool.requestAdvance adds the principal to both counters and then checks both caps
// against the post-issuance position:
//
//	orgOutstanding[org] * 10_000 > ORG_EXPOSURE_CAP_BPS * totalAssets  ->  CapExceeded
//	totalOutstanding     * 10_000 > UTILIZATION_CAP_BPS  * totalAssets  ->  CapExceeded
//
// Sizing from the new principal alone therefore under-seeds any pool with an open advance
// still on the books: the plan prints "ready" and the snap reverts anyway. The binding floor is
// whichever of the two caps demands more capital.
//
// The third constraint, `principal > totalAssets - totalOutstanding -> InsufficientLiquidity`,
// needs no term of its own: it asks for TVL >= totalOutstanding_after, and the utilization
// floor already asks for 1.25x that. TestUtilizationFloorCoversLiquidity pins that.
func RequiredPoolTVL(principalMicros *big.Int, exposure Exposure) *big.Int {
	orgAfter := new(big.Int).Add(orZero(exposure.OrgOutstandingMicros), principalMicros)
	totalAfter := new(big.Int).Add(orZero(exposure.TotalOutstandingMicros), principalMicros)

	orgFloor := capFloor(orgAfter, OrgExposureCapBps)
	utilFloor := capFloor(totalAfter, UtilizationCapBps)
	if utilFloor.Cmp(orgFloor) > 0 {
		return utilFloor
	}
	return orgFloor
}

func orZero(v *big.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v
}

// Plan is the seeding work a run needs, derived from live chain reads plus the desired target.
type Plan struct {
	// PriceMicros is the customer payment the demo job is created with.
	PriceMicros *big.Int
	// RateBps is the org's CURRENT on-chain advance rate (never assumed to be 50%: after an
	// accepted job the flywheel has already raised it, which raises the advance and therefore
	// the TVL the next run needs).
	RateBps uint64
	// AdvanceMicros is the principal the snap will draw at RateBps.
	AdvanceMicros *big.Int
	// RequiredTVLMicros is the cap floor for that advance ON TOP OF existing exposure.
	RequiredTVLMicros *big.Int
	// Exposure is what the pool already owes, read live. Sizing without it under-seeds a pool
	// that still carries an open advance.
	Exposure Exposure
	// BindingCap names the constraint that set RequiredTVLMicros ("org exposure" or
	// "utilization"), so the printed plan explains itself instead of asserting a number.
	BindingCap string
	// TargetTVLMicros is what the pool will hold after seeding.
	TargetTVLMicros *big.Int
	// CurrentTVLMicros is what the pool holds now.
	CurrentTVLMicros *big.Int
	// DepositMicros is the top-up to submit; zero when the pool is already deep enough.
	DepositMicros *big.Int
}

// SeedAlreadySufficient reports whether the pool needs no deposit at all.
func (p Plan) SeedAlreadySufficient() bool { return p.DepositMicros.Sign() == 0 }

// BuildPlan derives the seeding plan. desiredTVLMicros is the operator's preferred cushion
// (the demo default is 150 USDC); when the cap floor is higher, the floor wins, so neither a
// raised flywheel rate nor exposure left over from a previous run can silently under-seed.
func BuildPlan(priceMicros *big.Int, rateBps uint64, currentTVLMicros, desiredTVLMicros *big.Int, exposure Exposure) (Plan, error) {
	if priceMicros == nil || priceMicros.Sign() <= 0 {
		return Plan{}, errors.New("job price must be positive")
	}
	if rateBps == 0 {
		return Plan{}, errors.New("advance rate must be positive")
	}
	if currentTVLMicros == nil || currentTVLMicros.Sign() < 0 {
		return Plan{}, errors.New("current pool TVL must be zero or positive")
	}
	if exposure.OrgOutstandingMicros != nil && exposure.OrgOutstandingMicros.Sign() < 0 {
		return Plan{}, errors.New("org outstanding must be zero or positive")
	}
	if exposure.TotalOutstandingMicros != nil && exposure.TotalOutstandingMicros.Sign() < 0 {
		return Plan{}, errors.New("total outstanding must be zero or positive")
	}
	// An org cannot owe more than everyone owes together. If the two reads disagree they did not
	// come from the same block, and planning on a torn read is how you get a confident "ready"
	// followed by a revert.
	if orZero(exposure.OrgOutstandingMicros).Cmp(orZero(exposure.TotalOutstandingMicros)) > 0 {
		return Plan{}, fmt.Errorf("inconsistent exposure: org owes %s but the whole pool owes %s",
			exposure.OrgOutstandingMicros, exposure.TotalOutstandingMicros)
	}

	advance := AdvanceFor(priceMicros, rateBps)
	if advance.Sign() == 0 {
		return Plan{}, fmt.Errorf("advance rounds to zero at %d bps on %s micros", rateBps, priceMicros)
	}
	required := RequiredPoolTVL(advance, exposure)
	binding := "org exposure"
	if capFloor(new(big.Int).Add(orZero(exposure.TotalOutstandingMicros), advance), UtilizationCapBps).Cmp(
		capFloor(new(big.Int).Add(orZero(exposure.OrgOutstandingMicros), advance), OrgExposureCapBps)) > 0 {
		binding = "utilization"
	}

	target := new(big.Int).Set(required)
	if desiredTVLMicros != nil && desiredTVLMicros.Cmp(target) > 0 {
		target = new(big.Int).Set(desiredTVLMicros)
	}

	deposit := new(big.Int).Sub(target, currentTVLMicros)
	if deposit.Sign() < 0 {
		deposit = big.NewInt(0)
	}

	return Plan{
		PriceMicros:       new(big.Int).Set(priceMicros),
		RateBps:           rateBps,
		AdvanceMicros:     advance,
		RequiredTVLMicros: required,
		TargetTVLMicros:   target,
		CurrentTVLMicros:  new(big.Int).Set(currentTVLMicros),
		DepositMicros:     deposit,
		Exposure: Exposure{
			OrgOutstandingMicros:   new(big.Int).Set(orZero(exposure.OrgOutstandingMicros)),
			TotalOutstandingMicros: new(big.Int).Set(orZero(exposure.TotalOutstandingMicros)),
		},
		BindingCap: binding,
	}, nil
}

// Controls is the absolute capital-control state (docs/MAINNET.md) that JobVault.createJob/fund
// and FloatPool.requestAdvance check before the percentage caps above. Those caps scale with TVL,
// so a plan that only models them can print "ready" on a pool that will still refuse the snap.
//
// A pool deployed before the controls existed (the Arc testnet pool today) has none of these
// views. Present is then false and there is nothing to check.
type Controls struct {
	Present     bool
	PoolPaused  bool
	VaultPaused bool
	// MaxTotalExposure bounds open principal PLUS every loss already written off (#107), so
	// TotalWrittenOff is part of the check, not a statistic.
	MaxTotalExposure *big.Int
	MaxAdvance       *big.Int
	TotalWrittenOff  *big.Int
	MaxJobPayment    *big.Int
}

// LossBudgetLeft is what the exposure ceiling still allows on top of the pool's open book:
// maxTotalExposure - totalOutstanding - totalWrittenOff, floored at zero.
func LossBudgetLeft(c Controls, exposure Exposure) *big.Int {
	left := new(big.Int).Sub(orZero(c.MaxTotalExposure), orZero(exposure.TotalOutstandingMicros))
	left.Sub(left, orZero(c.TotalWrittenOff))
	if left.Sign() < 0 {
		return new(big.Int)
	}
	return left
}

// ControlBlocker returns why the run would revert under the capital controls, naming the
// contract's own error, or "" when it would not. Checks follow the contracts' order: createJob
// and fund on JobVault, then requestAdvance on FloatPool.
func ControlBlocker(c Controls, plan Plan) string {
	if !c.Present {
		return ""
	}
	if c.VaultPaused {
		return "JobVault is paused, so createJob and fund revert EnforcedPause"
	}
	if orZero(c.MaxJobPayment).Sign() == 0 {
		return "JobVault.maxJobPayment is unset, so createJob reverts CapNotSet"
	}
	if plan.PriceMicros.Cmp(c.MaxJobPayment) > 0 {
		return fmt.Sprintf("the job price %s USDC is above JobVault.maxJobPayment %s, so createJob reverts JobTooLarge",
			FormatUSDC(plan.PriceMicros), FormatUSDC(c.MaxJobPayment))
	}
	if c.PoolPaused {
		return "FloatPool is paused, so the snap reverts EnforcedPause"
	}
	if orZero(c.MaxTotalExposure).Sign() == 0 || orZero(c.MaxAdvance).Sign() == 0 {
		return "FloatPool caps are unset, so the snap reverts CapNotSet"
	}
	if plan.AdvanceMicros.Cmp(c.MaxAdvance) > 0 {
		return fmt.Sprintf("the advance %s USDC is above FloatPool.maxAdvance %s, so the snap reverts AdvanceTooLarge",
			FormatUSDC(plan.AdvanceMicros), FormatUSDC(c.MaxAdvance))
	}
	if left := LossBudgetLeft(c, plan.Exposure); plan.AdvanceMicros.Cmp(left) > 0 {
		return fmt.Sprintf("the advance %s USDC exceeds what maxTotalExposure %s still allows (%s: %s open, %s already written off), so the snap reverts ExposureCapExceeded",
			FormatUSDC(plan.AdvanceMicros), FormatUSDC(c.MaxTotalExposure), FormatUSDC(left),
			FormatUSDC(orZero(plan.Exposure.TotalOutstandingMicros)), FormatUSDC(orZero(c.TotalWrittenOff)))
	}
	return ""
}

// JobID derives the bytes32 vault job id for a run.
//
// Every run gets a FRESH id, because JobVault.createJob reverts with JobExists once an id is
// used and the chain has no delete. That is precisely what makes the release gate's
// "reset -> spine -> reset -> spine" possible without hand-editing anything between takes.
func JobID(runID string) [32]byte {
	var out [32]byte
	copy(out[:], crypto.Keccak256([]byte("snapfall/demo-job/"+runID)))
	return out
}

// FormatUSDC renders atomic micros as a plain decimal string with 6 places, trimmed to at
// least two, for human-facing plan output.
func FormatUSDC(micros *big.Int) string {
	if micros == nil {
		return "0.00"
	}
	neg := micros.Sign() < 0
	abs := new(big.Int).Abs(micros)
	whole := new(big.Int).Quo(abs, big.NewInt(1_000_000))
	frac := new(big.Int).Rem(abs, big.NewInt(1_000_000))
	s := fmt.Sprintf("%s.%06d", whole.String(), frac.Int64())
	// trim trailing zeros but keep two decimals
	for len(s) > 0 && s[len(s)-1] == '0' && s[len(s)-3] != '.' {
		s = s[:len(s)-1]
	}
	if neg {
		return "-" + s
	}
	return s
}

// ParseUSDC parses a decimal USDC string ("150", "12.5", "0.040000") into atomic micros.
// Fail-closed: more than 6 decimal places is an error rather than a silent truncation of
// someone's money.
func ParseUSDC(v string) (*big.Int, error) {
	if v == "" {
		return nil, errors.New("empty USDC amount")
	}
	whole, frac := v, ""
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			whole, frac = v[:i], v[i+1:]
			break
		}
	}
	if len(frac) > 6 {
		return nil, fmt.Errorf("%q has more than 6 decimal places (USDC is 6dp)", v)
	}
	for len(frac) < 6 {
		frac += "0"
	}
	if whole == "" {
		whole = "0"
	}
	out, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || out.Sign() < 0 {
		return nil, fmt.Errorf("%q is not a valid non-negative USDC amount", v)
	}
	return out, nil
}
