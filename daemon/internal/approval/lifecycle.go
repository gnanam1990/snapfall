// The G7 approval lifecycle: request → decision → execution, with the decision bound to
// the exact intent hash, expiry on an injected clock, idempotent decisions, and nonce
// replay closed at intake and at execution.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Clock is injected time (Step-3 pin 2): expiry tests are deterministic, never sleep-based.
type Clock func() time.Time

// State is an approval request's lifecycle state.
type State string

const (
	StatePending              State = "pending"
	StateApproved             State = "approved"
	StateRejected             State = "rejected"
	StateAlternativeRequested State = "alternative_requested"
	StateExpired              State = "expired"
)

// terminal states admit no further decisions.
func (s State) terminal() bool { return s != StatePending }

// DecisionKind is what the owner (or policy engine) decided.
type DecisionKind string

const (
	DecideApprove            DecisionKind = "approve"
	DecideReject             DecisionKind = "reject"
	DecideRequestAlternative DecisionKind = "request_alternative"
)

// Sentinel errors — the machine-readable refusal vocabulary (FR-PAY-004 spirit).
var (
	ErrNonceReplayed      = errors.New("nonce-replayed: this nonce has already been submitted")
	ErrUnknownRequest     = errors.New("unknown-request: no approval request with that ID")
	ErrNotApproved        = errors.New("not-approved: no valid approval exists for this intent")
	ErrAlreadyDecided     = errors.New("already-decided: request is terminal; a conflicting decision is refused")
	ErrExpired            = errors.New("approval-expired: the approval window has elapsed (SEC-006)")
	ErrHashMismatch       = errors.New("intent-hash-mismatch: intent differs from the one approved (AT-05); new approval required")
	ErrPolicyChanged      = errors.New("policy-changed: policy version changed since approval (SEC-006); re-evaluation required")
	ErrAlreadyExecuted    = errors.New("already-executed: this intent has been executed; replay refused")
	ErrBadAlternativeLink = errors.New("bad-alternative-link: AlternativeTo must reference an alternative_requested decision on the same job")
	ErrDenied             = errors.New("denied: policy denied this intent; no approval path exists")
)

// Request is one approval request, bound to the intent hash at creation.
type Request struct {
	ID         string
	JobID      string
	IntentHash string
	Intent     Intent
	State      State
	DecidedBy  string
	Reason     string
	CreatedAt  time.Time
	DecidedAt  time.Time
	ExpiresAt  time.Time
	Executed   bool
	ExecutedAt time.Time
	// decided is closed exactly when the request leaves Pending (a human decision, or
	// expiry marked in Decide). An async waiter (the Purchaser, on HumanApprovalRequired)
	// selects on it; nil for non-pending or recovered requests (DecisionSignal returns a
	// closed channel then, so a waiter never blocks forever). Not serialized.
	decided chan struct{}
}

