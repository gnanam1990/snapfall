package brain

import (
	"context"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// blockingWorker holds its Handle open until released — standing in for a DD worker
// blocked inside Purchase awaiting an approval decision.
type blockingWorker struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingWorker) Kind() string { return "due-diligence" }
func (w *blockingWorker) Handle(_ context.Context, _ envelope.Envelope, _ worker.Report) error {
	close(w.started)
	<-w.release
	return nil
}

// TestAsyncDispatch_ConfirmReturnsBeforeWorkerFinishes pins the property that motivated
// the G8 async change (decision #2): Confirm() MUST return before the worker completes,
// so a Purchase that blocks awaiting approval never holds the owner's call hostage and a
// single-threaded owner surface cannot deadlock. Once every other test awaits completion,
// THIS is the test asserting the asynchrony — a future refactor that silently makes
// assignment synchronous again fails here while the rest of the suite stays green.
func TestAsyncDispatch_ConfirmReturnsBeforeWorkerFinishes(t *testing.T) {
	b, _, _ := newTestBrain(t)
	bw := &blockingWorker{started: make(chan struct{}), release: make(chan struct{})}
	b.mu.Lock()
	b.workers["due-diligence"] = bw
	b.mu.Unlock()

	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_async", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}

	// Confirm dispatches and must return while the worker is still running.
	if err := b.Confirm(ctx, "job_async", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	select {
	case <-bw.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never entered Handle")
	}

	// The worker is blocked on `release`, so the task CANNOT have finished — yet Confirm()
	// has already returned. If the task is already done here, Confirm() waited for it.
	b.mu.Lock()
	h := b.tasks["job_async"]
	b.mu.Unlock()
	select {
	case <-h.done:
		t.Fatal("SYNCHRONOUS REGRESSION: the task completed before the worker was released — Confirm() waited for the worker to finish")
	default:
		// still running — the async property holds
	}

	close(bw.release)
	if err := b.AwaitTask("job_async"); err != nil {
		t.Fatalf("task: %v", err)
	}
}
