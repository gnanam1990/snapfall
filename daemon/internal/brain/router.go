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

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
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
	// freezeReg is the G11 kill switch (nil = ungated); orgID scopes org-level checks.
	freezeReg *freeze.Registry
	orgID     string
	// purchaser routes worker spend requests through policy+approval (nil = refused).
	purchaser Purchaser
	// billingAgent is the G12 read-side invoice formatter — held by Brain alone, invoked
	// from the single GenerateInvoice site. invoiceMu serializes version assignment.
	billingAgent *billing.Agent
	invoiceMu    sync.Mutex
	// advanceFlow is the human-authorized advance path (internal/advancing) — held by
	// Brain alone, invoked from the single ProposeAdvance site.
	advanceFlow *advancing.Flow
	// rootCtx, when set (the serving daemon), bounds every task goroutine's lifetime:
	// SIGTERM cancels it -> blocked tasks wake, new dispatches are refused. nil in
	// package tests/demos (tasks then detach from the request ctx, as before).
	rootCtx context.Context
	// recovered guards Recover() exactly-once per Brain (serve pin 1).
	recovered bool
	// beforeFreezeCheck is a TEST-ONLY hook (nil in production) invoked at worker-start,
	// immediately before the freeze gate — it lets a test engage a freeze deterministically
	// in the dispatch->start window to pin that "begins" means worker-start (decision #3).
	beforeFreezeCheck func()
	// tasks tracks the one background goroutine per dispatched job (G8 async assignment).
	// Assignment is no longer inline in the owner's Confirm() call — the worker runs on
	// its own goroutine so a blocked Purchase never holds the owner's call hostage, and a
	// single-threaded owner surface (Telegram/HTTP) cannot deadlock. Each handle carries a
	// done channel for a DETERMINISTIC completion signal (tests await it, never poll).
	tasks map[string]*taskHandle
}

// taskHandle is the per-job background dispatch: its done channel closes when the whole
// worker interaction (author -> QA loop -> terminal) has run to completion or been
// withheld, and err is the terminal outcome, set before done is closed.
type taskHandle struct {
	done chan struct{}
	err  error
}

// SetFreeze wires the kill-switch registry and the org identity it checks against.
func (b *Brain) SetFreeze(r *freeze.Registry, orgID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.freezeReg = r
	b.orgID = orgID
}

// frozenErr consults the kill switch for a job/agent in this org.
func (b *Brain) frozenErr(jobID, agentKind string) error {
	b.mu.Lock()
	r, org := b.freezeReg, b.orgID
	b.mu.Unlock()
	if r == nil {
		return nil
	}
	if e := r.Check(org, jobID, agentKind); e != nil {
		return freeze.Err(e)
	}
	return nil
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
		tasks:   make(map[string]*taskHandle),
	}

	b.maxRevisions = 2

	// Every memory write projects into the SQL jobs table (see project.go): the hook
	// makes projection a property of writing memory, not a call-site obligation.
	if mem != nil {
		mem.AfterUpdate = b.projectJob
	}

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

// Purchaser is Brain's handle to the deterministic policy+approval pipeline for worker
// purchase requests. The daemon wires a concrete one (backed by approval.Lifecycle);
// nil = purchases refused. Set via SetPurchaser, like the funding pointer and freeze
// registry — Brain holds it, workers never see it.
type Purchaser interface {
	// Decide runs policy + approval for one JOB-STAMPED intent and returns the structured
	// outcome. The jobID/agentKind on the intent are applied by Brain, not the worker.
	Decide(ctx context.Context, intent PurchaseIntent) (worker.PurchaseOutcome, error)
}

// PurchaseIntent is a worker's purchase request AFTER Brain has stamped the job and the
// worker's identity — the fields a worker cannot forge are set here by the closure.
type PurchaseIntent struct {
	JobID           string
	AgentKind       string
	Merchant        string
	Resource        string
	AmountMicros    int64
	MaxAmountMicros int64
	Purpose         string
	// AlternativeTo carries the worker's causal link to a request-alternative decision
	// (G7); intake validates it, so a forged value is refused, never trusted.
	AlternativeTo string
}

