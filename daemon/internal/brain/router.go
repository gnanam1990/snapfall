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
	// qaKind is the registered QA worker's kind ("" = no QA slot; Phase-1 flow).
	qaKind string
	// maxRevisions bounds the QA bounce loop (G9 pin 2). Exhausted -> escalate to owner.
	maxRevisions int
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

	b.maxRevisions = 2

	// THE routing table — owner-inbound flows (G3). Worker-inbound flows are NOT here:
	// they arrive only through the kind-stamped callback a worker was handed at
	// assignment (deliverFromWorker), so Brain always knows WHICH worker kind is
	// speaking — a stamp the worker cannot forge, because the closure applies it.
	b.routes = map[routeKey]handler{
		{envelope.RoleOwner, envelope.TypeOwnerRequest}: b.onOwnerRequest,
		{envelope.RoleOwner, envelope.TypeOwnerConfirm}: b.onOwnerConfirm,
		{envelope.RoleOwner, envelope.TypeOwnerReject}:  b.onOwnerReject,
	}
	return b
}

// RegisterQAWorker plugs the QA slot in and activates the G9 review loop: from now on
// every author draft is routed through QA before DeliveryReady (FR-QA-001).
func (b *Brain) RegisterQAWorker(w worker.Worker) error {
	if err := b.RegisterWorker(w); err != nil {
		return err
	}
	b.mu.Lock()
	b.qaKind = w.Kind()
	b.mu.Unlock()
	return nil
}

// SetMaxRevisions bounds the bounce loop (default 2).
func (b *Brain) SetMaxRevisions(n int) { b.mu.Lock(); b.maxRevisions = n; b.mu.Unlock() }

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

// workerReportFor builds the single outbound capability a Worker receives. It pins
// From to RoleWorker AND stamps the worker KIND Brain assigned — both applied by the
// closure, neither forgeable by the worker. The kind stamp is what makes "only the
// registered QA worker can issue a verdict" a structural property (G9 pin 1).
func (b *Brain) workerReportFor(kind string) worker.Report {
	return func(ctx context.Context, e envelope.Envelope) error {
		e.From = envelope.RoleWorker
		return b.deliverFromWorker(ctx, kind, e)
	}
}

// deliverFromWorker records and dispatches one worker message with its brain-stamped
// kind. The complete set of worker-inbound flows is this switch.
func (b *Brain) deliverFromWorker(ctx context.Context, kind string, e envelope.Envelope) error {
	ctx = logging.WithJob(ctx, e.JobID)

	if _, err := b.store.Append(ctx, store.Event{
		Kind:     "brain.msg." + string(e.Type),
		EntityID: e.JobID,
		Actor:    string(envelope.RoleWorker) + ":" + kind,
		Payload:  e,
	}); err != nil {
		return fmt.Errorf("recording %s from %s: %w", e.Type, kind, err)
	}

	switch e.Type {
	case envelope.TypeWorkerProgress:
		return b.onWorkerProgress(ctx, e)
	case envelope.TypeWorkerReport:
		return b.onWorkerReport(ctx, kind, e)
	case envelope.TypeQAVerdict:
		return b.onQAVerdict(ctx, kind, e)
	case envelope.TypeWorkerFailure:
		return b.onWorkerFailure(ctx, e)
	default:
		logging.From(ctx, b.log).Warn("no worker route", "kind", kind, "type", string(e.Type))
		return fmt.Errorf("no route for %s from worker kind %s", e.Type, kind)
	}
}

// assign dispatches an assignment to the registered worker of the given kind.
// bounceReasons and draft parameterize the G9 loop: a revision carries the QA
// reasons back to the author; a QA assignment carries the draft under review.
// This is Brain acting, not an inbound route — there is no envelope a spoke could
// send that lands here.
func (b *Brain) assign(ctx context.Context, jobID, kind string, bounceReasons []string, draft *envelope.Deliverable) error {
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
		worker.Assignment{Scope: js.Scope, BounceReasons: bounceReasons, Draft: draft})
	if err != nil {
		return err
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: "brain.msg." + string(envelope.TypeAssignment), EntityID: jobID,
		Actor: string(envelope.RoleBrain), Payload: assignment,
	}); err != nil {
		return err
	}
	if draft == nil {
		if err := b.memory.SetAssignedWorker(jobID, kind); err != nil {
			return err
		}
	}

	// Synchronous in Phase 1/2: the worker runs inline and reports through the one
	// kind-stamped callback it is handed.
	return w.Handle(ctx, assignment, b.workerReportFor(kind))
}
