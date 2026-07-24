// Package policy is the G6 deterministic payment-policy engine (PRD §2, FR-PAY-003/004,
// FR-POL-010).
//
// Evaluate is a PURE function: PaymentIntent in, Decision out. No LLM anywhere in the
// decision path, no I/O, no clock. The caller supplies spend state; expiry and nonce
// replay are enforced by the G7 approval lifecycle, not here (Step-1 ruling #1).
//
// Rule pipeline, FIXED order — pinned by the ordering cases in policy_test.go, not by
// this comment:
//
//  0. intent validation (a malformed amount never reaches a budget comparison)
//  1. job budget         (committed + amount vs the job's operating budget)
//  2. per-transaction limit
//  3. daily cumulative cap
//  4. merchant allowlist  (deny-by-default: unlisted merchant = deny)
//  5. blocked categories  (token-trading, gambling by default — FR-POL-010)
//  6. approval threshold  (Auto vs Human — a deniable intent is denied, never escalated)
//
// All amounts are int64 micro-USDC (6dp). No floats touch money.
package policy

import (
	"fmt"
	"time"
)

// Outcome is the engine's verdict (FR-PAY-004).
type Outcome string

const (
	AutoApprove           Outcome = "AUTO_APPROVE"
	HumanApprovalRequired Outcome = "HUMAN_APPROVAL_REQUIRED"
	Deny                  Outcome = "DENY"
)

// Rule names — the machine keys of the pipeline stages.
const (
	RuleIntentValidation  = "intent-validation"
	RuleJobBudget         = "job-budget"
	RulePerTxLimit        = "per-tx-limit"
	RuleDailyCap          = "daily-cap"
	RuleMerchantAllowlist = "merchant-allowlist"
	RuleBlockedCategory   = "blocked-category"
	RuleApprovalThreshold = "approval-threshold"
)

// Reason codes — one per distinct failure, stable for dashboards and tests.
const (
	CodeInvalidAmount          = "invalid-amount"
	CodeJobBudgetNotConfigured = "job-budget-not-configured"
	CodeJobBudgetExceeded      = "job-budget-exceeded"
	CodePerTxNotConfigured     = "per-tx-limit-not-configured"
	CodePerTxLimitExceeded     = "per-tx-limit-exceeded"
	CodeDailyCapNotConfigured  = "daily-cap-not-configured"
	CodeDailyCapExceeded       = "daily-cap-exceeded"
	CodeMerchantNotAllowlisted = "merchant-not-allowlisted"
	CodeCategoryBlocked        = "category-blocked"
	CodeAboveApprovalThreshold = "amount-above-approval-threshold"
	CodeInvalidSpendState      = "invalid-spend-state"
	CodeUnmodelledKind         = "unmodelled-intent-kind"
	CodeApprovalNotConfigured  = "approval-threshold-not-configured"
)

// maxIntentMicros bounds a single intent at 100M USDC — far above anything legitimate,
// low enough that no sum in the pipeline can approach int64 overflow.
const maxIntentMicros int64 = 100_000_000_000_000

// Intent kinds. Evaluate models exactly ONE kind — outbound agent spend (KindPayment).
// The advance (money INTO the treasury) is human-authorized only: it skips Evaluate and
// enters the approval lifecycle already marked HumanApprovalRequired; the settlement is
// customer-authorized and never sees policy at all. Any non-payment kind reaching
// Evaluate is therefore a ROUTING BUG, and rule 0 denies it by law — a fixed pipeline
// handing an unmodelled kind whatever falls out of its rules is where a fail-open hides.
const (
	KindPayment = "payment" // "" means KindPayment: every existing caller predates the field
	KindAdvance = "advance"
)

// PaymentIntent is the engine's input (v4 §8.3 shape; hashing/expiry live in G7).
type PaymentIntent struct {
	IntentID string `json:"intent_id"`
	OrgID    string `json:"org_id"`
	JobID    string `json:"job_id"`
	TaskID   string `json:"task_id"`
	AgentID  string `json:"agent_id"`
	// Kind is the intent's action class. Empty = KindPayment (back-compat, explicit
	// decision). Evaluate refuses every other value at rule 0 (CodeUnmodelledKind).
	Kind          string `json:"kind,omitempty"`
	Merchant      string `json:"merchant"`
	Resource      string `json:"resource"`
	AmountMicros  int64  `json:"amount_micros"`
	Purpose       string `json:"purpose"`
	Nonce         string `json:"nonce"`
	PolicyVersion string `json:"policy_version"`
}