// SetPurchaser wires the policy+approval pipeline Brain routes worker spends through.
func (b *Brain) SetPurchaser(p Purchaser) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purchaser = p
}

// purchaseFor builds the SPEND capability a Worker receives — the mirror of workerReportFor.
// Brain BINDS the jobID and worker kind in this closure; the worker's PurchaseRequest has
// no jobID field, so a worker cannot spend against another job's budget or spend as another
// agent — the cross-job/cross-agent refusal is structural, not a runtime check.
func (b *Brain) purchaseFor(kind, jobID string) worker.Purchase {
	return func(ctx context.Context, req worker.PurchaseRequest) (worker.PurchaseOutcome, error) {
		b.mu.Lock()
		p := b.purchaser
		b.mu.Unlock()
		if p == nil {
			return worker.PurchaseOutcome{}, fmt.Errorf("no purchaser wired: worker %q cannot spend on job %s", kind, jobID)
		}
		return p.Decide(ctx, PurchaseIntent{
			JobID: jobID, AgentKind: kind,
			Merchant: req.Merchant, Resource: req.Resource,
			AmountMicros: req.AmountMicros, MaxAmountMicros: req.MaxAmountMicros,
			Purpose: req.Purpose, AlternativeTo: req.AlternativeTo,
		})
	}
}

