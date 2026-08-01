package approval

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// AT-15, verbatim (docs/SRS-v4-annex.md:793):
//
//	AT-15 | Unauthorized advance | Agent-originated/non-treasury advance rejected at
//	        API (FR-FLT-001) and contract (SC-FP-004).
//
// AT-15 names two layers and the daemon owns the API one. Part of the API layer is already
// covered at the MANIFEST boundary: a manifest declaring can_request_advance: true fails
// validation fatally with an agent-may-borrow finding (internal/agents/manifest_test.go:125),
// so an agent that ASKS for borrowing capacity never activates. What a manifest test cannot
// cover is intake itself. Manifests are a configuration gate read once at load; they say
// nothing about what the Lifecycle accepts when a caller inside the daemon names the wrong
// method. This file is that half: the intake API.
//
// It asserts BOTH directions of the wrong-door check, because "advances are human-authorized
// only" (FR-FLT-001, SC-FP-002) is only true when neither door accepts the other's traffic:
//
//	(a) Submit, the PAYMENT door, must DENY an advance-kind intent at policy rule 0
//	    (policy.CodeUnmodelledKind). Submit is the door that AUTO-APPROVES below the 0.10
//	    threshold, so without rule 0 an agent-originated advance shaped small enough would be
//	    authorized with no human in the loop at all. That is FR-FLT-001's violation exactly.
//
//	(b) SubmitAdvance, the human-only door, must refuse a payment-kind intent with
//	    ErrWrongKind. That door deliberately SKIPS policy.Evaluate and enters PENDING
//	    unconditionally, so a payment routed through it would face no job budget, no
//	    per-transaction limit, no merchant allowlist and no daily cap. It is the wider hole
//	    of the two, and it is the one nothing tested before this file.
//
// One direction alone is not the test. A door that refuses foreign traffic while its
// neighbour waves it through is still one open door.

// at15Fixture is newFixtureOn over a fresh temp store, handing the store back as well:
// half of every assertion here is about what did NOT reach the durable log, and that needs
// the handle. Closing is registered here because newFixtureOn does not own the store.
func at15Fixture(t *testing.T) (*fixture, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "at15_advance.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return newFixtureOn(t, st), st
}

// at15CountEvents counts durable events of one kind: the audit half of each assertion.
func at15CountEvents(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("counting %s events: %v", kind, err)
	}
	return n
}