// PolicyConfig is the active policy. A zero limit on any deny rule means NOT
// CONFIGURED and denies — "unset means unlimited" is how a money bug ships
// (Step-2 ruling B; case 21 pins the job-budget instance).
type PolicyConfig struct {
	JobBudgetMicros     int64
	PerTxLimitMicros    int64
	DailyCapMicros      int64
	ApprovalAboveMicros int64
	MerchantAllowlist   []string
	MerchantCategories  map[string]string
	BlockedCategories   []string
}

// SpendState is the caller-supplied cumulative state.
//
// CALLER CONTRACT for DailySpentMicros: the "day" is the UTC CALENDAR DAY — the sum of
// settled+reserved spend since DailyWindowStartUTC(now). The engine does not define or
// verify the window; it compares against whatever the caller computed. Callers MUST use
// DailyWindowStartUTC so every component agrees on the boundary. (Step-2 addition A.)
type SpendState struct {
	// JobCommittedMicros is reserved + settled spend against this job so far.
	JobCommittedMicros int64
	// DailySpentMicros is org-wide spend inside the current UTC-day window.
	DailySpentMicros int64
}

// Validate rejects an incomplete or self-contradictory policy AT LOAD TIME, so a
// misconfigured daemon fails at startup instead of silently denying every payment at
// the first intent (Step-2 follow-up B). Evaluate keeps its own per-call guards as
// defense in depth; this is the early, loud version of the same rule.
//
// TRACKED (review batch, architect ruling): production policy-config LOADING does not
// exist yet, so Validate currently has no production call site — the fail-closed-at-
// startup property is UNREALISED, not shipped; the runtime guards are what actually
// operate today. Whoever writes the policy-config loader MUST call Validate() at load
// and refuse to boot on error. The same note sits in internal/config's package doc so
// the loader's author finds it.
func (c PolicyConfig) Validate() error {
	if c.JobBudgetMicros <= 0 {
		return fmt.Errorf("policy config: job_budget must be positive (unset does not mean unlimited)")
	}
	if c.PerTxLimitMicros <= 0 {
		return fmt.Errorf("policy config: per_tx_limit must be positive (unset does not mean unlimited)")
	}
	if c.DailyCapMicros <= 0 {
		return fmt.Errorf("policy config: daily_cap must be positive (unset does not mean unlimited)")
	}
	if c.ApprovalAboveMicros < 0 {
		return fmt.Errorf("policy config: approval_above must not be negative")
	}
	if len(c.MerchantAllowlist) == 0 {
		return fmt.Errorf("policy config: merchant allowlist is empty; every payment would be denied")
	}
	// A blocked category that no merchant maps to is legal (blocklists name the bad, not
	// the known); an allowlisted merchant mapped to a blocked category is a contradiction
	// worth failing on — it can never transact.
	blocked := make(map[string]bool, len(c.BlockedCategories))
	for _, b := range c.BlockedCategories {
		blocked[b] = true
	}
	for _, m := range c.MerchantAllowlist {
		if cat, ok := c.MerchantCategories[m]; ok && blocked[cat] {
			return fmt.Errorf("policy config: merchant %q is allowlisted but categorized %q, which is blocked — it can never transact", m, cat)
		}
	}
	return nil
}

