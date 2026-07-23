package advancing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func rig(t *testing.T) (*Flow, *approval.Lifecycle, *store.Store, *funding.Agent, chan struct{}) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "adv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	fund := funding.New()
	f := New(life, st, fund, slog.New(slog.NewTextHandler(io.Discard, nil)), "org_demo", 5*time.Minute)
	done := make(chan struct{}, 4)
	f.afterExecute = func() { done <- struct{}{} }
	return f, life, st, fund, done
}

func count(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind=?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The advance is HUMAN-AUTHORIZED ONLY: the proposal enters pending unconditionally,
// lands in the same inbox as payments, and NO policy evaluation ever ran for it — the
// absence of a policy.evaluated event is the skip-Evaluate decision made observable.
func TestAdvance_ProposalIsHumanOnlyAndUnevaluated(t *testing.T) {
	f, life, st, _, _ := rig(t)
	req, err := f.Propose(context.Background(), "job_adv", "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if req.State != approval.StatePending || req.Intent.Kind != policy.KindAdvance {
		t.Fatalf("proposal: %+v, want pending advance-kind", req)
	}
	if req.Intent.AmountMicros != 12_500_000 {
		t.Fatalf("principal %d, want 12500000 (50%% of 25.00)", req.Intent.AmountMicros)
	}
	pending := life.PendingRequests()
	if len(pending) != 1 || pending[0].ID != req.ID {
		t.Fatalf("the advance must sit in the SAME inbox: %+v", pending)
	}
	if n := count(t, st, "policy.evaluated"); n != 0 {
		t.Fatalf("policy.evaluated events = %d — the advance must skip Evaluate entirely", n)
	}
	// The lifecycle's front door for payments refuses the kind outright.
	if _, err := life.SubmitAdvance(context.Background(), approval.Intent{Kind: "payment"}); !errors.Is(err, approval.ErrWrongKind) {
		t.Fatalf("SubmitAdvance with payment kind: %v, want ErrWrongKind", err)
	}
}

// Owner approves in the inbox -> Grant -> Funding records request_advance -> the
// honest stop. The durable write-ahead claim (payment.executing) precedes the stop,
// and a replayed execution is refused — exactly-once through the same gate set as
// every payment.
func TestAdvance_ApproveExecutesToPendingChainExactlyOnce(t *testing.T) {
	f, life, st, fund, done := rig(t)
	ctx := context.Background()
	req, err := f.Propose(ctx, "job_adv", "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "gnanam", "advance approved — the snap"); err != nil {
		t.Fatal(err)
	}
	<-done

	if n := count(t, st, "advance.pending_chain"); n != 1 {
		t.Fatalf("advance.pending_chain events = %d, want 1", n)
	}
	// Write-ahead claim precedes the stop, durably.
	var claimSeq, stopSeq int
	if err := st.DB().QueryRow(`SELECT seq FROM events WHERE kind='payment.executing'`).Scan(&claimSeq); err != nil {
		t.Fatalf("the durable claim must exist: %v", err)
	}
	if err := st.DB().QueryRow(`SELECT seq FROM events WHERE kind='advance.pending_chain'`).Scan(&stopSeq); err != nil {
		t.Fatal(err)
	}
	if claimSeq >= stopSeq {
		t.Fatalf("claim (seq %d) must precede the honest stop (seq %d)", claimSeq, stopSeq)
	}
	// Funding recorded the Phase-2 instruction.
	ins := fund.Executed()
	if len(ins) != 1 || ins[0].Kind != "request_advance" || ins[0].AmountMicros != 12_500_000 {
		t.Fatalf("funding instructions: %+v", ins)
	}
	// Replay: the lifecycle refuses a second execution of the same approval.
	snap, _ := life.Snapshot(req.ID)
	err = life.Execute(ctx, snap.Intent, req.ID, func(context.Context, approval.Grant) error { return nil })
	if !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("replayed execution: %v, want ErrAlreadyExecuted", err)
	}
}