// workerReportFor builds the single outbound capability a Worker receives. It pins
// From to RoleWorker AND stamps the worker KIND and JOB Brain assigned — all applied
// by the closure, none forgeable by the worker. The kind stamp makes "only the
// registered QA worker can issue a verdict" structural (G9 pin 1); the job stamp stops
// a worker reporting against a DIFFERENT job by swapping e.JobID (review-batch fix).
func (b *Brain) workerReportFor(kind, jobID string) worker.Report {
	return func(ctx context.Context, e envelope.Envelope) error {
		if e.JobID != jobID {
			return fmt.Errorf("worker %q reported job %q but was assigned job %q; cross-job report refused", kind, e.JobID, jobID)
		}
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

// bounceReasons and draft parameterize the G9 loop: a revision carries the QA reasons
// back to the author; a QA assignment carries the draft under review. This is Brain
// acting, not an inbound route — there is no envelope a spoke could send that lands here.
//
// dispatchTask is the ASYNC entry to a job's worker (G8): it launches runTask on a
// tracked background goroutine and returns immediately, so the owner's Confirm() call is
// never held for the job's duration. The freeze gate is NOT here — it is inside runTask,
// immediately before Handle, so "begins" means worker-start (decision #3): a freeze that
// engages in the dispatch->start window still stops the worker. Nested dispatches inside
// the QA loop (author<->QA) run INLINE via runTask on this same goroutine, so one handle
// covers the whole interaction and its done channel fires exactly at the terminal state.
func (b *Brain) dispatchTask(ctx context.Context, jobID, kind string, bounceReasons []string, draft *envelope.Deliverable) error {
	// SHUTDOWN RULE (serve step): once the daemon root context is cancelled, NO new task
	// dispatches — the same "stops new claims" posture as the freeze. In-flight tasks are
	// not aborted here; they complete or return via their own ctx (see taskContext).
	b.mu.Lock()
	root := b.rootCtx
	b.mu.Unlock()
	if root != nil && root.Err() != nil {
		return fmt.Errorf("dispatch refused: daemon is shutting down (%w)", root.Err())
	}

	h := &taskHandle{done: make(chan struct{})}
	b.mu.Lock()
	b.tasks[jobID] = h
	b.mu.Unlock()

	// The task context is decoupled from the caller's request (Confirm() returning must
	// not kill the task) but — when serving — BOUND to the daemon root, so SIGTERM stops
	// blocked work. Cancellation is never allowed past a payment's write-ahead claim; the
	// Purchaser shields approval.Execute (see purchasing.execute).
	taskCtx := b.taskContext(ctx)
	go func() {
		err := b.runTask(taskCtx, jobID, kind, bounceReasons, draft)
		b.mu.Lock()
		h.err = err
		b.mu.Unlock()
		close(h.done)
	}()
	return nil
}

// taskContext derives the context a task goroutine runs under. With a root context set
// (the serving daemon), tasks are children of the DAEMON's lifetime — cancelled on
// SIGTERM — not of the owner's request. Without one (package tests, demos), the prior
// behavior holds: detached from the request, never cancelled.
func (b *Brain) taskContext(req context.Context) context.Context {
	b.mu.Lock()
	root := b.rootCtx
	b.mu.Unlock()
	if root != nil {
		return root
	}
	return context.WithoutCancel(req)
}

// SetRootContext binds task goroutines to the daemon's lifetime (the serve step): on
// SIGTERM, blocked tasks wake and return, new dispatches are refused, and in-flight
// payment executions still complete past their claim (the shutdown analogue of the
// freeze in-flight ruling). Call once at serve time, before any dispatch.
func (b *Brain) SetRootContext(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rootCtx = ctx
}

// WaitTasks blocks until every currently-tracked task goroutine has finished — the
// shutdown drain. Blocked tasks are woken by root-context cancellation (they return
// interrupted); an Execute past its claim completes under the Purchaser's shield, so
// this wait is bounded by real work, not by an owner who never answers.
func (b *Brain) WaitTasks() {
	b.mu.Lock()
	handles := make([]*taskHandle, 0, len(b.tasks))
	for _, h := range b.tasks {
		handles = append(handles, h)
	}
	b.mu.Unlock()
	for _, h := range handles {
		<-h.done
	}
}

// AwaitTask blocks until the job's dispatched task reaches its terminal state and returns
// its outcome. This is the DETERMINISTIC completion signal — closed by the goroutine's
// completion, never a wall-clock poll (decision #2). A job never dispatched returns nil.
func (b *Brain) AwaitTask(jobID string) error {
	b.mu.Lock()
	h := b.tasks[jobID]
	b.mu.Unlock()
	if h == nil {
		return nil
	}
	<-h.done
	b.mu.Lock()
	defer b.mu.Unlock()
	return h.err
}

// runTask is THE dispatch chokepoint: the freeze gate and the sole worker-invocation
// site live here (pinned by TestAT09_DispatchChokepointIsSingle). It runs one invocation
// inline; the worker's report callbacks may re-enter runTask (QA loop) synchronously on
// the same goroutine. Called on the background goroutine by dispatchTask (first dispatch)
// and inline by the QA-loop handlers (re-assignments).
func (b *Brain) runTask(ctx context.Context, jobID, kind string, bounceReasons []string, draft *envelope.Deliverable) error {
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

	// ── G11 + G8: THE task chokepoint, checked at WORKER-START. With async dispatch,
	//    "begins" means here — not when dispatchTask launched the goroutine — so a freeze
	//    that engages in the dispatch->start window still stops the worker (decision #3).
	//    One gate stops all new tasks; withheld dispatches are recorded (fail loudly). ──
	if b.beforeFreezeCheck != nil {
		b.beforeFreezeCheck() // test-only hook: engage a freeze IN the dispatch->start window
	}
	if err := b.frozenErr(jobID, kind); err != nil {
		_, _ = b.store.Append(ctx, store.Event{
			Kind: "task.withheld", EntityID: jobID, Actor: "brain",
			Payload: map[string]any{"worker_kind": kind, "reason": err.Error()},
		})
		return err
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
	return w.Handle(ctx, assignment, b.workerReportFor(kind, jobID), b.purchaseFor(kind, jobID))
}
