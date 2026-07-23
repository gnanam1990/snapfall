package policy

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The G6 fixture table, exactly as approved at Step 1 (cases 1-20) plus the
// zero-budget ruling (case 21). Config is demo-derived; every amount is 6dp micros.

func tableConfig() PolicyConfig {
	return PolicyConfig{
		JobBudgetMicros:     6_000_000,  // 6.00
		PerTxLimitMicros:    5_000_000,  // 5.00 hard deny
		DailyCapMicros:      10_000_000, // 10.00
		ApprovalAboveMicros: 100_000,    // 0.10 -> above escalates
		MerchantAllowlist: []string{
			"api.research-data.example",
			"api.benchmarks.example",
			"api.defi-signals.example", // allowlisted BUT token-trading (case 10)
			"api.casino-odds.example",  // allowlisted BUT gambling (case 11)
			"api.uncategorized.example",
		},
		MerchantCategories: map[string]string{
			"api.research-data.example": "business-data",
			"api.benchmarks.example":    "business-data",
			"api.defi-signals.example":  "token-trading",
			"api.casino-odds.example":   "gambling",
			// api.uncategorized.example deliberately unmapped (case 18)
		},
		BlockedCategories: []string{"token-trading", "gambling"}, // FR-POL-010 defaults
	}
}

func intent(amountMicros int64, merchant string) PaymentIntent {
	return PaymentIntent{
		IntentID:     "pi_test",
		OrgID:        "org_demo",
		JobID:        "job_104",
		TaskID:       "task_research_01",
		AgentID:      "due-diligence",
		Merchant:     merchant,
		Resource:     "GET /v1/data",
		AmountMicros: amountMicros,
		Purpose:      "test purchase",
		Nonce:        "0xabc",
	}
}

// fixtureCase is one row of the approved table.
type fixtureCase struct {
	name        string
	cfg         PolicyConfig
	state       SpendState
	intent      PaymentIntent
	wantOutcome Outcome
	// wantRule is the primary reason's rule ("" = expect Reason == nil).
	wantRule string
	wantCode string
	// wantLimit/wantActual pin the numbers in the reason (0 = don't check).
	wantLimit  int64
	wantActual int64
	// wantChecks pins how many rules were evaluated (short-circuit proof; 0 = don't check).
	wantChecks int
	// wantLastCheck pins which rule the pipeline stopped at ("" = don't check).
	wantLastCheck string
}

