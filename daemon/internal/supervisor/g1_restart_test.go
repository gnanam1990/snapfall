package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// G1 gate: kill a worker mid-run; confirm it restarts cleanly with state intact.
//
// The worker's "state" lives in the store, not in the process — that is what makes the
// restart clean. The worker persists one event per step; it is killed partway through;
// on restart it reads how far it got from the store and continues from there. The final
// event log must contain every step exactly once — no lost steps, no repeats.
func TestG1_KilledWorkerRestartsWithStateIntact(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "g1.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	const totalSteps = 6
	crashed := false

	stepWorker := &fnWorker{
		name: "stepper",
		fn: func(ctx context.Context) error {
			// Recover position from persisted state — the process was killed, the DB was not.
			done, err := st.EventCount(ctx)
			if err != nil {
				return err
			}
			for step := done + 1; step <= totalSteps; step++ {
				if _, err := st.Append(ctx, store.Event{
					Kind:     "g1.step",
					EntityID: "stepper",
					Payload:  map[string]any{"step": step},
				}); err != nil {
					return err
				}
				// The kill: die once, partway through, AFTER committing step 3.
				if step == 3 && !crashed {
					crashed = true
					return errors.New("killed mid-run")
				}
			}
			return nil
		},
	}

	sup := New(quietLogger(), 5, time.Millisecond)
	if err := sup.RegisterEssential(stepWorker); err != nil {
		t.Fatalf("RegisterEssential: %v", err)
	}
	sup.Start(ctx)
	sup.Wait()

	if !crashed {
		t.Fatal("the crash never happened; the test proved nothing")
	}
	h := sup.Health()[0]
	if h.State != StateComplete {
		t.Fatalf("worker must finish after restart, final state %q (lastErr %q)", h.State, h.LastErr)
	}
	if h.Restarts != 1 {
		t.Errorf("restarts = %d, want exactly 1", h.Restarts)
	}

	// State intact: all steps present, none duplicated.
	n, err := st.EventCount(ctx)
	if err != nil {
		t.Fatalf("EventCount: %v", err)
	}
	if n != totalSteps {
		t.Errorf("event count = %d, want %d — a lost or repeated step means restart was not clean", n, totalSteps)
	}
}
