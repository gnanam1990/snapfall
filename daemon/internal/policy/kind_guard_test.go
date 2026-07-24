package policy

import (
	"strings"
	"testing"
)

// wouldAutoApprove is an intent that passes EVERY rule as a payment: allowlisted
// merchant, tiny amount, empty spend state, under the approval threshold. The kind
// guard must be the ONLY thing standing between it and AutoApprove — that is what
// "denied on rule 0 specifically, not incidentally" means.
func wouldAutoApprove() PaymentIntent {
	return PaymentIntent{
		IntentID: "pi_kind", OrgID: "org_demo", JobID: "job_kind", AgentID: "due-diligence",
		Merchant: DemoMerchantProfile, Resource: "GET /v1/company-profile",
		AmountMicros: 40_000, Purpose: "kind-guard fixture", Nonce: "0x" + strings.Repeat("ab", 32),
	}
}

// Rule 0, by law not accident: an advance-kind intent reaching Evaluate is DENIED with
// CodeUnmodelledKind as the FIRST check — before budget, allowlist, or any other rule
// can pass or fail. Evaluate models outbound agent spend; the advance is
// human-authorized and enters the lifecycle pre-marked, skipping Evaluate entirely —
// so an advance-kind intent here is a routing bug, and a fixed pipeline handing an
// unmodelled kind whatever falls out of its rules is exactly where a fail-open hides.
func TestEvaluate_AdvanceKindDeniedOnRuleZeroSpecifically(t *testing.T) {
	cfg, state := DemoPolicy(), SpendState{}

	// Sanity: as a payment, the same intent auto-approves — every other rule passes.
	base := wouldAutoApprove()
	if d := Evaluate(cfg, state, base); d.Outcome != AutoApprove {
		t.Fatalf("fixture must auto-approve as a payment, got %v (%+v)", d.Outcome, d.Reason)
	}

	for _, kind := range []string{KindAdvance, "settlement", "coffee"} {
		in := wouldAutoApprove()
		in.Kind = kind
		d := Evaluate(cfg, state, in)
		if d.Outcome != Deny {
			t.Fatalf("kind %q: outcome %v, want Deny", kind, d.Outcome)
		}
		if d.Reason == nil || d.Reason.Code != CodeUnmodelledKind {
			t.Fatalf("kind %q: reason %+v, want %s — the denial must name the kind guard, not budget or allowlist", kind, d.Reason, CodeUnmodelledKind)
		}
		// Rule 0 SPECIFICALLY: the kind guard is the first and only recorded check —
		// no later rule ever ran, so the denial cannot be incidental.
		if len(d.Checks) != 1 || d.Checks[0].Result != "FAIL" {
			t.Fatalf("kind %q: checks %+v — the kind guard must fail FIRST, before any other rule runs", kind, d.Checks)
		}
	}
}

// Back-compat is explicit: an empty kind IS a payment (every existing caller), and the
// explicit KindPayment behaves identically.
func TestEvaluate_EmptyAndPaymentKindIdentical(t *testing.T) {
	cfg, state := DemoPolicy(), SpendState{}
	empty := wouldAutoApprove()
	explicit := wouldAutoApprove()
	explicit.Kind = KindPayment
	de, dx := Evaluate(cfg, state, empty), Evaluate(cfg, state, explicit)
	if de.Outcome != dx.Outcome || de.Outcome != AutoApprove {
		t.Fatalf("empty=%v explicit=%v, want both AutoApprove", de.Outcome, dx.Outcome)
	}
}
