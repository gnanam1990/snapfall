package approval

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The acceptance criteria, verbatim (docs/SRS-v4-annex.md §12.2):
//
//   AT-03 | Approval-required purchase | 4.00 USDC over threshold → task pauses,
//          approval sent; no signature exists before approval.
//   AT-04 | Rejected alternative | Reject + request-cheaper → original intent terminal;
//          Research proceeds with lower-cost source.
//   AT-05 | Substitution attack | Amount/merchant changed post-approval → signing
//          denied; new approval required.
//
// "No signature exists" is asserted literally: the Executor is a counter, and the count
// must be zero until approval and exactly one after.

// fakeClock is a settable clock — expiry tests are deterministic, never sleep-based.
type fakeClock struct{ now atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.now.Store(t.UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.now.Add(int64(d)) }

// countingExecutor counts invocations — the "signature" stand-in.
type countingExecutor struct{ n atomic.Int32 }

func (e *countingExecutor) fn() Executor {
	return func(_ context.Context, g Grant) error {
		if g.Empty() {
			panic("executor received a forged/empty grant")
		}
		e.n.Add(1)
		return nil
	}
}

type fixture struct {
	l     *Lifecycle
	clock *fakeClock
	exec  *countingExecutor
	// version is swappable to test policy-change invalidation.
	version atomic.Value
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return newFixtureOn(t, st)
}

func newFixtureOn(t *testing.T, st *store.Store) *fixture {
	t.Helper()
	f := &fixture{
		clock: newFakeClock(time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)),
		exec:  &countingExecutor{},
	}
	f.version.Store("pol_7")

	f.l = New(st, f.clock.Now)
	f.l.Policy = func() (policy.PolicyConfig, string) {
		return policy.DemoPolicy(), f.version.Load().(string)
	}
	f.l.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	return f
}

// demoIntent is the $4.00 escalation intent unless overridden.
func (f *fixture) demoIntent(over func(*Intent)) Intent {
	in := Intent{
		IntentID:     "pi_at03",
		OrgID:        "org_demo",
		JobID:        "job_104",
		TaskID:       "task_research_01",
		AgentID:      "due-diligence",
		Merchant:     policy.DemoMerchantPremium,
		Resource:     "GET /v1/premium-dataset",
		AmountMicros: 4_000_000,
		Purpose:      "premium market dataset",
		Nonce:        "0x" + strings.Repeat("ab", 32),
		ExpiresAt:    f.clock.Now().Add(5 * time.Minute),
	}
	if over != nil {
		over(&in)
	}
	return in
}

// ─────────────────────────────────────────────────────────────────────────
// AT-03 — "task pauses, approval sent; no signature exists before approval"
// ─────────────────────────────────────────────────────────────────────────

func TestAT03_OverThresholdPausesForApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.l.Submit(ctx, f.demoIntent(nil))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Decision.Outcome != policy.HumanApprovalRequired {
		t.Fatalf("outcome %s, want HUMAN_APPROVAL_REQUIRED", res.Decision.Outcome)
	}
	if res.Request == nil || res.Request.State != StatePending {
		t.Fatalf("request state %v, want pending — the task pauses", res.Request)
	}

	// Execution before any decision must refuse, and the executor never runs.
	err = f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn())
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("pre-approval execute: %v, want ErrNotApproved", err)
	}
	if got := f.exec.n.Load(); got != 0 {
		t.Fatalf("executor ran %d times before approval — \"no signature exists before approval\"", got)
	}
}

func TestAT03_ApprovalUnblocksExecution(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	req, err := f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "needed for the report")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if req.State != StateApproved || req.DecidedBy != "gnanam" || req.DecidedAt.IsZero() {
		t.Fatalf("decision not recorded: %+v", req)
	}

	if err := f.l.Execute(ctx, req.Intent, req.ID, f.exec.fn()); err != nil {
		t.Fatalf("post-approval execute: %v", err)
	}
	if got := f.exec.n.Load(); got != 1 {
		t.Fatalf("executor ran %d times, want exactly 1", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// AT-04 — "original intent terminal; Research proceeds with lower-cost source"
// ─────────────────────────────────────────────────────────────────────────

func TestAT04_RejectIsTerminal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	req, err := f.l.Decide(ctx, res.Request.ID, DecideReject, "gnanam", "too expensive")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if req.State != StateRejected || req.Reason != "too expensive" {
		t.Fatalf("rejection not recorded with reason: %+v", req)
	}

	// A later conflicting approval is refused; state unchanged.
	if _, err := f.l.Decide(ctx, req.ID, DecideApprove, "gnanam", ""); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("approve-after-reject: %v, want ErrAlreadyDecided", err)
	}
	got, _ := f.l.Request(req.ID)
	if got.State != StateRejected {
		t.Fatalf("state moved to %s after refused decision", got.State)
	}

	// Execution refused forever.
	if err := f.l.Execute(ctx, req.Intent, req.ID, f.exec.fn()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("execute on rejected: %v, want ErrNotApproved", err)
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran for a rejected intent")
	}
}

