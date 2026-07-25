package advancing

import (
	"context"
	"testing"
)

// The advance intent's amount tracks the org's CURRENT chain rate, not a hardcoded
// 50%: a rate oracle reporting 5500 bps makes the intent 0.55 of a 1.00 quote (matching
// what FloatPool draws), and the purpose reads "55%". Without an oracle it falls back to
// the base rate.
func TestAdvance_IntentTracksChainRate(t *testing.T) {
	f, life, _, _, _ := rig(t)
	f.SetRateOracle(func(context.Context) (uint16, bool) { return 5500, true })

	req, err := f.Propose(context.Background(), "job_rate", "0x"+repeatHex64(), "1.00")
	if err != nil {
		t.Fatal(err)
	}
	if req.Intent.AmountMicros != 550_000 {
		t.Fatalf("advance principal %d, want 550000 (55%% of 1.00)", req.Intent.AmountMicros)
	}
	if want := "55% of the 1.00"; !contains(req.Intent.Purpose, want) {
		t.Fatalf("purpose %q, want it to state %q", req.Intent.Purpose, want)
	}

	// No oracle → base-rate fallback (50%).
	f.SetRateOracle(nil)
	req2, err := f.Propose(context.Background(), "job_rate2", "0x"+repeatHex64(), "1.00")
	if err != nil {
		t.Fatal(err)
	}
	if req2.Intent.AmountMicros != 500_000 {
		t.Fatalf("fallback principal %d, want 500000 (base 50%%)", req2.Intent.AmountMicros)
	}
	_ = life
}

func repeatHex64() string {
	s := ""
	for i := 0; i < 64; i++ {
		s += "a"
	}
	return s
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