// A rejection is a full stop: no instruction, no chain record, nothing halted-looking.
func TestAdvance_RejectExecutesNothing(t *testing.T) {
	f, life, st, fund, done := rig(t)
	ctx := context.Background()
	req, err := f.Propose(ctx, "job_adv", "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideReject, "gnanam", "not now"); err != nil {
		t.Fatal(err)
	}
	<-done
	if n := count(t, st, "advance.pending_chain"); n != 0 {
		t.Fatal("a rejected advance produced a chain record")
	}
	if len(fund.Executed()) != 0 {
		t.Fatal("a rejected advance reached Funding")
	}
}

// AT-09's advance clause, both ends: an engaged freeze refuses the PROPOSAL at intake
// (nonce not burned), and an approval decided during a freeze cannot EXECUTE — the
// lifecycle's atomic admission refuses the Grant, visibly.
func TestAdvance_FreezeGatesProposeAndExecute(t *testing.T) {
	f, life, st, fund, done := rig(t)
	ctx := context.Background()
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	life.Freeze = reg

	// Intake half.
	if _, err := reg.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "incident"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Propose(ctx, "job_adv", "25.00"); err == nil {
		t.Fatal("a frozen org accepted an advance proposal")
	}
	if err := reg.Lift(ctx, freeze.KindOrg, "org_demo", "gnanam", "resolved"); err != nil {
		t.Fatal(err)
	}

	// Execution half: propose, freeze, approve — the await must be refused at the gate.
	req, err := f.Propose(ctx, "job_adv", "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Engage(ctx, freeze.KindJob, "job_adv", "gnanam", "incident"); err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "gnanam", "approved during freeze"); err != nil {
		t.Fatal(err)
	}
	<-done
	if n := count(t, st, "advance.pending_chain"); n != 0 {
		t.Fatal("a frozen advance reached the chain stop")
	}
	if len(fund.Executed()) != 0 {
		t.Fatal("a frozen advance reached Funding")
	}
	if n := count(t, st, "advance.halted"); n != 1 {
		t.Fatalf("the refusal must be visible: advance.halted = %d, want 1", n)
	}
}

// The restart posture: an approved-but-unexecuted advance is NEVER auto-executed by a
// new process — it is escalated to the owner. And an EXECUTED advance stays executed
// across restart: the durable claim survives replay, so re-execution is refused.
func TestAdvance_RestartNeverAutoExecutesAndClaimSurvives(t *testing.T) {
	f, life, st, _, done := rig(t)
	ctx := context.Background()

	// Approved-but-unexecuted: submit directly (no await goroutine — the "old process"
	// died before executing).
	res, err := life.SubmitAdvance(ctx, approval.Intent{
		IntentID: "adv_orphan", OrgID: "org_demo", JobID: "job_orphan", AgentID: "funding",
		Kind: policy.KindAdvance, Resource: "FloatPool.requestAdvance",
		AmountMicros: 500_000, MaxAmountMicros: 500_000, Purpose: "orphaned advance",
		Nonce:     "0x" + strings.Repeat("aa", 32),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "approved then crashed"); err != nil {
		t.Fatal(err)
	}

	// An executed advance, for the second half.
	req, err := f.Propose(ctx, "job_done", "10.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "gnanam", "approved and executed"); err != nil {
		t.Fatal(err)
	}
	<-done

	// "Restart": a fresh lifecycle + flow over the same store.
	life2 := approval.New(st, time.Now)
	life2.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life2.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	if err := life2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	f2 := New(life2, st, funding.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), "org_demo", 5*time.Minute)

	n, err := f2.EscalateInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("escalated = %d, want exactly the orphaned approval (the executed one must not escalate)", n)
	}
	if c := count(t, st, "advance.interrupted"); c != 1 {
		t.Fatalf("advance.interrupted events = %d, want 1", c)
	}
	// No auto-execution happened for the orphan...
	if c := count(t, st, "advance.pending_chain"); c != 1 {
		t.Fatalf("advance.pending_chain = %d — the restart must not have executed the orphan", c)
	}
	// ...and the executed one is locked by its durable claim.
	snap, _ := life2.Snapshot(req.ID)
	err = life2.Execute(ctx, snap.Intent, req.ID, func(context.Context, approval.Grant) error { return nil })
	if !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("post-restart replay: %v, want ErrAlreadyExecuted", err)
	}
}
