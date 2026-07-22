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
)

// maxIntentMicros bounds a single intent at 100M USDC — far above anything legitimate,
// low enough that no sum in the pipeline can approach int64 overflow.
const maxIntentMicros int64 = 100_000_000_000_000

// PaymentIntent is the engine's input (v4 §8.3 shape; hashing/expiry live in G7).
type PaymentIntent struct {
	IntentID      string `json:"intent_id"`
	OrgID         string `json:"org_id"`
	JobID         string `json:"job_id"`
	TaskID        string `json:"task_id"`
	AgentID       string `json:"agent_id"`
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

// FormatUSDC renders micros as a decimal USDC string ("6.000001").
func FormatUSDC(micros int64) string {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000)
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
	if in.AmountMicros <= 0 || in.AmountMicros > maxIntentMicros {
		return fail(Reason{
			Rule: RuleIntentValidation, Code: CodeInvalidAmount,
			ActualMicros: in.AmountMicros,
			Message:      fmt.Sprintf("intent amount %s USDC is not a positive amount within bounds", FormatUSDC(in.AmountMicros)),
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
	wouldCommit := state.JobCommittedMicros + in.AmountMicros
	if wouldCommit > cfg.JobBudgetMicros {
		return fail(Reason{
			Rule: RuleJobBudget, Code: CodeJobBudgetExceeded,
			LimitMicros: cfg.JobBudgetMicros, ActualMicros: wouldCommit,
			Message: fmt.Sprintf("job budget exceeded: this purchase would commit %s of the %s USDC operating budget",
				FormatUSDC(wouldCommit), FormatUSDC(cfg.JobBudgetMicros)),
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
	wouldDaily := state.DailySpentMicros + in.AmountMicros
	if wouldDaily > cfg.DailyCapMicros {
		return fail(Reason{
			Rule: RuleDailyCap, Code: CodeDailyCapExceeded,
			LimitMicros: cfg.DailyCapMicros, ActualMicros: wouldDaily,
			Message: fmt.Sprintf("daily cap exceeded: this purchase would bring today's spend to %s of the %s USDC cap (UTC day)",
				FormatUSDC(wouldDaily), FormatUSDC(cfg.DailyCapMicros)),
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
	//      deniable intent never reaches this line. ──
	if cfg.ApprovalAboveMicros > 0 && in.AmountMicros > cfg.ApprovalAboveMicros {
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
