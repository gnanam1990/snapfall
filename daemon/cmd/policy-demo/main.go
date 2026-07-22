// Command policy-demo exercises the G6 policy engine with realistic PaymentIntents
// and prints the actual decisions + structured reasons — the strings the dashboard
// and Telegram render verbatim (Step-2 manual verification).
//
//	go run ./cmd/policy-demo
package main

import (
	"encoding/json"
	"fmt"

	"github.com/gnanam1990/snapfall/daemon/internal/policy"
)

func main() {
	// The demo job's policy: 6.00 job budget, 5.00 per-tx hard limit, 10.00 daily,
	// 0.10 auto-approval threshold, blocked categories per FR-POL-010 defaults.
	cfg := policy.DemoPolicy()
	// Extend the canonical demo policy with the fixture-only blocked-category merchant,
	// so scenario 4/5 can demonstrate FR-POL-010 without polluting DemoPolicy itself.
	cfg.MerchantAllowlist = append(cfg.MerchantAllowlist, "api.defi-signals.example")
	cfg.MerchantCategories["api.defi-signals.example"] = "token-trading"

	type scenario struct {
		title  string
		state  policy.SpendState
		intent policy.PaymentIntent
	}

	base := policy.PaymentIntent{
		IntentID: "pi_demo", OrgID: "org_demo", JobID: "job_104",
		TaskID: "task_research_01", AgentID: "due-diligence",
		Resource: "GET /v1/data", Nonce: "0xf2c1", PolicyVersion: "pol_7",
	}
	mk := func(amount int64, merchant, purpose string) policy.PaymentIntent {
		in := base
		in.AmountMicros = amount
		in.Merchant = merchant
		in.Purpose = purpose
		return in
	}

	scenarios := []scenario{
		{
			title:  "1. The 0.04 company profile (demo auto-approve beat, AT-02)",
			state:  policy.SpendState{},
			intent: mk(40_000, policy.DemoMerchantProfile, "competitor company profile"),
		},
		{
			title:  "2. The 4.00 premium dataset (demo escalation beat, AT-03) — THE ON-SCREEN REASON",
			state:  policy.SpendState{JobCommittedMicros: 40_000, DailySpentMicros: 40_000},
			intent: mk(4_000_000, policy.DemoMerchantProfile, "premium market dataset"),
		},
		{
			title:  "3. The 0.06 benchmark alternative after rejection (AT-04)",
			state:  policy.SpendState{JobCommittedMicros: 40_000, DailySpentMicros: 40_000},
			intent: mk(60_000, policy.DemoMerchantBenchmark, "benchmark summary (cheaper source)"),
		},
		{
			title:  "4. Token-trading merchant, well within every budget (FR-POL-010)",
			state:  policy.SpendState{},
			intent: mk(40_000, "api.defi-signals.example", "defi signal feed"),
		},
		{
			title:  "5. Over budget AND blocked category — ordering: budget reason wins",
			state:  policy.SpendState{JobCommittedMicros: 5_990_000},
			intent: mk(40_000, "api.defi-signals.example", "double violation"),
		},
		{
			title:  "6. Unknown merchant (deny-by-default)",
			state:  policy.SpendState{},
			intent: mk(40_000, "api.sketchy.example", "mystery data"),
		},
	}

	for _, s := range scenarios {
		fmt.Println("════════════════════════════════════════════════════════════════")
		fmt.Println(s.title)
		fmt.Printf("   intent: %s USDC → %s  (job committed %s, daily %s)\n",
			policy.FormatUSDC(s.intent.AmountMicros), s.intent.Merchant,
			policy.FormatUSDC(s.state.JobCommittedMicros), policy.FormatUSDC(s.state.DailySpentMicros))

		d := policy.Evaluate(cfg, s.state, s.intent)

		fmt.Printf("   OUTCOME: %s\n", d.Outcome)
		if d.Reason != nil {
			raw, _ := json.MarshalIndent(d.Reason, "   ", "  ")
			fmt.Printf("   reason (verbatim wire JSON):\n   %s\n", raw)
		}
		trace, _ := json.Marshal(d.Checks)
		fmt.Printf("   evaluation trace: %s\n\n", trace)
	}
}
