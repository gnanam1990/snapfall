// The canonical demo policy configuration — ONE source of truth for the recording-week
// policy, so the demo beats cannot drift apart from the engine silently.
package policy

// Demo merchant identifiers. CROSS-STREAM DEPENDENCY (Vasanth, V2/V12): these MUST match
// the merchant identity his seller/seed presents at pay time — today the three resources
// on the paid demo API. If his seed script renames a host or switches merchant identity
// to the x402 payTo address, this file is the one place to update, and
// TestDemoPolicy_AllowlistsEveryDemoMerchant fails until it happens — a failing test
// instead of a failing take.
const (
	// DemoMerchantProfile serves the $0.04 company profile (auto-approve beat, AT-02).
	DemoMerchantProfile = "api.research-data.example"
	// DemoMerchantPremium serves the $4.00 premium dataset (escalation beat, AT-03).
	DemoMerchantPremium = "api.research-data.example"
	// DemoMerchantBenchmark serves the $0.06 benchmark summary (the adaptation, AT-04).
	// It MUST be allowlisted ahead of time: the demo depends on this purchase
	// auto-approving right after the rejection beat, with no second prompt.
	DemoMerchantBenchmark = "api.benchmarks.example"
)

// DemoPolicy returns the demo's policy configuration (PRD §12 beats, v4 §15.2 numbers).
// cmd/policy-demo and the seed script consume THIS, never a private copy.
func DemoPolicy() PolicyConfig {
	return PolicyConfig{
		JobBudgetMicros:     6_000_000,  // 6.00 operating budget
		PerTxLimitMicros:    5_000_000,  // 5.00 hard per-tx deny
		DailyCapMicros:      10_000_000, // 10.00 daily cap
		ApprovalAboveMicros: 100_000,    // 0.10 auto-approval threshold
		MerchantAllowlist: []string{
			DemoMerchantProfile,
			DemoMerchantBenchmark,
		},
		MerchantCategories: map[string]string{
			DemoMerchantProfile:   "business-data",
			DemoMerchantBenchmark: "business-data",
		},
		BlockedCategories: []string{"token-trading", "gambling"}, // FR-POL-010 defaults
	}
}
