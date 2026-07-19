package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fnWorker adapts a function to Worker.
type fnWorker struct {
	name string
	fn   func(context.Context) error
}

func (w *fnWorker) Name() string                  { return w.name }
func (w *fnWorker) Run(ctx context.Context) error { return w.fn(ctx) }

// NFR-001: "Supervisor recovers crashed workers." A worker that keeps failing is
// restarted until its budget runs out, then left alone rather than spinning forever.
func TestSupervise_RestartsCrashedWorker(t *testing.T) {
	var runs atomic.Int32
	sup := New(quietLogger(), 3, time.Millisecond)

	if err := sup.RegisterEssential(&fnWorker{
		name: "flaky",
		fn: func(context.Context) error {
			runs.Add(1)
			return errors.New("boom")
		},
	}); err != nil {
		t.Fatalf("RegisterEssential: %v", err)
	}

	sup.Start(context.Background())
	sup.Wait()

	if got := runs.Load(); got != 3 {
		t.Errorf("ran %d times, want 3 (the restart budget)", got)
	}
	h := sup.Health()[0]
	if h.State != StateFailed {
		t.Errorf("final state = %q, want %q", h.State, StateFailed)
	}
	if h.LastErr != "boom" {
		t.Errorf("last error = %q, want boom", h.LastErr)
	}
}

// A worker that recovers should not exhaust its budget.
func TestSupervise_StopsRestartingOnceHealthy(t *testing.T) {
	var runs atomic.Int32
	sup := New(quietLogger(), 5, time.Millisecond)

	sup.RegisterEssential(&fnWorker{
		name: "recovers",
		fn: func(context.Context) error {
			if runs.Add(1) < 3 {
				return errors.New("not yet")
			}
			return nil // third attempt succeeds
		},
	})

	sup.Start(context.Background())
	sup.Wait()

	if got := runs.Load(); got != 3 {
		t.Errorf("ran %d times, want 3", got)
	}
	if h := sup.Health()[0]; h.State != StateComplete {
		t.Errorf("final state = %q, want %q", h.State, StateComplete)
	}
	if h := sup.Health()[0]; h.Restarts != 2 {
		t.Errorf("restarts = %d, want 2", h.Restarts)
	}
}

// A cancelled parent context is a clean shutdown, NOT a crash — otherwise every
// Ctrl-C would burn restart budget and log spurious failures.
func TestSupervise_CancellationIsNotACrash(t *testing.T) {
	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	sup := New(quietLogger(), 5, time.Millisecond)
	sup.RegisterEssential(&fnWorker{
		name: "long-running",
		fn: func(ctx context.Context) error {
			runs.Add(1)
			<-ctx.Done()
			return ctx.Err() // returns a non-nil error, but the cause is cancellation
		},
	})

	sup.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	sup.Wait()

	if got := runs.Load(); got != 1 {
		t.Errorf("ran %d times, want 1 — cancellation must not trigger a restart", got)
	}
	h := sup.Health()[0]
	if h.State != StateStopped {
		t.Errorf("final state = %q, want %q", h.State, StateStopped)
	}
	if h.Restarts != 0 {
		t.Errorf("restarts = %d, want 0", h.Restarts)
	}
}

// The bug this test exists for: an infinite infrastructure worker (the outbox publisher)
// pinned the daemon open forever, so a bounded run never terminated. Essential workers
// finishing must tear the tree down.
func TestSupervise_EssentialCompletionStopsInfrastructure(t *testing.T) {
	var infraStopped atomic.Bool

	sup := New(quietLogger(), 1, time.Millisecond)

	sup.RegisterEssential(&fnWorker{
		name: "agent",
		fn: func(context.Context) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	})
	sup.Register(&fnWorker{
		name: "publisher",
		fn: func(ctx context.Context) error {
			<-ctx.Done() // runs forever until told otherwise
			infraStopped.Store(true)
			return nil
		},
	})

	done := make(chan struct{})
	go func() {
		sup.Start(context.Background())
		sup.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop after its essential worker finished")
	}
	if !infraStopped.Load() {
		t.Error("infrastructure worker was never signalled to stop")
	}
}

// With no essential workers, the daemon runs until interrupted.
func TestSupervise_NoEssentialWorkersRunsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sup := New(quietLogger(), 1, time.Millisecond)

	sup.Register(&fnWorker{
		name: "forever",
		fn: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	})

	done := make(chan struct{})
	go func() { sup.Start(ctx); sup.Wait(); close(done) }()

	select {
	case <-done:
		t.Fatal("supervisor stopped without being cancelled")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
}

func TestRegister_RejectsDuplicateNames(t *testing.T) {
	sup := New(quietLogger(), 1, time.Millisecond)
	w := &fnWorker{name: "dup", fn: func(context.Context) error { return nil }}

	if err := sup.Register(w); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := sup.Register(w); err == nil {
		t.Error("registering the same name twice must fail — health reporting keys on it")
	}
}

// FR-ORG-004 requires per-employee status; the supervisor is where it comes from.
func TestHealth_ReportsEveryWorker(t *testing.T) {
	sup := New(quietLogger(), 1, time.Millisecond)
	sup.Register(&fnWorker{name: "a", fn: func(context.Context) error { return nil }})
	sup.Register(&fnWorker{name: "b", fn: func(context.Context) error { return nil }})

	h := sup.Health()
	if len(h) != 2 {
		t.Fatalf("Health() returned %d workers, want 2", len(h))
	}
	for _, s := range h {
		if s.State != StateIdle {
			t.Errorf("%s state = %q before Start, want %q", s.Name, s.State, StateIdle)
		}
	}
}
