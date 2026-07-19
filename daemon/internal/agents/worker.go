package agents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/events"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// HeartbeatWorker is the Day-1 dummy worker (PRD §14.3 B: "supervisor with one dummy worker").
//
// It does no reasoning and makes no proposals — it exists to prove the runtime spine end to
// end: worker -> store.Append (event + outbox, one transaction) -> publisher -> bus ->
// subscriber. The real agent execution loop (PRD §6.5) replaces Run once the action broker
// and policy engine exist.
//
// Deliberately has no key material, no network, and no filesystem access, matching the
// permissions its manifest declares.
type HeartbeatWorker struct {
	Role     Role
	Store    *store.Store
	Log      *slog.Logger
	Interval time.Duration
	// Beats bounds the run so `snapfall --once` terminates. 0 means run until cancelled.
	Beats int
}

// Name identifies the worker to the supervisor.
func (w *HeartbeatWorker) Name() string { return fmt.Sprintf("agent/%s", w.Role) }

// Run emits heartbeats until the context ends or Beats is reached.
func (w *HeartbeatWorker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}

	if _, err := w.Store.Append(ctx, store.Event{
		Kind:     events.KindWorkerStarted,
		EntityID: string(w.Role),
		Actor:    w.Name(),
		Payload:  map[string]any{"role": string(w.Role)},
	}); err != nil {
		return fmt.Errorf("recording start: %w", err)
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for beat := 1; ; beat++ {
		select {
		case <-ctx.Done():
			// Clean shutdown. The supervisor reads ctx.Err() and records this as
			// stopped rather than crashed.
			return nil
		case <-t.C:
			if _, err := w.Store.Append(ctx, store.Event{
				Kind:     events.KindWorkerHeartbeat,
				EntityID: string(w.Role),
				Actor:    w.Name(),
				Payload:  map[string]any{"role": string(w.Role), "beat": beat},
			}); err != nil {
				return fmt.Errorf("recording heartbeat %d: %w", beat, err)
			}
			w.Log.Debug("heartbeat", "worker", w.Name(), "beat", beat)

			if w.Beats > 0 && beat >= w.Beats {
				return nil
			}
		}
	}
}