// at15CountNonceClaims counts payment_intents rows carrying this nonce. That row IS the
// durable intake claim (the UNIQUE constraint is the replay authority), so counting it is
// how "the nonce was burned" and "the nonce was not burned" become assertions rather than
// readings of the code.
func at15CountNonceClaims(t *testing.T, st *store.Store, nonce string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM payment_intents WHERE nonce = ?`, nonce).Scan(&n); err != nil {
		t.Fatalf("counting nonce claims for %s: %v", nonce, err)
	}
	return n
}

// at15RelabelledPaymentIntent is the fixture's own 4.00 escalation intent, the one AT-03
// drives, with exactly ONE field changed: Kind now says advance. Everything else is
// untouched, so if the payment door ever admits it, the only thing that changed is the
// kind check. Direction (b) below submits the same fixture intent WITHOUT the relabelling
// and it escalates cleanly, which is what makes this vector's DENY attributable to Kind and
// to nothing else.
func at15RelabelledPaymentIntent(f *fixture) Intent {
	return f.demoIntent(func(i *Intent) {
		i.IntentID = "pi_at15_relabelled"
		i.Kind = policy.KindAdvance
		i.Nonce = "0x" + strings.Repeat("15", 32)
	})
}

// at15AdvanceIntent is the intent advancing.Flow.Propose actually builds (advancing.go:88),
// with one field changed: AgentID names a worker agent instead of "funding". Kind, ChainRef
// (the bytes32 vault job id), the FloatPool call as the resource, and principal == max are
// all the real shape, and the amount is AT-11's 12.50. So what is under test is the DOOR,
// not a malformed struct that any validator would have caught.
func at15AdvanceIntent(f *fixture) Intent {
	return Intent{
		IntentID:        "adv_at15",
		OrgID:           "org_demo",
		JobID:           "job_104",
		AgentID:         "due-diligence",
		Kind:            policy.KindAdvance,
		ChainRef:        "0x" + strings.Repeat("7f", 32),
		Resource:        "FloatPool.requestAdvance",
		Purpose:         "working-capital advance: 50% of the 25.00 USDC escrowed receivable",
		AmountMicros:    12_500_000,
		MaxAmountMicros: 12_500_000,
		Nonce:           "0x" + strings.Repeat("a1", 32),
		ExpiresAt:       f.clock.Now().Add(5 * time.Minute),
	}
}

// TestAT15_AgentOriginatedAdvanceIsRejectedAtIntake is the API half of AT-15
// (FR-FLT-001, SC-FP-002, docs/SRS-v4-annex.md:793), asserting both directions of the
// wrong-door check described in this file's header comment.
func TestAT15_AgentOriginatedAdvanceIsRejectedAtIntake(t *testing.T) {
	// ─────────────────────────────────────────────────────────────────────
	// (a) The PAYMENT door denies an advance kind, at rule 0, by law
	// ─────────────────────────────────────────────────────────────────────
	t.Run("payment door denies an advance kind", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			build func(f *fixture) Intent
		}{
			{
				// The sharpest vector: an intent that is otherwise PERFECTLY approvable
				// (allowlisted merchant, inside every limit, escalating cleanly on its own
				// door as direction (b) proves below) whose ONLY defect is the kind. If
				// rule 0 ever stopped firing, this one would not merely slip through, it
				// would land in the owner inbox dressed as a payment.
				name:  "otherwise-approvable payment relabelled as an advance",
				build: at15RelabelledPaymentIntent,
			},
			{
				// The realistic vector: the advance flow's own intent shape, pushed through
				// the payment door by a caller that named the wrong method.
				name:  "agent-originated advance-shaped intent",
				build: at15AdvanceIntent,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f, st := at15Fixture(t)
				ctx := context.Background()
				in := tc.build(f)

				res, err := f.l.Submit(ctx, in)
				if !errors.Is(err, ErrDenied) {
					t.Fatalf("Submit of an advance-kind intent: %v, want ErrDenied", err)
				}
				if res.Request != nil {
					t.Fatalf("a denied advance opened an approval request: %+v", res.Request)
				}
				if res.Decision.Outcome != policy.Deny {
					t.Fatalf("outcome %s, want DENY", res.Decision.Outcome)
				}
				if res.Decision.Reason == nil {
					t.Fatal("denied with no structured reason: FR-PAY-004 requires the refusal be machine-readable")
				}
				if res.Decision.Reason.Rule != policy.RuleIntentValidation ||
					res.Decision.Reason.Code != policy.CodeUnmodelledKind {
					t.Fatalf("deny reason %+v, want rule %s / code %s: the refusal must come from the KIND, not incidentally from a limit or the allowlist",
						res.Decision.Reason, policy.RuleIntentValidation, policy.CodeUnmodelledKind)
				}

				// Rule 0 fires BEFORE any other rule can pass or fail incidentally (the
				// kind guard is the first statement in Evaluate). Exactly one check, and it
				// FAILED: no budget rule, no per-transaction limit and no allowlist entry
				// contributed to this verdict, so retuning any of them cannot change it.
				// Without this assertion the test would still pass on a policy that had
				// stopped modelling kinds and merely happened to deny 12.50 for other
				// reasons.
				if len(res.Decision.Checks) != 1 ||
					res.Decision.Checks[0].Rule != policy.RuleIntentValidation ||
					res.Decision.Checks[0].Result != "FAIL" {
					t.Fatalf("evaluation trace %+v, want exactly one FAILED %s check: an unmodelled kind must be refused by law, before any other rule runs",
						res.Decision.Checks, policy.RuleIntentValidation)
				}

				// Durably: the refusal is audited, and NO approval record exists anywhere a
				// later Decide could reach. "Rejected at API" means no path remains, not
				// that the first attempt returned an error.
				if n := at15CountEvents(t, st, "policy.evaluated"); n != 1 {
					t.Fatalf("policy.evaluated events = %d, want 1: an unauthorized advance must leave an audit trail", n)
				}
				if n := at15CountEvents(t, st, "approval.requested"); n != 0 {
					t.Fatalf("approval.requested events = %d, want 0: a denied advance opened a recoverable approval record", n)
				}
				if got := len(f.l.Requests()); got != 0 {
					t.Fatalf("%d requests visible after a denied advance, want 0", got)
				}

				// The nonce IS burned here, and the asymmetry with (b) below is deliberate
				// rather than an oversight: Submit claims the nonce before it evaluates
				// (lifecycle.go's claimNonce call precedes policy.Evaluate), so a DENIED
				// intent is spent. Correcting the kind and re-presenting the same nonce is
				// refused as a replay, which is the conservative direction for money: a
				// policy denial is not a routing mistake to be retried, it is a verdict.
				if n := at15CountNonceClaims(t, st, in.Nonce); n != 1 {
					t.Fatalf("nonce claims = %d, want 1: Submit claims the nonce before it evaluates, so a denied intent is spent", n)
				}
				corrected := in
				corrected.Kind = ""
				if _, err := f.l.Submit(ctx, corrected); !errors.Is(err, ErrNonceReplayed) {
					t.Fatalf("same nonce resubmitted with the kind corrected: %v, want ErrNonceReplayed", err)
				}
			})
		}
	})

	// ─────────────────────────────────────────────────────────────────────
	// (b) The ADVANCE door refuses a payment kind, before it claims anything
	// ─────────────────────────────────────────────────────────────────────
	t.Run("advance door refuses a payment kind", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			kind string
		}{
			// "" is the back-compat spelling of payment (see policy.KindPayment's doc):
			// every caller predating the field submits it, so this is the shape a misrouted
			// legacy purchase actually has, and it is the case the design names.
			{"unset kind, which means payment", ""},
			{"explicit payment kind", policy.KindPayment},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f, st := at15Fixture(t)
				ctx := context.Background()

				wrong := f.demoIntent(func(i *Intent) {
					i.IntentID = "pi_at15_wrong_door"
					i.Kind = tc.kind
					i.Nonce = "0x" + strings.Repeat("15", 32)
				})

				res, err := f.l.SubmitAdvance(ctx, wrong)
				if !errors.Is(err, ErrWrongKind) {
					t.Fatalf("SubmitAdvance of a payment-kind intent: %v, want ErrWrongKind", err)
				}
				// Fail closed WITH an explanation, not a bare sentinel: the refusal names
				// the kind this door requires, so whoever reads the log knows which door to
				// use. ErrWrongKind's own text names no kind, so this asserts the wrap.
				if !strings.Contains(err.Error(), policy.KindAdvance) {
					t.Fatalf("refusal does not name the required kind %q: %v", policy.KindAdvance, err)
				}
				if res.Request != nil {
					t.Fatalf("the advance door opened a request for a payment intent: %+v", res.Request)
				}

				// A payment must never reach this door's unconditional PENDING state.
				// SubmitAdvance skips policy.Evaluate entirely, by design, so a payment
				// landing there would be one owner tap away from execution having faced no
				// job budget, no per-transaction limit, no merchant allowlist and no daily
				// cap. Nothing visible and nothing durable is the only acceptable outcome.
				if got := len(f.l.Requests()); got != 0 {
					t.Fatalf("%d requests visible after the wrong-door refusal, want 0", got)
				}
				if n := at15CountEvents(t, st, "approval.requested"); n != 0 {
					t.Fatalf("approval.requested events = %d, want 0: a payment reached the human-only advance door", n)
				}

				// The refusal precedes the nonce claim (the kind guard is the first check in
				// SubmitAdvance, ahead of both the freeze gate and claimNonce), so a caller
				// that merely named the wrong method has not spent the intent...
				if n := at15CountNonceClaims(t, st, wrong.Nonce); n != 0 {
					t.Fatalf("nonce claims = %d, want 0: the wrong-door refusal burned the nonce", n)
				}

				// ...and the SAME intent goes through cleanly on its own door. This is AT-09's
				// "stops new claims without burning the nonce" property applied to routing: a
				// routing mistake stays correctable, while a policy DENY (direction (a)) is
				// deliberately not.
				//
				// It is also this file's non-vacuity guard. It proves the fixture intent is
				// genuinely acceptable to Submit, so direction (a)'s DENY on the very same
				// intent is caused by the Kind field alone and by nothing about the merchant,
				// the amount or the demo policy's limits.
				ok, err := f.l.Submit(ctx, wrong)
				if err != nil {
					t.Fatalf("the same intent through the payment door: %v, so the wrong-door refusal did burn it", err)
				}
				if ok.Request == nil || ok.Decision.Outcome != policy.HumanApprovalRequired {
					t.Fatalf("the 4.00 payment must escalate on its own door, got outcome %s / request %+v",
						ok.Decision.Outcome, ok.Request)
				}
			})
		}
	})
}
