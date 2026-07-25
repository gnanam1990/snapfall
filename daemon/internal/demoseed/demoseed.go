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

// RequiredPoolTVL is the minimum pool TVL that lets a single advance of principalMicros pass
// the per-org exposure cap.
//
// The contract checks the position AFTER issuance:
//
//	orgOutstanding * 10_000 > ORG_EXPOSURE_CAP_BPS * totalAssets  ->  revert CapExceeded
//
// so the advance survives only while principal*10_000 <= capBps*TVL, i.e.
// TVL >= ceil(principal * 10_000 / capBps). The ceiling matters: integer division that
// rounded down would return a TVL one micro too small and still revert.
func RequiredPoolTVL(principalMicros *big.Int) *big.Int {
	num := new(big.Int).Mul(principalMicros, big.NewInt(BpsDenominator))
	den := big.NewInt(OrgExposureCapBps)
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		quo.Add(quo, big.NewInt(1))
	}
	return quo
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
	// RequiredTVLMicros is the exposure-cap floor for that advance.
	RequiredTVLMicros *big.Int
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
// (the demo default is 150 USDC); when the exposure-cap floor is higher, the floor wins, so a
// raised flywheel rate can never silently under-seed the next run.
func BuildPlan(priceMicros *big.Int, rateBps uint64, currentTVLMicros, desiredTVLMicros *big.Int) (Plan, error) {
	if priceMicros == nil || priceMicros.Sign() <= 0 {
		return Plan{}, errors.New("job price must be positive")
	}
	if rateBps == 0 {
		return Plan{}, errors.New("advance rate must be positive")
	}
	if currentTVLMicros == nil || currentTVLMicros.Sign() < 0 {
		return Plan{}, errors.New("current pool TVL must be zero or positive")
	}

	advance := AdvanceFor(priceMicros, rateBps)
	if advance.Sign() == 0 {
		return Plan{}, fmt.Errorf("advance rounds to zero at %d bps on %s micros", rateBps, priceMicros)
	}
	required := RequiredPoolTVL(advance)

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
	}, nil
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
