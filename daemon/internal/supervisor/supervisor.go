// Package supervisor starts, watches, and restarts agent workers (PRD §6.3, NFR-001).
//
// NFR-001: "Supervisor recovers crashed workers." A worker that returns an error is restarted
// with exponential backoff; a worker that returns nil has finished its work and is left alone.
// Cancellation of the parent context stops everything and is not treated as a crash.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// State is a worker's lifecycle state (PRD §6.6 agent state machine, FR-ORG-004).
type State string

const (
	StateIdle     State = "idle"
	StateRunning  State = "running"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
	StateFrozen   State = "frozen"
	StateComplete State = "complete"
)

// Worker is anything the supervisor can run. Run must return promptly when ctx is cancelled.
type Worker interface {
	Name() string
	Run(ctx context.Context) error
}

// Status is a point-in-time health snapshot (FR-ORG-004, NFR-010).
type Status struct {
	Name     string
	State    State
	Restarts int
	LastErr  string
	Since    time.Time
}

// Supervisor owns a set of workers.
//
// Workers are either *essential* or infrastructure. Essential workers are the reason the
// daemon is running; infrastructure workers (the outbox publisher, later the indexer) exist
// to serve them and run forever by design. When every essential worker has reached a terminal
// state the supervisor cancels its run context, so infrastructure unwinds instead of pinning
// the process open. A daemon with no essential workers registered runs until interrupted.
type Supervisor struct {
	log         *slog.Logger
	mu          sync.RWMutex
	workers     []Worker
	essential   map[string]bool
	remaining   int
	status      map[string]*Status
	maxRestarts int
	baseBackoff time.Duration
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// New returns a supervisor. maxRestarts of 0 means unlimited; the demo uses a bound so a
// hard-broken worker surfaces instead of restart-looping behind the dashboard.
func New(log *slog.Logger, maxRestarts int, baseBackoff time.Duration) *Supervisor {
	if baseBackoff <= 0 {
		baseBackoff = 200 * time.Millisecond
	}
	return &Supervisor{
		log:         log,
		essential:   make(map[string]bool),
		status:      make(map[string]*Status),
		maxRestarts: maxRestarts,
		baseBackoff: baseBackoff,
	}
}

// Register adds an infrastructure worker. Must be called before Start.
func (s *Supervisor) Register(w Worker) error { return s.register(w, false) }

// RegisterEssential adds a worker whose completion counts toward shutdown.
func (s *Supervisor) RegisterEssential(w Worker) error { return s.register(w, true) }

func (s *Supervisor) register(w Worker, essential bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.status[w.Name()]; dup {
		return fmt.Errorf("worker %q already registered", w.Name())
	}
	s.workers = append(s.workers, w)
	s.status[w.Name()] = &Status{Name: w.Name(), State: StateIdle, Since: time.Now()}
	if essential {
		s.essential[w.Name()] = true
		s.remaining++
	}
	return nil
}

// Start launches every registered worker and returns immediately.
func (s *Supervisor) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.cancel = cancel
	workers := append([]Worker{}, s.workers...)
	s.mu.Unlock()

	for _, w := range workers {
		s.wg.Add(1)
		go s.supervise(runCtx, w)
	}
}

// retire marks an essential worker finished and shuts the tree down once none remain.
func (s *Supervisor) retire(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.essential[name] {
		return
	}
	s.essential[name] = false
	s.remaining--
	if s.remaining <= 0 && s.cancel != nil {
		s.log.Info("all essential workers finished, stopping supervisor")
		s.cancel()
	}
}

// Wait blocks until every worker has exited.
func (s *Supervisor) Wait() { s.wg.Wait() }

// supervise runs one worker, restarting it on failure with exponential backoff.
func (s *Supervisor) supervise(ctx context.Context, w Worker) {
	defer s.wg.Done()
	name := w.Name()
	defer s.retire(name)

	for attempt := 0; ; attempt++ {
		s.setState(name, StateRunning, nil)
		err := w.Run(ctx)

		// A cancelled parent context is a clean shutdown, not a crash.
		if ctx.Err() != nil {
			s.setState(name, StateStopped, nil)
			s.log.Info("worker stopped", "worker", name)
			return
		}
		if err == nil {
			s.setState(name, StateComplete, nil)
			s.log.Info("worker finished", "worker", name)
			return
		}

		s.setState(name, StateFailed, err)
		s.bumpRestarts(name)
		s.log.Error("worker crashed", "worker", name, "attempt", attempt+1, "err", err)

		if s.maxRestarts > 0 && attempt+1 >= s.maxRestarts {
			s.log.Error("worker exceeded restart budget, giving up", "worker", name, "restarts", attempt+1)
			return
		}

		// Exponential backoff, capped, so a persistently failing worker does not spin.
		backoff := s.baseBackoff << min(attempt, 6)
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			s.setState(name, StateStopped, nil)
			return
		case <-time.After(backoff):
		}
	}
}

func (s *Supervisor) setState(name string, st State, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.status[name]; ok {
		cur.State = st
		cur.Since = time.Now()
		if err != nil {
			cur.LastErr = err.Error()
		}
	}
}

func (s *Supervisor) bumpRestarts(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.status[name]; ok {
		cur.Restarts++
	}
}

// Health returns a snapshot of every worker's status (NFR-010).
func (s *Supervisor) Health() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Status, 0, len(s.status))
	for _, st := range s.status {
		out = append(out, *st)
	}
	return out
}

// ErrWorkerStopped is returned by workers that exit because their context ended.
var ErrWorkerStopped = errors.New("worker stopped")
