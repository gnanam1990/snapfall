// Package events is the daemon's typed event bus and outbox publisher (PRD §6.2, §8.5).
//
// Delivery is at-least-once: the publisher marks an outbox row published only after every
// subscriber accepted it. Handlers must therefore be idempotent — the same guarantee AT-10
// (restart recovery) depends on, where "no completed payment or advance repeats" is enforced
// by idempotency keys downstream, not by the bus pretending to be exactly-once.
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Kinds from the PRD §8.5 taxonomy. Not exhaustive — added as workstreams need them.
const (
	KindDaemonStarted   = "daemon.started"
	KindWorkerStarted   = "agent.started"
	KindWorkerHeartbeat = "agent.heartbeat"
	KindWorkerFailed    = "agent.failed"
	KindWorkerStopped   = "agent.stopped"
)

// Message is one delivered event.
type Message struct {
	Topic   string
	Payload []byte
}

// Handler consumes a message. Returning an error leaves the outbox row unpublished
// so it is retried on the next tick.
type Handler func(context.Context, Message) error

// Bus fans messages out to subscribers by topic.
type Bus struct {
	mu   sync.RWMutex
	subs map[string][]Handler
	all  []Handler
}

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[string][]Handler)}
}

// Subscribe registers h for exactly one topic.
func (b *Bus) Subscribe(topic string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[topic] = append(b.subs[topic], h)
}

// SubscribeAll registers h for every topic — used by the audit log and the dashboard stream.
func (b *Bus) SubscribeAll(h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.all = append(b.all, h)
}

// dispatch delivers to every matching handler, returning the first error.
func (b *Bus) dispatch(ctx context.Context, m Message) error {
	b.mu.RLock()
	handlers := append(append([]Handler{}, b.subs[m.Topic]...), b.all...)
	b.mu.RUnlock()

	for _, h := range handlers {
		if err := h(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// Publisher drains the transactional outbox onto the bus.
type Publisher struct {
	store    *store.Store
	bus      *Bus
	log      *slog.Logger
	interval time.Duration
	batch    int
}

// NewPublisher wires a store to a bus. interval of 0 defaults to 100ms, which keeps the
// dashboard inside NFR-002's 2-second update budget with room to spare.
func NewPublisher(s *store.Store, b *Bus, log *slog.Logger, interval time.Duration) *Publisher {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	return &Publisher{store: s, bus: b, log: log, interval: interval, batch: 64}
}

// Drain publishes every currently-unpublished row once. Returns how many were delivered.
// Exposed separately from Run so tests can step the publisher deterministically.
func (p *Publisher) Drain(ctx context.Context) (int, error) {
	rows, err := p.store.Unpublished(ctx, p.batch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if err := p.bus.dispatch(ctx, Message{Topic: r.Topic, Payload: r.Payload}); err != nil {
			// Leave it unpublished; the next tick retries. Ordering is preserved because
			// we stop at the first failure rather than skipping ahead.
			p.log.Warn("outbox delivery failed, will retry", "id", r.ID, "topic", r.Topic, "err", err)
			return n, nil
		}
		if err := p.store.MarkPublished(ctx, r.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Run drains on a ticker until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := p.Drain(ctx); err != nil {
				p.log.Error("outbox drain failed", "err", err)
			}
		}
	}
}