// DailyWindowStartUTC pins the daily-cumulative window definition: the window
// containing `now` starts at midnight UTC of now's UTC date. Timezone-independent —
// 03:30 IST and 22:00 UTC the previous evening share a window.
func DailyWindowStartUTC(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Check records one evaluated pipeline stage, in order.
type Check struct {
	Rule   string `json:"rule"`
	Result string `json:"result"` // PASS | FAIL
}

// Reason is the structured, machine-readable explanation (FR-PAY-004). The dashboard
// and Telegram render Message verbatim; everything else is for machines.
type Reason struct {
	Rule         string `json:"rule"`
	Code         string `json:"code"`
	LimitMicros  int64  `json:"limit_micros"`
	ActualMicros int64  `json:"actual_micros"`
	Merchant     string `json:"merchant,omitempty"`
	Category     string `json:"category,omitempty"`
	Message      string `json:"message"`
}

// Decision is the engine's full output: the verdict, the first tripped rule (nil on
// AutoApprove), and the ordered evaluation trace.
type Decision struct {
	Outcome Outcome `json:"outcome"`
	Reason  *Reason `json:"reason,omitempty"`
	Checks  []Check `json:"checks"`
}

// FormatUSDC renders micros as a decimal USDC string ("6.000001"). Handles the full
// int64 range: MinInt64's magnitude does not fit int64, so the negation goes through
// uint64 (review-batch fix).
func FormatUSDC(micros int64) string {
	sign := ""
	mag := uint64(micros)
	if micros < 0 {
		sign = "-"
		mag = uint64(-(micros + 1)) + 1 // |MinInt64| without overflowing
	}
	return fmt.Sprintf("%s%d.%06d", sign, mag/1_000_000, mag%1_000_000)
}

// satAdd is a saturating add for REPORTING only (never for the comparison itself).
func satAdd(a, b int64) int64 {
	if a > 0 && b > 9223372036854775807-a {
		return 9223372036854775807
	}
	return a + b
}

// Evaluate runs the pipeline. Pure; safe for concurrent use.
func Evaluate(cfg PolicyConfig, state SpendState, in PaymentIntent) Decision {
	var checks []Check

	fail := func(r Reason) Decision {
		checks = append(checks, Check{Rule: r.Rule, Result: "FAIL"})
		reason := r
		return Decision{Outcome: Deny, Reason: &reason, Checks: checks}
	}
	pass := func(rule string) {
		checks = append(checks, Check{Rule: rule, Result: "PASS"})
	}

	// ── 0. Intent validation — malformed money never reaches a budget comparison. ──
	// The kind guard runs FIRST: an unmodelled kind fails here, by law, before any
	// budget or allowlist rule can pass or fail incidentally.
	if in.Kind != "" && in.Kind != KindPayment {
		return fail(Reason{
			Rule: RuleIntentValidation, Code: CodeUnmodelledKind,
			Message: fmt.Sprintf("intent kind %q is not modelled by this pipeline: Evaluate authorizes outbound agent spend only (advances are human-authorized via the lifecycle; settlements are customer-authorized)", in.Kind),
		})
	}
	if in.AmountMicros <= 0 || in.AmountMicros > maxIntentMicros {
		return fail(Reason{
			Rule: RuleIntentValidation, Code: CodeInvalidAmount,
			ActualMicros: in.AmountMicros,
			Message:      fmt.Sprintf("intent amount %s USDC is not a positive amount within bounds", FormatUSDC(in.AmountMicros)),
		})
	}
	// Negative spend state is corrupt input, not a discount (review-batch fix):
	// a negative committed/daily figure would inflate remaining headroom.
	if state.JobCommittedMicros < 0 || state.DailySpentMicros < 0 {
		return fail(Reason{
			Rule: RuleIntentValidation, Code: CodeInvalidSpendState,
			Message: fmt.Sprintf("spend state is negative (job %s, daily %s USDC); refusing to evaluate against corrupt state",
				FormatUSDC(state.JobCommittedMicros), FormatUSDC(state.DailySpentMicros)),
		})
	}

	// ── 1. Job budget. ──
	if cfg.JobBudgetMicros <= 0 {
		return fail(Reason{
			Rule: RuleJobBudget, Code: CodeJobBudgetNotConfigured,
			ActualMicros: in.AmountMicros,
			Message:      "job has no operating budget configured; unset does not mean unlimited",
		})
	}
	// Overflow-safe: compare amount against remaining headroom instead of adding
	// (near-MaxInt64 state must DENY, never wrap negative — review-batch fix).
	if in.AmountMicros > cfg.JobBudgetMicros-state.JobCommittedMicros {
		return fail(Reason{
			Rule: RuleJobBudget, Code: CodeJobBudgetExceeded,
			LimitMicros: cfg.JobBudgetMicros, ActualMicros: satAdd(state.JobCommittedMicros, in.AmountMicros),
			Message: fmt.Sprintf("job budget exceeded: this purchase would commit %s of the %s USDC operating budget",
				FormatUSDC(satAdd(state.JobCommittedMicros, in.AmountMicros)), FormatUSDC(cfg.JobBudgetMicros)),
		})
	}
	pass(RuleJobBudget)

	// ── 2. Per-transaction limit. ──
	if cfg.PerTxLimitMicros <= 0 {
		return fail(Reason{
			Rule: RulePerTxLimit, Code: CodePerTxNotConfigured,
			ActualMicros: in.AmountMicros,
			Message:      "no per-transaction limit configured; unset does not mean unlimited",
		})
	}
	if in.AmountMicros > cfg.PerTxLimitMicros {
		return fail(Reason{
			Rule: RulePerTxLimit, Code: CodePerTxLimitExceeded,
			LimitMicros: cfg.PerTxLimitMicros, ActualMicros: in.AmountMicros,
			Message: fmt.Sprintf("amount %s USDC exceeds the %s USDC per-transaction limit",
				FormatUSDC(in.AmountMicros), FormatUSDC(cfg.PerTxLimitMicros)),
		})
	}
	pass(RulePerTxLimit)

	// ── 3. Daily cumulative cap (window: see SpendState contract). ──
	if cfg.DailyCapMicros <= 0 {
		return fail(Reason{
			Rule: RuleDailyCap, Code: CodeDailyCapNotConfigured,
			ActualMicros: in.AmountMicros,
			Message:      "no daily spend cap configured; unset does not mean unlimited",
		})
	}
	if in.AmountMicros > cfg.DailyCapMicros-state.DailySpentMicros {
		return fail(Reason{
			Rule: RuleDailyCap, Code: CodeDailyCapExceeded,
			LimitMicros: cfg.DailyCapMicros, ActualMicros: satAdd(state.DailySpentMicros, in.AmountMicros),
			Message: fmt.Sprintf("daily cap exceeded: this purchase would bring today's spend to %s of the %s USDC cap (UTC day)",
				FormatUSDC(satAdd(state.DailySpentMicros, in.AmountMicros)), FormatUSDC(cfg.DailyCapMicros)),
		})
	}
	pass(RuleDailyCap)

	// ── 4. Merchant allowlist — deny by default (SEC-007 posture). ──
	allowlisted := false
	for _, m := range cfg.MerchantAllowlist {
		if m == in.Merchant {
			allowlisted = true
			break
		}
	}
	if !allowlisted {
		return fail(Reason{
			Rule: RuleMerchantAllowlist, Code: CodeMerchantNotAllowlisted,
			Merchant: in.Merchant, ActualMicros: in.AmountMicros,
			Message: fmt.Sprintf("merchant %q is not on the allowlist; unknown merchants are denied", in.Merchant),
		})
	}
	pass(RuleMerchantAllowlist)

	// ── 5. Blocked categories (FR-POL-010). An unmapped merchant passes: the
	//      allowlist already vouched for it; this rule blocks known-bad, it does
	//      not require known-good (case 18). ──
	if cat, mapped := cfg.MerchantCategories[in.Merchant]; mapped {
		for _, blocked := range cfg.BlockedCategories {
			if cat == blocked {
				return fail(Reason{
					Rule: RuleBlockedCategory, Code: CodeCategoryBlocked,
					Merchant: in.Merchant, Category: cat, ActualMicros: in.AmountMicros,
					Message: fmt.Sprintf("merchant %q is categorized %q, which is a blocked spend category", in.Merchant, cat),
				})
			}
		}
	}
	pass(RuleBlockedCategory)

	// ── 6. Approval threshold: Auto vs Human. Every deny rule has passed; a
	//      deniable intent never reaches this line. An UNSET threshold escalates
	//      EVERYTHING — unset never means auto-approve, the same fail-closed ruling
	//      as the deny limits (review-batch fix). ──
	if cfg.ApprovalAboveMicros <= 0 {
		return Decision{
			Outcome: HumanApprovalRequired,
			Reason: &Reason{
				Rule: RuleApprovalThreshold, Code: CodeApprovalNotConfigured,
				ActualMicros: in.AmountMicros,
				Message:      "no auto-approval threshold configured; unset does not mean auto-approve — owner approval required",
			},
			Checks: checks,
		}
	}
	if in.AmountMicros > cfg.ApprovalAboveMicros {
		return Decision{
			Outcome: HumanApprovalRequired,
			Reason: &Reason{
				Rule: RuleApprovalThreshold, Code: CodeAboveApprovalThreshold,
				LimitMicros: cfg.ApprovalAboveMicros, ActualMicros: in.AmountMicros,
				Message: fmt.Sprintf("amount %s USDC is above the %s USDC auto-approval threshold; owner approval required",
					FormatUSDC(in.AmountMicros), FormatUSDC(cfg.ApprovalAboveMicros)),
			},
			Checks: checks,
		}
	}

	return Decision{Outcome: AutoApprove, Checks: checks}
}