// The full demo beat, with the explicit provenance link (Step-4 addition 2):
// $4.00 escalates → owner requests cheaper → $0.06 alternative carries
// AlternativeTo=original request → auto-approves → executes. Original stays dead.
func TestAT04_RequestAlternativeFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	orig, _ := f.l.Submit(ctx, f.demoIntent(nil))
	if _, err := f.l.Decide(ctx, orig.Request.ID, DecideRequestAlternative, "gnanam", "find a cheaper source"); err != nil {
		t.Fatalf("request-alternative: %v", err)
	}

	// The worker adapts: a FRESH intent, fresh nonce, explicitly linked to the decision
	// that spawned it — the activity feed renders causality from this field, not from
	// ordering guesses.
	alt := f.demoIntent(func(in *Intent) {
		in.IntentID = "pi_at04_alt"
		in.Merchant = policy.DemoMerchantBenchmark
		in.Resource = "GET /v1/benchmark-summary"
		in.AmountMicros = 60_000
		in.Purpose = "benchmark summary (cheaper source)"
		in.Nonce = "0x" + strings.Repeat("cd", 32)
		in.AlternativeTo = orig.Request.ID
	})
	res, err := f.l.Submit(ctx, alt)
	if err != nil {
		t.Fatalf("alternative submit: %v", err)
	}
	if res.Decision.Outcome != policy.AutoApprove {
		t.Fatalf("the 0.06 alternative must auto-approve, got %s", res.Decision.Outcome)
	}
	if res.Request.Intent.AlternativeTo != orig.Request.ID {
		t.Fatalf("provenance link lost: %q", res.Request.Intent.AlternativeTo)
	}
	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); err != nil {
		t.Fatalf("alternative execute: %v", err)
	}
	if f.exec.n.Load() != 1 {
		t.Fatal("alternative did not execute exactly once")
	}

	// "Original intent terminal": still refuses execution.
	if err := f.l.Execute(ctx, orig.Request.Intent, orig.Request.ID, f.exec.fn()); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("original still executable: %v", err)
	}
}

// A forged link — pointing at a request that never asked for an alternative,
// or at another job's request — is refused at intake.
func TestAT04_BadAlternativeLinkRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Link target does not exist.
	_, err := f.l.Submit(ctx, f.demoIntent(func(in *Intent) {
		in.Nonce = "0x" + strings.Repeat("11", 32)
		in.AlternativeTo = "apr_nonexistent"
	}))
	if !errors.Is(err, ErrBadAlternativeLink) {
		t.Fatalf("dangling link: %v, want ErrBadAlternativeLink", err)
	}

	// Link target exists but was APPROVED, not alternative-requested.
	ok, _ := f.l.Submit(ctx, f.demoIntent(func(in *Intent) {
		in.IntentID = "pi_approved"
		in.Nonce = "0x" + strings.Repeat("22", 32)
	}))
	f.l.Decide(ctx, ok.Request.ID, DecideApprove, "gnanam", "")
	_, err = f.l.Submit(ctx, f.demoIntent(func(in *Intent) {
		in.IntentID = "pi_bad_link"
		in.Nonce = "0x" + strings.Repeat("33", 32)
		in.AlternativeTo = ok.Request.ID
	}))
	if !errors.Is(err, ErrBadAlternativeLink) {
		t.Fatalf("link to an approved request: %v, want ErrBadAlternativeLink", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// AT-05 — "amount/merchant changed post-approval → signing denied; new approval required"
// ─────────────────────────────────────────────────────────────────────────

func TestAT05_AmountChangeInvalidatesApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	tampered := res.Request.Intent
	tampered.AmountMicros += 1 // one micro-USDC

	if err := f.l.Execute(ctx, tampered, res.Request.ID, f.exec.fn()); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("amount change: %v, want ErrHashMismatch", err)
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran on a tampered amount — signing was not denied")
	}
}

func TestAT05_MerchantChangeInvalidatesApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	tampered := res.Request.Intent
	tampered.Merchant = "api.attacker.example"

	if err := f.l.Execute(ctx, tampered, res.Request.ID, f.exec.fn()); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("merchant change: %v, want ErrHashMismatch", err)
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran on a tampered merchant — signing was not denied")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Expiry (SEC-006) — deterministic via the injected clock
// ─────────────────────────────────────────────────────────────────────────

func TestExpiry_PendingRequestExpires(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.clock.Advance(6 * time.Minute) // past the 5-minute window

	_, err := f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "too late")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("decide after expiry: %v, want ErrExpired", err)
	}
	got, _ := f.l.Request(res.Request.ID)
	if got.State != StateExpired {
		t.Fatalf("state %s, want expired", got.State)
	}
}

func TestExpiry_ApprovedButUnexecutedExpires(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	f.clock.Advance(6 * time.Minute) // approval granted, then the window passes

	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); !errors.Is(err, ErrExpired) {
		t.Fatalf("execute after expiry: %v, want ErrExpired", err)
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran on an expired approval")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Policy-version binding (SEC-006 / threat model: "bound to ... policy version")
// ─────────────────────────────────────────────────────────────────────────

func TestPolicyVersionChangeInvalidatesApproval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	f.version.Store("pol_8") // the policy changed between approval and execution

	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); !errors.Is(err, ErrPolicyChanged) {
		t.Fatalf("execute across a policy bump: %v, want ErrPolicyChanged", err)
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran under a stale policy version")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Idempotency and exactly-once
// ─────────────────────────────────────────────────────────────────────────

func TestDecision_Idempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))

	r1, err := f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "ok")
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	decidedAt := r1.DecidedAt

	// The SAME decision again: one effect, not two — and not an error.
	r2, err := f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "ok")
	if err != nil {
		t.Fatalf("repeated approve must be a recognized no-op: %v", err)
	}
	if !r2.DecidedAt.Equal(decidedAt) {
		t.Fatal("repeated decision changed state — two effects from one decision")
	}

	// A CONFLICTING decision is refused.
	if _, err := f.l.Decide(ctx, res.Request.ID, DecideReject, "gnanam", "no"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("conflicting decision: %v, want ErrAlreadyDecided", err)
	}
}

func TestExecution_ExactlyOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); !errors.Is(err, ErrAlreadyExecuted) {
		t.Fatalf("second execute: %v, want ErrAlreadyExecuted", err)
	}
	if got := f.exec.n.Load(); got != 1 {
		t.Fatalf("executor ran %d times, want exactly 1", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Nonce replay — intake and restart
// ─────────────────────────────────────────────────────────────────────────

func TestNonce_DuplicateSubmissionRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first := f.demoIntent(nil)
	if _, err := f.l.Submit(ctx, first); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Different intent, SAME nonce.
	second := f.demoIntent(func(in *Intent) { in.IntentID = "pi_replay"; in.AmountMicros = 40_000 })
	if _, err := f.l.Submit(ctx, second); !errors.Is(err, ErrNonceReplayed) {
		t.Fatalf("replayed nonce: %v, want ErrNonceReplayed", err)
	}
}

// The store, not a map, is the nonce authority: a replay is refused even by a fresh
// lifecycle over the same database — the restart half of the replay defense.
func TestNonce_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	st1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f1 := newFixtureOn(t, st1)
	in := f1.demoIntent(nil)
	if _, err := f1.l.Submit(ctx, in); err != nil {
		t.Fatalf("submit: %v", err)
	}
	st1.Close()

	// "Restart": new store handle, new lifecycle, same database file.
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	f2 := newFixtureOn(t, st2)

	replay := f2.demoIntent(func(i *Intent) { i.IntentID = "pi_after_restart" })
	if _, err := f2.l.Submit(ctx, replay); !errors.Is(err, ErrNonceReplayed) {
		t.Fatalf("nonce replay after restart: %v, want ErrNonceReplayed", err)
	}
}

// A denied intent opens no approval path at all.
func TestSubmit_DeniedIntentHasNoRequest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	res, err := f.l.Submit(ctx, f.demoIntent(func(in *Intent) {
		in.Merchant = "api.unlisted.example" // deny: not allowlisted
	}))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("denied intent: %v, want ErrDenied", err)
	}
	if res.Request != nil {
		t.Fatal("a denied intent must not open an approval request")
	}
	if res.Decision.Reason == nil || res.Decision.Reason.Rule != policy.RuleMerchantAllowlist {
		t.Fatalf("deny reason missing or wrong: %+v", res.Decision.Reason)
	}
}
