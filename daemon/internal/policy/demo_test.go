package policy

import (
	"strings"
	"testing"
)

// Step-2 follow-up A: the demo depends on specific merchants being allowlisted BEFORE
// recording week. The seed script that creates the merchants is Vasanth's stream (V2/V12);
// this test is the tripwire that turns silent drift into a red suite.

// Every demo merchant is allowlisted in the canonical demo policy. If V2/V12 renames a
// merchant, DemoMerchant* must be updated in one place and this keeps the beats honest.
func TestDemoPolicy_AllowlistsEveryDemoMerchant(t *testing.T) {
	cfg := DemoPolicy()
	allow := make(map[string]bool, len(cfg.MerchantAllowlist))
	for _, m := range cfg.MerchantAllowlist {
		allow[m] = true
	}

	for name, merchant := range map[string]string{
		"$0.04 company profile (AT-02)": DemoMerchantProfile,
		"$4.00 premium dataset (AT-03)": DemoMerchantPremium,
		"$0.06 benchmark alt (AT-04)":   DemoMerchantBenchmark,
	} {
		if !allow[merchant] {
			t.Errorf("%s merchant %q is NOT allowlisted — this fails during a take, not a test", name, merchant)
		}
	}
}

// The three demo beats produce their scripted outcomes against the canonical config.
// This is the whole 1:10-1:30 demo sequence as one deterministic test.
func TestDemoPolicy_ProducesTheScriptedBeats(t *testing.T) {
	cfg := DemoPolicy()
	state := SpendState{}

	// Beat 1: $0.04 auto-approves.
	d := Evaluate(cfg, state, intent(40_000, DemoMerchantProfile))
	if d.Outcome != AutoApprove {
		t.Errorf("$0.04 beat: %s (reason %+v), want AUTO_APPROVE", d.Outcome, d.Reason)
	}

	// Beat 2: $4.00 escalates to the owner.
	d = Evaluate(cfg, SpendState{JobCommittedMicros: 40_000, DailySpentMicros: 40_000},
		intent(4_000_000, DemoMerchantPremium))
	if d.Outcome != HumanApprovalRequired {
		t.Errorf("$4.00 beat: %s, want HUMAN_APPROVAL_REQUIRED", d.Outcome)
	}

	// Beat 3: the $0.06 alternative auto-approves — NO second prompt after the rejection.
	d = Evaluate(cfg, SpendState{JobCommittedMicros: 40_000, DailySpentMicros: 40_000},
		intent(60_000, DemoMerchantBenchmark))
	if d.Outcome != AutoApprove {
		t.Errorf("$0.06 beat: %s (reason %+v), want AUTO_APPROVE — a second prompt here kills the take", d.Outcome, d.Reason)
	}
}

// The canonical demo policy must itself pass load-time validation.
func TestDemoPolicy_IsValid(t *testing.T) {
	if err := DemoPolicy().Validate(); err != nil {
		t.Fatalf("DemoPolicy failed its own validation: %v", err)
	}
}

// Step-2 follow-up B: an incomplete config is a STARTUP error, not a silent
// runtime deny-everything.
func TestValidate_RejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PolicyConfig)
		wantSub string
	}{
		{"zero job budget", func(c *PolicyConfig) { c.JobBudgetMicros = 0 }, "job_budget"},
		{"zero per-tx limit", func(c *PolicyConfig) { c.PerTxLimitMicros = 0 }, "per_tx_limit"},
		{"zero daily cap", func(c *PolicyConfig) { c.DailyCapMicros = 0 }, "daily_cap"},
		{"negative threshold", func(c *PolicyConfig) { c.ApprovalAboveMicros = -1 }, "approval_above"},
		{"empty allowlist", func(c *PolicyConfig) { c.MerchantAllowlist = nil }, "allowlist"},
		{
			"allowlisted merchant in a blocked category",
			func(c *PolicyConfig) {
				c.MerchantAllowlist = append(c.MerchantAllowlist, "api.casino.example")
				c.MerchantCategories["api.casino.example"] = "gambling"
			},
			"can never transact",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DemoPolicy()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}