func fixtureTable() []fixtureCase {
	cfg := tableConfig()
	return []fixtureCase{
		// ── A. Demo-spine outcomes ──
		{
			name: "01_auto_approve_demo_purchase",
			cfg:  cfg, state: SpendState{},
			intent:      intent(40_000, "api.research-data.example"),
			wantOutcome: AutoApprove, wantRule: "",
			wantChecks: 5, wantLastCheck: RuleBlockedCategory, // all five rules ran and passed
		},
		{
			name: "02_human_approval_over_threshold",
			cfg:  cfg, state: SpendState{},
			intent:      intent(4_000_000, "api.research-data.example"),
			wantOutcome: HumanApprovalRequired,
			wantRule:    RuleApprovalThreshold, wantCode: CodeAboveApprovalThreshold,
			wantLimit: 100_000, wantActual: 4_000_000,
			wantChecks: 5, wantLastCheck: RuleBlockedCategory, // every deny rule passed first
		},

		// ── B. Boundaries: exactly-at approves, over-by-one-micro denies ──
		{
			name: "03_exactly_at_job_budget_approves",
			cfg:  cfg, state: SpendState{JobCommittedMicros: 5_960_000},
			intent:      intent(40_000, "api.research-data.example"), // 5.96 + 0.04 == 6.00
			wantOutcome: AutoApprove, wantRule: "",
		},
		{
			name: "04_one_micro_over_job_budget_denies",
			cfg:  cfg, state: SpendState{JobCommittedMicros: 5_960_000},
			intent:      intent(40_001, "api.research-data.example"), // 6.000001
			wantOutcome: Deny, wantRule: RuleJobBudget, wantCode: CodeJobBudgetExceeded,
			wantLimit: 6_000_000, wantActual: 6_000_001,
			wantChecks: 1, wantLastCheck: RuleJobBudget,
		},
		{
			name: "05_exactly_at_per_tx_limit_escalates",
			cfg:  cfg, state: SpendState{},
			intent:      intent(5_000_000, "api.research-data.example"),
			wantOutcome: HumanApprovalRequired, // passes per-tx inclusively, then escalates
			wantRule:    RuleApprovalThreshold, wantCode: CodeAboveApprovalThreshold,
			wantChecks: 5, wantLastCheck: RuleBlockedCategory,
		},
		{
			name: "06_one_micro_over_per_tx_denies",
			cfg:  cfg, state: SpendState{},
			intent:      intent(5_000_001, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RulePerTxLimit, wantCode: CodePerTxLimitExceeded,
			wantLimit: 5_000_000, wantActual: 5_000_001,
			wantChecks: 2, wantLastCheck: RulePerTxLimit,
		},
		{
			name: "07_exactly_at_daily_cap_approves",
			cfg:  cfg, state: SpendState{DailySpentMicros: 9_960_000},
			intent:      intent(40_000, "api.research-data.example"), // 9.96 + 0.04 == 10.00
			wantOutcome: AutoApprove, wantRule: "",
		},
		{
			name: "08_one_micro_over_daily_cap_denies",
			cfg:  cfg, state: SpendState{DailySpentMicros: 9_960_000},
			intent:      intent(40_001, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleDailyCap, wantCode: CodeDailyCapExceeded,
			wantLimit: 10_000_000, wantActual: 10_000_001,
			wantChecks: 3, wantLastCheck: RuleDailyCap,
		},

		// ── C. Remaining deny reasons ──
		{
			name: "09_unlisted_merchant_denies",
			cfg:  cfg, state: SpendState{},
			intent:      intent(40_000, "api.sketchy.example"),
			wantOutcome: Deny, wantRule: RuleMerchantAllowlist, wantCode: CodeMerchantNotAllowlisted,
			wantChecks: 4, wantLastCheck: RuleMerchantAllowlist,
		},
		{
			name: "10_blocked_category_token_trading",
			cfg:  cfg, state: SpendState{},
			intent:      intent(40_000, "api.defi-signals.example"), // allowlisted, still dies
			wantOutcome: Deny, wantRule: RuleBlockedCategory, wantCode: CodeCategoryBlocked,
			wantChecks: 5, wantLastCheck: RuleBlockedCategory,
		},
		{
			name: "11_blocked_category_gambling",
			cfg:  cfg, state: SpendState{},
			intent:      intent(40_000, "api.casino-odds.example"),
			wantOutcome: Deny, wantRule: RuleBlockedCategory, wantCode: CodeCategoryBlocked,
		},

		// ── D. Ordering pins: two violations, first-in-order wins ──
		{
			name: "12_ordering_budget_beats_blocked_category",
			cfg:  cfg, state: SpendState{JobCommittedMicros: 5_990_000},
			intent:      intent(40_000, "api.defi-signals.example"), // over budget AND token-trading
			wantOutcome: Deny, wantRule: RuleJobBudget, wantCode: CodeJobBudgetExceeded,
			wantChecks: 1, wantLastCheck: RuleJobBudget,
		},
		{
			name: "13_ordering_budget_beats_per_tx",
			cfg:  cfg, state: SpendState{JobCommittedMicros: 1_000_000},
			intent:      intent(5_500_000, "api.research-data.example"), // 6.50 > 6.00 budget AND > 5.00 per-tx
			wantOutcome: Deny, wantRule: RuleJobBudget, wantCode: CodeJobBudgetExceeded,
			wantChecks: 1, wantLastCheck: RuleJobBudget,
		},
		{
			name: "14_ordering_per_tx_beats_daily",
			cfg:  overrideBudget(cfg, 20_000_000), state: SpendState{DailySpentMicros: 9_000_000},
			intent:      intent(5_500_000, "api.research-data.example"), // over per-tx AND would blow daily
			wantOutcome: Deny, wantRule: RulePerTxLimit, wantCode: CodePerTxLimitExceeded,
			wantChecks: 2, wantLastCheck: RulePerTxLimit,
		},
		{
			name: "15_ordering_daily_beats_merchant",
			cfg:  cfg, state: SpendState{DailySpentMicros: 9_990_000},
			intent:      intent(40_000, "api.sketchy.example"), // over daily AND unlisted
			wantOutcome: Deny, wantRule: RuleDailyCap, wantCode: CodeDailyCapExceeded,
			wantChecks: 3, wantLastCheck: RuleDailyCap,
		},
		{
			name: "16_ordering_merchant_beats_blocked_category",
			cfg:  addCategory(cfg, "api.offshore-casino.example", "gambling"), state: SpendState{},
			intent:      intent(40_000, "api.offshore-casino.example"), // unlisted AND gambling
			wantOutcome: Deny, wantRule: RuleMerchantAllowlist, wantCode: CodeMerchantNotAllowlisted,
			wantChecks: 4, wantLastCheck: RuleMerchantAllowlist,
		},

		// ── E. Semantics + hygiene ──
		{
			name: "17a_zero_amount_denies",
			cfg:  cfg, state: SpendState{},
			intent:      intent(0, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleIntentValidation, wantCode: CodeInvalidAmount,
			wantChecks: 1, wantLastCheck: RuleIntentValidation,
		},
		{
			name: "17b_negative_amount_denies",
			cfg:  cfg, state: SpendState{},
			intent:      intent(-1, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleIntentValidation, wantCode: CodeInvalidAmount,
		},
		{
			name: "18_unknown_category_passes_category_check",
			cfg:  cfg, state: SpendState{},
			intent:      intent(40_000, "api.uncategorized.example"),
			wantOutcome: AutoApprove, wantRule: "",
		},

		// ── The Step-2 ruling: zero/unset job budget is DENY, not unlimited ──
		{
			name: "21_zero_job_budget_denies_not_unlimited",
			cfg:  overrideBudget(cfg, 0), state: SpendState{},
			intent:      intent(40_000, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleJobBudget, wantCode: CodeJobBudgetNotConfigured,
			wantChecks: 1, wantLastCheck: RuleJobBudget,
		},

		// ── Review batch (PR #2 bot findings, verified) ──
		{
			// Negative spend state is corrupt input, not a discount: fail closed.
			name: "22_negative_spend_state_denies",
			cfg:  cfg, state: SpendState{JobCommittedMicros: -1},
			intent:      intent(40_000, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleIntentValidation, wantCode: CodeInvalidSpendState,
			wantChecks: 1, wantLastCheck: RuleIntentValidation,
		},
		{
			// Near-MaxInt64 committed spend must DENY on the budget, not wrap negative
			// and sail through to auto-approval.
			name: "23_near_max_committed_denies_not_wraps",
			cfg:  cfg, state: SpendState{JobCommittedMicros: 9_223_372_036_854_775_000},
			intent:      intent(40_000, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleJobBudget, wantCode: CodeJobBudgetExceeded,
			wantChecks: 1, wantLastCheck: RuleJobBudget,
		},
		{
			// Same overflow posture on the daily cap.
			name: "23b_near_max_daily_denies_not_wraps",
			cfg:  cfg, state: SpendState{DailySpentMicros: 9_223_372_036_854_775_000},
			intent:      intent(40_000, "api.research-data.example"),
			wantOutcome: Deny, wantRule: RuleDailyCap, wantCode: CodeDailyCapExceeded,
			wantChecks: 3, wantLastCheck: RuleDailyCap,
		},
		{
			// An UNSET approval threshold escalates everything — unset never means
			// auto-approve (the same fail-closed ruling as the deny limits).
			name: "24_zero_threshold_escalates_everything",
			cfg:  overrideThreshold(cfg, 0), state: SpendState{},
			intent:      intent(10_000, "api.research-data.example"), // one cent
			wantOutcome: HumanApprovalRequired,
			wantRule:    RuleApprovalThreshold, wantCode: CodeApprovalNotConfigured,
			wantChecks: 5, wantLastCheck: RuleBlockedCategory,
		},
	}
}

func overrideThreshold(cfg PolicyConfig, v int64) PolicyConfig {
	cfg.ApprovalAboveMicros = v
	return cfg
}

func overrideBudget(cfg PolicyConfig, budget int64) PolicyConfig {
	cfg.JobBudgetMicros = budget
	return cfg
}

func addCategory(cfg PolicyConfig, merchant, category string) PolicyConfig {
	m := make(map[string]string, len(cfg.MerchantCategories)+1)
	for k, v := range cfg.MerchantCategories {
		m[k] = v
	}
	m[merchant] = category
	cfg.MerchantCategories = m
	return cfg
}

func TestEvaluate_FixtureTable(t *testing.T) {
	for _, tc := range fixtureTable() {
		t.Run(tc.name, func(t *testing.T) {
			d := Evaluate(tc.cfg, tc.state, tc.intent)

			if d.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %s, want %s (reason: %+v)", d.Outcome, tc.wantOutcome, d.Reason)
			}
			if tc.wantRule == "" {
				if d.Reason != nil {
					t.Fatalf("expected no reason, got %+v", d.Reason)
				}
			} else {
				if d.Reason == nil {
					t.Fatal("expected a reason, got nil")
				}
				if d.Reason.Rule != tc.wantRule {
					t.Errorf("reason.Rule = %s, want %s", d.Reason.Rule, tc.wantRule)
				}
				if tc.wantCode != "" && d.Reason.Code != tc.wantCode {
					t.Errorf("reason.Code = %s, want %s", d.Reason.Code, tc.wantCode)
				}
				if tc.wantLimit != 0 && d.Reason.LimitMicros != tc.wantLimit {
					t.Errorf("reason.LimitMicros = %d, want %d", d.Reason.LimitMicros, tc.wantLimit)
				}
				if tc.wantActual != 0 && d.Reason.ActualMicros != tc.wantActual {
					t.Errorf("reason.ActualMicros = %d, want %d", d.Reason.ActualMicros, tc.wantActual)
				}
				if d.Reason.Message == "" {
					t.Error("reason.Message must never be empty — the dashboard renders it verbatim")
				}
			}
			if tc.wantChecks != 0 && len(d.Checks) != tc.wantChecks {
				t.Errorf("evaluated %d rules, want %d (short-circuit ordering pin): %+v", len(d.Checks), tc.wantChecks, d.Checks)
			}
			if tc.wantLastCheck != "" {
				last := d.Checks[len(d.Checks)-1]
				if last.Rule != tc.wantLastCheck {
					t.Errorf("pipeline stopped at %s, want %s", last.Rule, tc.wantLastCheck)
				}
			}
		})
	}
}

// Case 19: purity — same inputs, identical decision; inputs unmutated.
func TestEvaluate_DeterministicAndPure(t *testing.T) {
	cfg := tableConfig()
	state := SpendState{JobCommittedMicros: 1_000_000, DailySpentMicros: 2_000_000}
	in := intent(4_000_000, "api.research-data.example")

	cfgBefore := tableConfig()
	stateBefore := state
	inBefore := in

	d1 := Evaluate(cfg, state, in)
	d2 := Evaluate(cfg, state, in)

	if !reflect.DeepEqual(d1, d2) {
		t.Errorf("same input, different decisions:\n%+v\n%+v", d1, d2)
	}
	if !reflect.DeepEqual(cfg, cfgBefore) || state != stateBefore || in != inBefore {
		t.Error("Evaluate mutated its inputs")
	}
}

// Case 20: the reason is machine-readable JSON with stable keys — this is the wire
// format the dashboard and Telegram render.
func TestReason_MarshalsToStableJSON(t *testing.T) {
	d := Evaluate(tableConfig(), SpendState{JobCommittedMicros: 5_960_000},
		intent(40_001, "api.research-data.example"))
	if d.Reason == nil {
		t.Fatal("expected a deny reason")
	}

	raw, err := json.Marshal(d.Reason)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"rule", "code", "limit_micros", "actual_micros", "message"} {
		if _, ok := m[key]; !ok {
			t.Errorf("reason JSON missing key %q: %s", key, raw)
		}
	}
}

// Step-2 addition A: the daily window definition is a CALLER contract. This pins it:
// the window is the UTC calendar day. Two instants in the same UTC day share a window;
// crossing UTC midnight resets it, regardless of local timezone.
// FormatUSDC must render every int64, including MinInt64 (whose negation overflows).
func TestFormatUSDC_Extremes(t *testing.T) {
	if got := FormatUSDC(-9223372036854775808); got != "-9223372036854.775808" {
		t.Errorf("FormatUSDC(MinInt64) = %q", got)
	}
	if got := FormatUSDC(-1); got != "-0.000001" {
		t.Errorf("FormatUSDC(-1) = %q", got)
	}
	if got := FormatUSDC(9223372036854775807); got != "9223372036854.775807" {
		t.Errorf("FormatUSDC(MaxInt64) = %q", got)
	}
}

func TestDailyWindowStartUTC(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)

	// 23:59:59 UTC and 00:00:01 UTC the next day are different windows.
	before := time.Date(2026, 7, 21, 23, 59, 59, 0, time.UTC)
	after := time.Date(2026, 7, 22, 0, 0, 1, 0, time.UTC)
	if DailyWindowStartUTC(before).Equal(DailyWindowStartUTC(after)) {
		t.Error("UTC midnight must reset the window")
	}

	// 03:30 IST on Jul 22 is 22:00 UTC on Jul 21 — same window as `before`,
	// even though the local calendar already turned over.
	istLate := time.Date(2026, 7, 22, 3, 30, 0, 0, ist)
	if !DailyWindowStartUTC(before).Equal(DailyWindowStartUTC(istLate)) {
		t.Error("the window is the UTC day, not the local day")
	}

	// The window start is exactly midnight UTC.
	start := DailyWindowStartUTC(after)
	if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Location() != time.UTC {
		t.Errorf("window start = %v, want midnight UTC", start)
	}
}