// DecisionSignal returns a channel closed when the request leaves Pending. A blocked
// Purchase selects on this to wake the instant the owner decides — never a poll. Unknown
// or already-terminal requests return an already-closed channel, so a waiter cannot hang.
func (l *Lifecycle) DecisionSignal(requestID string) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if req, ok := l.requests[requestID]; ok && req.decided != nil && req.State == StatePending {
		return req.decided
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Snapshot returns a copy of the request for reading its terminal state and reason — the
// structured decision the Purchaser maps back to the worker. Never the live pointer.
func (l *Lifecycle) Snapshot(requestID string) (Request, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	req, ok := l.requests[requestID]
	if !ok {
		return Request{}, false
	}
	return *req, true
}

// closeDecided closes the pending-decision signal exactly once (caller holds l.mu).
func closeDecided(req *Request) {
	if req.decided != nil {
		close(req.decided)
		req.decided = nil
	}
}

// Grant is the capability Execute mints when — and only when — every gate has passed:
// hash bound, state approved, unexpired, policy version current, never executed.
//
// Every field is unexported. A Grant forged outside this package (`approval.Grant{}`)
// is EMPTY: it names no amount, no merchant, no job — there is no money movement it
// could describe. The data needed to act can only enter a Grant here, post-gate. Any
// Funding-side entry point that demands a Grant therefore cannot be reached with a bare
// policy.Decision, an expired approval, or nothing at all. This is the execution-side
// twin of AT-16: the unsafe call is unexpressible, not merely checked.
type Grant struct {
	intent    Intent
	requestID string
	grantedAt time.Time
}

// Intent returns the exact intent that was approved and re-verified.
func (g Grant) Intent() Intent { return g.intent }

// RequestID names the approval record this grant was minted from.
func (g Grant) RequestID() string { return g.requestID }

// GrantedAt is when the execution gate passed.
func (g Grant) GrantedAt() time.Time { return g.grantedAt }

// Empty reports whether this grant was forged outside the lifecycle (zero value).
func (g Grant) Empty() bool { return g.requestID == "" }

// There is deliberately NO exported constructor for a populated Grant. A Grant with
// real payment data can be minted ONLY inside Execute, after every gate. Tests that
// need a legitimate Grant (including tests in other packages) obtain one by driving a
// real approved lifecycle flow and capturing what the Executor receives — never a
// production-reachable shortcut, which would be a second door past the gates.

// Executor performs the approved action — for the demo spine, the Funding-agent call.
// It receives an approval-minted Grant, never a bare Intent or policy Decision.
type Executor func(ctx context.Context, g Grant) error

// Lifecycle wires policy evaluation, approval state, and execution binding together.
type Lifecycle struct {
	st    *store.Store
	clock Clock
	// Policy returns the active config and its version — read at intake AND at
	// execution, so a version bump between the two invalidates (SEC-006).
	Policy func() (policy.PolicyConfig, string)
	// Spend returns the current spend state for a job (caller contract: the daily
	// window is the UTC calendar day, policy.DailyWindowStartUTC).
	Spend func(jobID string) policy.SpendState
	// Freeze is the G11 kill-switch registry. Optional (nil = ungated); the daemon
	// always wires it. Gates: Submit before the nonce claim, Execute before the
	// write-ahead claim and Grant minting.
	Freeze *freeze.Registry

	mu       sync.Mutex
	requests map[string]*Request
}

// frozenErr consults the kill switch for this intent's scopes (a non-authoritative
// fast-fail; the authoritative gate is admitExecution, which is atomic with Engage).
func (l *Lifecycle) frozenErr(in Intent) error {
	if l.Freeze == nil {
		return nil
	}
	if e := l.Freeze.Check(in.OrgID, in.JobID, in.AgentID); e != nil {
		return freeze.Err(e)
	}
	return nil
}

// admitExecution atomically gates AND counts this execution against the freeze registry.
// The in-flight count now lives in the registry (incremented under the same lock Engage
// snapshots), so freeze admission and the in-flight transition are one step w.r.t. a
// concurrent Engage — closing the gap where an admitted payment was reported as
// zero-in-flight (review fix, Anandan #4.1). With no registry wired, it is a no-op.
func (l *Lifecycle) admitExecution(in Intent) (func(), error) {
	if l.Freeze == nil {
		return func() {}, nil
	}
	return l.Freeze.AdmitExecution(in.OrgID, in.JobID, in.AgentID)
}

// New builds a lifecycle over the given store and clock.
func New(st *store.Store, clock Clock) *Lifecycle {
	return &Lifecycle{st: st, clock: clock, requests: make(map[string]*Request)}
}

// SubmitResult is what intake returns: the policy decision, and the approval request
// when one exists (auto-approved or pending; nil on deny).
type SubmitResult struct {
	Decision policy.Decision
	Request  *Request
}

// Submit is intake: claim the nonce, evaluate policy, and open the approval record.
//
// Nonce replay is closed HERE, before policy ever runs, and the claim is durable —
// the payment_intents.nonce UNIQUE constraint in the store is the authority, so a
// replay is refused even across a daemon restart.
func (l *Lifecycle) Submit(ctx context.Context, in Intent) (SubmitResult, error) {
	if l.Policy == nil || l.Spend == nil {
		return SubmitResult{}, fmt.Errorf("lifecycle not wired: Policy and Spend must be set")
	}

	// ── G11: the kill switch gates intake BEFORE the nonce claim, so a frozen-scope
	//    submission does not burn its nonce — the same intent submits cleanly after
	//    the freeze lifts (AT-09 "stops new claims"). ──
	if err := l.frozenErr(in); err != nil {
		return SubmitResult{}, err
	}

	// ── Resolve the active policy version BEFORE the nonce claim, so the durable
	//    payment_intents row records the version actually evaluated (review-batch fix:
	//    it previously recorded the caller-supplied/empty value). ──
	cfg, version := l.Policy()
	in.PolicyVersion = version

	// ── Nonce claim (durable, unique). ──
	if err := l.claimNonce(ctx, in); err != nil {
		return SubmitResult{}, err
	}

	// ── AT-04 provenance: an alternative must reference a real request-alternative
	//    decision on the same job. ──
	if in.AlternativeTo != "" {
		l.mu.Lock()
		orig, ok := l.requests[in.AlternativeTo]
		l.mu.Unlock()
		if !ok || orig.State != StateAlternativeRequested || orig.JobID != in.JobID {
			return SubmitResult{}, ErrBadAlternativeLink
		}
	}

	// ── Policy evaluation (G6, pure). ──
	d := policy.Evaluate(cfg, l.Spend(in.JobID), policy.PaymentIntent{
		IntentID: in.IntentID, OrgID: in.OrgID, JobID: in.JobID, TaskID: in.TaskID,
		AgentID: in.AgentID, Merchant: in.Merchant, Resource: in.Resource,
		AmountMicros: in.AmountMicros, Purpose: in.Purpose, Nonce: in.Nonce,
		PolicyVersion: version,
	})

	if err := l.appendEvent(ctx, in.JobID, "policy.evaluated", map[string]any{
		"intent_id": in.IntentID, "outcome": string(d.Outcome), "reason": d.Reason,
	}); err != nil {
		return SubmitResult{}, err
	}

	if d.Outcome == policy.Deny {
		return SubmitResult{Decision: d}, ErrDenied
	}

	// ── Open the request, bound to the FULL intent hash at this moment. ──
	hash := InternalHash(in)
	req := &Request{
		ID:         "apr_" + hash[2:14],
		JobID:      in.JobID,
		IntentHash: hash,
		Intent:     in,
		CreatedAt:  l.clock(),
		ExpiresAt:  in.ExpiresAt,
	}
	switch d.Outcome {
	case policy.AutoApprove:
		req.State = StateApproved
		req.DecidedBy = "policy-engine"
		req.DecidedAt = l.clock()
	case policy.HumanApprovalRequired:
		req.State = StatePending
		req.decided = make(chan struct{}) // an async Purchase waits on this
	}

	// Durable BEFORE visible: the approval.requested event is what recovery rebuilds
	// from, so it must land before the request enters the map (review-batch fix). The
	// full intent rides the event so a restart can re-verify the hash.
	if err := l.appendEvent(ctx, in.JobID, "approval.requested", map[string]any{
		"request_id": req.ID, "intent_hash": hash, "state": string(req.State),
		"intent": in, "decided_by": req.DecidedBy,
	}); err != nil {
		return SubmitResult{}, err
	}

	l.mu.Lock()
	l.requests[req.ID] = req
	l.mu.Unlock()

	// Return a COPY: mutating the returned struct must never move gate state
	// (review-batch fix; the map-owned request stays private).
	cp := *req
	return SubmitResult{Decision: d, Request: &cp}, nil
}

// Decide records a human decision. Idempotent: repeating the SAME decision is a
// recognized no-op; a CONFLICTING decision on a terminal request is refused.
func (l *Lifecycle) Decide(ctx context.Context, requestID string, kind DecisionKind, by, reason string) (*Request, error) {
	if by == "" {
		return nil, fmt.Errorf("decision on %s names no decider", requestID)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	req, ok := l.requests[requestID]
	if !ok {
		return nil, ErrUnknownRequest
	}

	target := map[DecisionKind]State{
		DecideApprove:            StateApproved,
		DecideRequestAlternative: StateAlternativeRequested,
		DecideReject:             StateRejected,
	}[kind]
	if target == "" {
		return nil, fmt.Errorf("unknown decision kind %q", kind)
	}

	// Idempotency first: the same decision landing twice is one effect, not two —
	// and not an error, so a retried Telegram tap is harmless.
	if req.State == target && req.DecidedBy == by {
		cp := *req
		return &cp, nil
	}
	if req.State.terminal() {
		return nil, fmt.Errorf("%w: request is %s", ErrAlreadyDecided, req.State)
	}

	// Expiry beats any decision on a pending request (SEC-006).
	if !l.clock().Before(req.ExpiresAt) {
		req.State = StateExpired
		req.DecidedAt = l.clock()
		req.Reason = "expired before decision"
		closeDecided(req)
		if err := l.appendEvent(ctx, req.JobID, "approval.expired", map[string]any{"request_id": req.ID}); err != nil {
			return nil, err
		}
		cp := *req
		return &cp, ErrExpired
	}

	req.State = target
	req.DecidedBy = by
	req.Reason = reason
	req.DecidedAt = l.clock()
	closeDecided(req) // wake any Purchase blocked on this decision

	if err := l.appendEvent(ctx, req.JobID, "approval."+string(kind), map[string]any{
		"request_id": req.ID, "by": by, "reason": reason,
	}); err != nil {
		return nil, err
	}
	cp := *req
	return &cp, nil
}

// Execute runs the approved action — the ONLY path to an Executor.
//
// The binding is re-verified at execution time, in this order:
//  1. the request exists
//  2. the presented intent's FULL hash equals the hash bound at intake (AT-05)
//  3. the request is Approved (not pending/rejected/alternative/expired)
//  4. the approval has not expired on the injected clock (SEC-006)
//  5. the policy version is still the active one (SEC-006)
//  6. this request has never executed before (exactly-once)
//
// The executed flag is set BEFORE the executor runs (write-ahead, same posture as the
// H3 sidecar): a crash mid-execution recovers to "executed" and never double-spends.
func (l *Lifecycle) Execute(ctx context.Context, in Intent, requestID string, exec Executor) error {
	l.mu.Lock()

	req, ok := l.requests[requestID]
	if !ok {
		l.mu.Unlock()
		return ErrUnknownRequest
	}

	// ── G11: no new signature begins in a frozen scope. This sits BEFORE the
	//    write-ahead claim and the Grant minting: frozen scope -> no Grant -> no
	//    money (the funding door only opens for a Grant). An execution already past
	//    this line completes; see the freeze package doc. ──
	if err := l.frozenErr(req.Intent); err != nil {
		l.mu.Unlock()
		return err
	}

	if InternalHash(in) != req.IntentHash {
		l.mu.Unlock()
		return ErrHashMismatch
	}

	switch req.State {
	case StateApproved:
		// proceed
	case StatePending:
		l.mu.Unlock()
		return ErrNotApproved
	case StateExpired:
		l.mu.Unlock()
		return ErrExpired
	default: // rejected, alternative_requested
		l.mu.Unlock()
		return fmt.Errorf("%w: request is %s", ErrNotApproved, req.State)
	}

	if !l.clock().Before(req.ExpiresAt) {
		req.State = StateExpired
		closeDecided(req)
		l.mu.Unlock()
		return ErrExpired
	}

	_, activeVersion := l.Policy()
	if req.Intent.PolicyVersion != activeVersion {
		l.mu.Unlock()
		return fmt.Errorf("%w: approved under %s, active is %s", ErrPolicyChanged, req.Intent.PolicyVersion, activeVersion)
	}

	if req.Executed {
		l.mu.Unlock()
		return ErrAlreadyExecuted
	}

	// ── ATOMIC freeze admission + claim (review fix, Anandan #4.1). While still holding
	//    l.mu, gate on the kill switch AND count this execution in-flight in one step
	//    (the count lives in the registry, incremented under the lock Engage snapshots).
	//    So the freeze admission and the executed/in-flight transition are atomic w.r.t.
	//    a concurrent Engage: it either refuses this execution or reports it in-flight —
	//    never "nothing was in flight" while a payment proceeds. Frozen here -> no claim,
	//    intent NOT burned. ──
	release, err := l.admitExecution(req.Intent)
	if err != nil {
		l.mu.Unlock()
		return err
	}

	// Write-ahead: claim execution before performing it (atomic with the admission above).
	req.Executed = true
	req.ExecutedAt = l.clock()
	l.mu.Unlock()

	// The claim MUST be durable before the executor runs (review-batch fix): a
	// memory-only claim disappears in a crash and the payment would repeat. If the
	// append fails, un-claim, release the in-flight admission, and abort — the executor
	// has not run, so retrying later is safe. No durable claim, no execution.
	if err := l.appendEvent(ctx, req.JobID, "payment.executing", map[string]any{
		"request_id": req.ID, "intent_hash": req.IntentHash,
	}); err != nil {
		l.mu.Lock()
		req.Executed = false
		req.ExecutedAt = time.Time{}
		l.mu.Unlock()
		release()
		return fmt.Errorf("refusing to execute without a durable claim: %w", err)
	}

	// Release the in-flight admission when the executor returns; a freeze engaged in this
	// window is recorded with this execution counted, and the owner report says it completed.
	defer release()

	grant := Grant{intent: in, requestID: req.ID, grantedAt: req.ExecutedAt}
	if err := exec(ctx, grant); err != nil {
		// The claim stands (no retry without a fresh intent) — mirrors the sidecar's
		// conservative posture: never risk a double-spend to save a retry.
		_ = l.appendEvent(ctx, req.JobID, "payment.failed", map[string]any{
			"request_id": req.ID, "error": err.Error(),
		})
		return fmt.Errorf("execution failed (intent is consumed; submit a fresh one): %w", err)
	}

	if err := l.appendEvent(ctx, req.JobID, "payment.executed", map[string]any{
		"request_id": req.ID, "intent_hash": req.IntentHash,
	}); err != nil {
		// The money moved; the durable executing-claim protects against replay. Surface
		// the audit-gap loudly rather than swallowing it.
		return fmt.Errorf("execution COMPLETED but the executed-event append failed (claim is durable; no replay risk): %w", err)
	}
	return nil
}

// Recover rebuilds the request map from the event log (G11 / extended AT-10).
//
// Before this existed, a restart LOST approved-but-unexecuted requests while their
// nonces stayed burned — the intent could neither execute nor resubmit. Replay folds
// the approval events in sequence order:
//
//	approval.requested        -> request opened (full intent + initial state)
//	approval.approve/reject/
//	approval.request_alternative -> decision applied
//	approval.expired          -> expired
//	payment.executing         -> EXECUTED (the write-ahead claim IS the claim: a crash
//	                             after it must never re-execute, so replay honors it)
//
// Call once at boot, before serving. Idempotent over the same log.
func (l *Lifecycle) Recover(ctx context.Context) error {
	rows, err := l.st.DB().QueryContext(ctx, `SELECT kind, payload_json FROM events
		WHERE kind IN ('approval.requested','approval.approve','approval.reject',
		               'approval.request_alternative','approval.expired','payment.executing')
		ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("recovering approvals: %w", err)
	}
	defer rows.Close()

	l.mu.Lock()
	defer l.mu.Unlock()

	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			return err
		}
		var p struct {
			RequestID  string `json:"request_id"`
			IntentHash string `json:"intent_hash"`
			State      string `json:"state"`
			Intent     Intent `json:"intent"`
			DecidedBy  string `json:"decided_by"`
			By         string `json:"by"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return fmt.Errorf("corrupt approval event: %w", err)
		}
		if p.RequestID == "" {
			continue
		}

		switch kind {
		case "approval.requested":
			req := &Request{
				ID: p.RequestID, JobID: p.Intent.JobID, IntentHash: p.IntentHash,
				Intent: p.Intent, State: State(p.State), DecidedBy: p.DecidedBy,
				ExpiresAt: p.Intent.ExpiresAt,
			}
			l.requests[p.RequestID] = req
		case "approval.approve", "approval.reject", "approval.request_alternative":
			if req, ok := l.requests[p.RequestID]; ok {
				req.State = map[string]State{
					"approval.approve":             StateApproved,
					"approval.reject":              StateRejected,
					"approval.request_alternative": StateAlternativeRequested,
				}[kind]
				req.DecidedBy, req.Reason = p.By, p.Reason
			}
		case "approval.expired":
			if req, ok := l.requests[p.RequestID]; ok {
				req.State = StateExpired
			}
		case "payment.executing":
			if req, ok := l.requests[p.RequestID]; ok {
				req.Executed = true
			}
		}
	}
	return rows.Err()
}

// Request returns a snapshot of one request.
func (l *Lifecycle) Request(id string) (Request, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.requests[id]
	if !ok {
		return Request{}, false
	}
	return *r, true
}

// claimNonce durably claims the intent nonce via the payment_intents UNIQUE constraint.
func (l *Lifecycle) claimNonce(ctx context.Context, in Intent) error {
	_, err := l.st.DB().ExecContext(ctx,
		`INSERT INTO payment_intents
		   (id, job_id, task_id, agent_id, merchant, resource, amount_usdc, purpose,
		    nonce, expiry, policy_version, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'Proposed', ?)`,
		in.IntentID, in.JobID, in.TaskID, in.AgentID, in.Merchant, in.Resource,
		policy.FormatUSDC(in.AmountMicros), in.Purpose,
		in.Nonce, in.ExpiresAt.Unix(), in.PolicyVersion, l.clock().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return ErrNonceReplayed
		}
		return fmt.Errorf("claiming nonce: %w", err)
	}
	return nil
}

// appendEvent writes one audit event. NOT best-effort (review-batch fix): Recover()
// rebuilds all request state from these events, so a silently-lost append is lost
// recovery state — and for payment.executing, an open double-pay window. Callers on
// money-critical paths fail closed on error.
func (l *Lifecycle) appendEvent(ctx context.Context, jobID, kind string, payload map[string]any) error {
	_, err := l.st.Append(ctx, store.Event{Kind: kind, EntityID: jobID, Actor: "approval", Payload: payload})
	if err != nil {
		return fmt.Errorf("audit append %s: %w", kind, err)
	}
	return nil
}
