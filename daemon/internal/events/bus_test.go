package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestPublisher(t *testing.T) (*store.Store, *Bus, *Publisher) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	bus := NewBus()
	return s, bus, NewPublisher(s, bus, quietLogger(), 0)
}

// collector is a concurrency-safe handler that records the topics it saw.
type collector struct {
	mu     sync.Mutex
	topics []string
}

func (c *collector) handle(_ context.Context, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topics = append(c.topics, m.Topic)
	return nil
}

func (c *collector) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.topics...)
}

// A topic subscriber sees its own topic and nothing else.
func TestBus_DeliversByTopic(t *testing.T) {
	ctx := context.Background()
	s, bus, pub := newTestPublisher(t)

	var funded, accepted collector
	bus.Subscribe("job.funded", funded.handle)
	bus.Subscribe("job.accepted", accepted.handle)

	for _, kind := range []string{"job.funded", "job.accepted", "job.funded"} {
		if _, err := s.Append(ctx, store.Event{Kind: kind, EntityID: "job_104"}); err != nil {
			t.Fatalf("Append %s: %v", kind, err)
		}
	}
	if _, err := pub.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if got := len(funded.seen()); got != 2 {
		t.Errorf("job.funded subscriber saw %d messages, want 2", got)
	}
	if got := len(accepted.seen()); got != 1 {
		t.Errorf("job.accepted subscriber saw %d messages, want 1", got)
	}
}

// SubscribeAll is what the audit log and dashboard SSE stream use (FR-AUD-001, FR-UI-006).
func TestBus_SubscribeAllSeesEveryTopic(t *testing.T) {
	ctx := context.Background()
	s, bus, pub := newTestPublisher(t)

	var all collector
	bus.SubscribeAll(all.handle)

	kinds := []string{"job.funded", "advance.issued", "payment.signed", "job.accepted"}
	for _, k := range kinds {
		if _, err := s.Append(ctx, store.Event{Kind: k}); err != nil {
			t.Fatalf("Append %s: %v", k, err)
		}
	}
	if _, err := pub.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	seen := all.seen()
	if len(seen) != len(kinds) {
		t.Fatalf("saw %d messages, want %d", len(seen), len(kinds))
	}
	// Order must match commit order — the audit log's ordering guarantee.
	for i, k := range kinds {
		if seen[i] != k {
			t.Errorf("message %d = %q, want %q", i, seen[i], k)
		}
	}
}

// Delivery is at-least-once: a handler that fails leaves the row unpublished so the
// next tick retries it. Losing it would break NFR-001's "no task event lost after commit."
func TestDrain_FailedHandlerLeavesRowForRetry(t *testing.T) {
	ctx := context.Background()
	s, bus, pub := newTestPublisher(t)

	var attempts int
	bus.Subscribe("job.funded", func(context.Context, Message) error {
		attempts++
		if attempts == 1 {
			return errors.New("subscriber not ready")
		}
		return nil
	})

	if _, err := s.Append(ctx, store.Event{Kind: "job.funded"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// First drain fails and publishes nothing.
	n, err := pub.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != 0 {
		t.Errorf("delivered %d on a failing handler, want 0", n)
	}
	rows, _ := s.Unpublished(ctx, 10)
	if len(rows) != 1 {
		t.Fatalf("row must remain unpublished for retry, backlog = %d", len(rows))
	}

	// Second drain succeeds.
	if n, err = pub.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("retry drain = (%d, %v), want (1, nil)", n, err)
	}
	rows, _ = s.Unpublished(ctx, 10)
	if len(rows) != 0 {
		t.Errorf("backlog should be empty after a successful retry, got %d", len(rows))
	}
}

// A failure must not let later events overtake the one that failed, or subscribers
// would observe events out of commit order.
func TestDrain_PreservesOrderAcrossFailure(t *testing.T) {
	ctx := context.Background()
	s, bus, pub := newTestPublisher(t)

	var got collector
	var fail = true
	bus.SubscribeAll(func(c context.Context, m Message) error {
		if fail && m.Topic == "job.funded" {
			return errors.New("transient")
		}
		return got.handle(c, m)
	})

	for _, k := range []string{"job.funded", "advance.issued"} {
		if _, err := s.Append(ctx, store.Event{Kind: k}); err != nil {
			t.Fatalf("Append %s: %v", k, err)
		}
	}

	if _, err := pub.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if seen := got.seen(); len(seen) != 0 {
		t.Fatalf("later event overtook the failed one: %v", seen)
	}

	fail = false
	if _, err := pub.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	seen := got.seen()
	if len(seen) != 2 || seen[0] != "job.funded" || seen[1] != "advance.issued" {
		t.Errorf("order after recovery = %v, want [job.funded advance.issued]", seen)
	}
}

// Draining an empty outbox is a no-op, not an error — it happens on every idle tick.
func TestDrain_EmptyOutboxIsNoOp(t *testing.T) {
	ctx := context.Background()
	_, _, pub := newTestPublisher(t)

	n, err := pub.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain on empty outbox: %v", err)
	}
	if n != 0 {
		t.Errorf("delivered %d from an empty outbox, want 0", n)
	}
}

// A published row is never redelivered on a later drain.
func TestDrain_DoesNotRedeliver(t *testing.T) {
	ctx := context.Background()
	s, bus, pub := newTestPublisher(t)

	var got collector
	bus.SubscribeAll(got.handle)

	if _, err := s.Append(ctx, store.Event{Kind: "job.accepted"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pub.Drain(ctx); err != nil {
			t.Fatalf("Drain %d: %v", i, err)
		}
	}
	if seen := got.seen(); len(seen) != 1 {
		t.Errorf("delivered %d times, want exactly 1", len(seen))
	}
}
