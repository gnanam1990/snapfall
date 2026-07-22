package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// ═══════════════════════════════════════════════════════════════════════════
// G5 — THE SUNDAY-26 EXIT GATE
//
// A stub Due Diligence job routes scope → confirm → assign → report end-to-end
// with no manual intervention. Phase 1 is not done until this passes.
// ═══════════════════════════════════════════════════════════════════════════

func TestG5_ExitGate_StubDDJobFullLoop(t *testing.T) {
	b, st, _ := newTestBrain(t)
	ctx := context.Background()

	// ── 1. SCOPE: owner asks; Brain proposes scope + quote. ──
	proposal, err := b.HandleOwnerRequest(ctx, "job_dd_1", "Acme Corp acquisition target")
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if !strings.Contains(proposal.Scope, "Acme Corp") {
		t.Errorf("scope does not reflect the request: %q", proposal.Scope)
	}
	if proposal.QuoteUSDC != "25.00" {
		t.Errorf("quote = %q, want 25.00 (the demo job)", proposal.QuoteUSDC)
	}

	// The job is scoped and WAITING — nothing has been assigned yet.
	js, ok := b.Job("job_dd_1")
	if !ok || js.Stage != StageScoped {
		t.Fatalf("after scoping: stage %q, want %q", js.Stage, StageScoped)
	}
	jm, _ := b.memory.Get("job_dd_1")
	if jm.AssignedWorker != "" {
		t.Fatal("a worker was assigned before the owner confirmed — the discipline is broken")
	}

	// ── 2. CONFIRM: the owner's decision is recorded, then assignment happens. ──
	if err := b.Confirm(ctx, "job_dd_1", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b, "job_dd_1")

	// ── 3+4. ASSIGN + REPORT happened as consequences — verify the end state. ──
	js, _ = b.Job("job_dd_1")
	if js.Stage != StageComplete {
		t.Fatalf("final stage %q, want %q — the loop did not close", js.Stage, StageComplete)
	}

	jm, err = b.memory.Get("job_dd_1")
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	if jm.AssignedWorker != "due-diligence" {
		t.Errorf("assigned worker %q, want due-diligence", jm.AssignedWorker)
	}
	if jm.CompletionPct != 100 {
		t.Errorf("completion %d%%, want 100", jm.CompletionPct)
	}
	if len(jm.Confirmations) != 1 || jm.Confirmations[0].By != "gnanam" || jm.Confirmations[0].At.IsZero() {
		t.Errorf("confirmation record wrong: %+v", jm.Confirmations)
	}
	if !strings.Contains(jm.Report, "Acme Corp") {
		t.Errorf("report does not carry the scope through: %q", jm.Report)
	}

	// The whole story is in the event log: request, proposal, confirm, assignment,
	// progress, report, job report — 7 messages, every hop recorded.
	n, _ := st.EventCount(ctx)
	if n != 7 {
		t.Errorf("event log holds %d messages, want 7 — a hop went unrecorded", n)
	}
}

// The discipline, negatively: an unconfirmed job cannot reach a worker.
func TestG5_ConfirmationGatesAssignment(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_gate", "Target Co"); err != nil {
		t.Fatalf("request: %v", err)
	}

	// No confirm. The job must sit scoped, unassigned, incomplete.
	js, _ := b.Job("job_gate")
	if js.Stage != StageScoped {
		t.Fatalf("stage %q, want scoped", js.Stage)
	}
	jm, _ := b.memory.Get("job_gate")
	if jm.AssignedWorker != "" || jm.CompletionPct != 0 {
		t.Fatal("work happened without owner confirmation")
	}

	// Confirming a job twice must fail — the second confirm finds it past StageScoped.
	if err := b.Confirm(ctx, "job_gate", "gnanam"); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	waitJob(b, "job_gate")
	if err := b.Confirm(ctx, "job_gate", "gnanam"); err == nil {
		t.Fatal("a second confirmation must be rejected, not re-run the job")
	}
}

// A rejected scope is terminal: recorded with the reason, never assigned.
func TestG5_RejectStopsTheJob(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_no", "Overpriced Co"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Reject(ctx, "job_no", "gnanam", "quote too high"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	js, _ := b.Job("job_no")
	if js.Stage != StageRejected {
		t.Errorf("stage %q, want rejected", js.Stage)
	}
	jm, _ := b.memory.Get("job_no")
	if jm.AssignedWorker != "" {
		t.Error("a rejected job reached a worker")
	}
	if len(jm.Confirmations) != 1 || !strings.Contains(jm.Confirmations[0].What, "quote too high") {
		t.Errorf("rejection not recorded with its reason: %+v", jm.Confirmations)
	}
}

// A worker failure lands the job in failed, recorded — not silently lost.
func TestG5_WorkerFailureIsRecorded(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()

	failing := failingWorker{}
	if err := b.RegisterWorker(failing); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	b.SetScoper(kindScoper{kind: failing.Kind()})

	if _, err := b.HandleOwnerRequest(ctx, "job_fail", "Doomed Co"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_fail", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b, "job_fail")

	js, _ := b.Job("job_fail")
	if js.Stage != StageFailed {
		t.Errorf("stage %q, want failed", js.Stage)
	}
	jm, _ := b.memory.Get("job_fail")
	if jm.Stage != string(StageFailed) {
		t.Errorf("memory stage %q, want failed", jm.Stage)
	}
}

// kindScoper routes every request to a fixed worker kind.
type kindScoper struct{ kind string }

func (s kindScoper) Scope(_ context.Context, request string) (string, string, string, error) {
	return "scope: " + request, "25.00", s.kind, nil
}

// failingWorker reports failure through the one channel it has.
type failingWorker struct{}

func (failingWorker) Kind() string { return "always-fails" }
func (failingWorker) Handle(ctx context.Context, a envelope.Envelope, report worker.Report, _ worker.Purchase) error {
	e, err := envelope.New(a.JobID, envelope.RoleWorker, envelope.TypeWorkerFailure,
		map[string]any{"reason": "stub failure"})
	if err != nil {
		return err
	}
	return report(ctx, e)
}
