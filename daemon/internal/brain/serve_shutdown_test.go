package brain

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// ctxWaitingWorker blocks until its TASK context is cancelled — standing in for a
// Purchase blocked awaiting an owner decision, which wakes on ctx.Done the same way.
type ctxWaitingWorker struct{ started chan struct{} }

func (w ctxWaitingWorker) Kind() string { return "due-diligence" }
func (w ctxWaitingWorker) Handle(ctx context.Context, _ envelope.Envelope, _ worker.Report, _ worker.Purchase) error {
	close(w.started)
	<-ctx.Done()
	return ctx.Err()
}

// Serve pin 1: a second Recover on the same Brain is refused — two replays over the same
// event log is the double-recovery hazard.
func TestRecover_SecondCallRefused(t *testing.T) {
	b, _, _ := newTestBrain(t)
	if err := b.Recover(); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	if err := b.Recover(); err == nil {
		t.Fatal("second Recover must be refused — it would replay state over a live working set")
	}
}

// Serve pins 2+3 composed: SIGTERM (root-context cancellation) wakes a blocked task and
// refuses new dispatches; a restart then escalates the interrupted job exactly as a crash
// would — from the event log's perspective the two are indistinguishable.
func TestServe_SigtermInterruptsBlockedTaskAndRestartEscalates(t *testing.T) {
	mem, err := NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	b1, st, _ := newTestBrain(t)
	b1.memory = mem
	root, sigterm := context.WithCancel(context.Background())
	b1.SetRootContext(root)
	w := ctxWaitingWorker{started: make(chan struct{})}
	b1.mu.Lock()
	b1.workers["due-diligence"] = w
	b1.mu.Unlock()

	ctx := context.Background()
	if _, err := b1.HandleOwnerRequest(ctx, "job_sig", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b1.Confirm(ctx, "job_sig", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	<-w.started // the task is running and blocked, exactly as a Purchase awaiting the owner

	sigterm() // SIGTERM lands

	// The blocked task wakes and surfaces its interruption; the drain returns — no
	// unkillable goroutine survives shutdown.
	if err := b1.AwaitTask("job_sig"); err == nil {
		t.Fatal("interrupted task must surface the interruption, not report success")
	}
	b1.WaitTasks()

	// New dispatches are refused after shutdown — "stops new claims", the freeze posture.
	if _, err := b1.HandleOwnerRequest(ctx, "job_late", "Beta Inc"); err != nil {
		t.Fatalf("late request: %v", err)
	}
	if err := b1.Confirm(ctx, "job_late", "gnanam"); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("dispatch after shutdown: %v, want a shutting-down refusal", err)
	}

	// RESTART: recovery treats the cleanly-shut-down job like a crashed one — escalate to
	// the owner, never resume or re-run (decision #1 composes with shutdown).
	b2 := New(b1.log, st, mem, b1.funding)
	b2.SetScoper(StubScoper{})
	var ran atomic.Bool
	b2.mu.Lock()
	b2.workers["due-diligence"] = windowWorker{ran: &ran}
	b2.mu.Unlock()
	if err := b2.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := b2.EscalateInterruptedTasks(ctx); err != nil {
		t.Fatalf("EscalateInterruptedTasks: %v", err)
	}
	js, ok := b2.Job("job_sig")
	if !ok || js.Stage != StageEscalated {
		t.Fatalf("stage after restart = %v (ok=%v), want escalated — clean shutdown must equal crash", js.Stage, ok)
	}
	if ran.Load() {
		t.Fatal("the worker RE-RAN after restart — interrupted tasks must escalate, not resume")
	}
}
