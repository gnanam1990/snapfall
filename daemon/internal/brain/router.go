// Package brain is the hub of the loop (G3, PRD §3, FR-BRN-001..004).
//
// One loop, one law: every message is Agent → Brain. Brain is the ONLY package that
// holds references to Workers, the Funding agent, and the owner surface — the spokes
// never hold references to each other. The routing table below is the complete set of
// message flows that exist in the system; a flow not in this table is not a flow.
package brain

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/logging"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// handler processes one routed envelope inside Brain.
type handler func(ctx context.Context, e envelope.Envelope) error

// routeKey is what the routing table is keyed by: who is speaking, and what they said.
type routeKey struct {
	From envelope.Role
	Type envelope.Type
}

// Brain routes every message and owns all spoke references.
type Brain struct {
	log     *slog.Logger
	store   *store.Store
	memory  *MemoryStore
	funding *funding.Agent // held by Brain alone; no other package sees this pointer

	mu      sync.Mutex
	workers map[string]worker.Worker
	routes  map[routeKey]handler
	jobs    map[string]*jobState
	scoper  Scoper
}

// New wires a Brain. The funding agent pointer is handed here and nowhere else.
func New(log *slog.Logger, st *store.Store, mem *MemoryStore, fund *funding.Agent) *Brain {
	b := &Brain{
		log:     log,
		store:   st,
		memory:  mem,
		funding: fund,
		workers: make(map[string]worker.Worker),
		jobs:    make(map[string]*jobState),
	}

	// THE routing table — the complete set of flows in the system (G3).
	// Owner→Brain and Worker→Brain are the only inbound edges; everything else
	// (Brain→Worker assignment, Brain→Funding instruction) is an action Brain
	// takes as a CONSEQUENCE of routing, not an edge someone else can invoke.
	b.routes = map[routeKey]handler{
		{envelope.RoleOwner, envelope.TypeOwnerRequest}: b.onOwnerRequest,
		{envelope.RoleOwner, envelope.TypeOwnerConfirm}: b.onOwnerConfirm,
		{envelope.RoleOwner, envelope.TypeOwnerReject}:  b.onOwnerReject,

		{envelope.RoleWorker, envelope.TypeWorkerProgress}: b.onWorkerProgress,
		{envelope.RoleWorker, envelope.TypeWorkerReport}:   b.onWorkerReport,
		{envelope.RoleWorker, envelope.TypeWorkerFailure}:  b.onWorkerFailure,
	}
	return b
}

// RegisterWorker plugs a worker slot in. Workers are registered BY KIND; Brain picks
// which kind serves a job — a Worker never chooses its own work (PRD §3).
func (b *Brain) RegisterWorker(w worker.Worker) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.workers[w.Kind()]; dup {
		return fmt.Errorf("worker kind %q already registered", w.Kind())
	}
	b.workers[w.Kind()] = w
	return nil
}

// Deliver routes one envelope. Every delivery is appended to the event log first —
// the log is the source of truth Brain replays from (G2, AT-10).
func (b *Brain) Deliver(ctx context.Context, e envelope.Envelope) error {
	ctx = logging.WithJob(ctx, e.JobID)
	log := logging.From(ctx, b.log)

	if _, err := b.store.Append(ctx, store.Event{
		Kind:     "brain.msg." + string(e.Type),
		EntityID: e.JobID,
		Actor:    string(e.From),
		Payload:  e,
	}); err != nil {
		return fmt.Errorf("recording %s from %s: %w", e.Type, e.From, err)
	}

	h, ok := b.routes[routeKey{e.From, e.Type}]
	if !ok {
		log.Warn("no route", "from", string(e.From), "type", string(e.Type))
		return fmt.Errorf("no route for %s from %s", e.Type, e.From)
	}
	log.Debug("routing", "from", string(e.From), "type", string(e.Type))
	return h(ctx, e)
}

// workerReport builds the single outbound capability a Worker receives (worker.Report).
// It pins From to RoleWorker regardless of what the worker put in the envelope, so a
// compromised worker cannot impersonate the owner to reach an owner-only route.
func (b *Brain) workerReport() worker.Report {
	return func(ctx context.Context, e envelope.Envelope) error {
		e.From = envelope.RoleWorker
		return b.Deliver(ctx, e)
	}
}

// assign dispatches a job's scope to the registered worker of the given kind.
// This is Brain acting, not an inbound route — there is no envelope a spoke could
// send that lands here.
func (b *Brain) assign(ctx context.Context, jobID, kind string) error {
	b.mu.Lock()
	w, ok := b.workers[kind]
	js := b.jobs[jobID]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("no worker registered for kind %q", kind)
	}
	if js == nil {
		return fmt.Errorf("unknown job %s", jobID)
	}

	assignment, err := envelope.New(jobID, envelope.RoleBrain, envelope.TypeAssignment,
		worker.Assignment{Scope: js.Scope})
	if err != nil {
		return err
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: "brain.msg." + string(envelope.TypeAssignment), EntityID: jobID,
		Actor: string(envelope.RoleBrain), Payload: assignment,
	}); err != nil {
		return err
	}
	if err := b.memory.SetAssignedWorker(jobID, kind); err != nil {
		return err
	}

	// Synchronous in Phase 1: the worker runs inline and reports through the one
	// callback it is handed. Phase 2 moves this onto the supervisor.
	return w.Handle(ctx, assignment, b.workerReport())
}
