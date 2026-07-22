// The G7 approval lifecycle: request → decision → execution, with the decision bound to
// the exact intent hash, expiry on an injected clock, idempotent decisions, and nonce
// replay closed at intake and at execution.
package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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

	mu       sync.Mutex
	requests map[string]*Request
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
	cfg, version := l.Policy()
	in.PolicyVersion = version
	d := policy.Evaluate(cfg, l.Spend(in.JobID), policy.PaymentIntent{
		IntentID: in.IntentID, OrgID: in.OrgID, JobID: in.JobID, TaskID: in.TaskID,
		AgentID: in.AgentID, Merchant: in.Merchant, Resource: in.Resource,
		AmountMicros: in.AmountMicros, Purpose: in.Purpose, Nonce: in.Nonce,
		PolicyVersion: version,
	})

	l.appendEvent(ctx, in.JobID, "policy.evaluated", map[string]any{
		"intent_id": in.IntentID, "outcome": string(d.Outcome), "reason": d.Reason,
	})

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
	}

	l.mu.Lock()
	l.requests[req.ID] = req
	l.mu.Unlock()

	l.appendEvent(ctx, in.JobID, "approval.requested", map[string]any{
		"request_id": req.ID, "intent_hash": hash, "state": string(req.State),
	})
	return SubmitResult{Decision: d, Request: req}, nil
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
		return req, nil
	}
	if req.State.terminal() {
		return nil, fmt.Errorf("%w: request is %s", ErrAlreadyDecided, req.State)
	}

	// Expiry beats any decision on a pending request (SEC-006).
	if !l.clock().Before(req.ExpiresAt) {
		req.State = StateExpired
		req.DecidedAt = l.clock()
		req.Reason = "expired before decision"
		l.appendEvent(ctx, req.JobID, "approval.expired", map[string]any{"request_id": req.ID})
		return req, ErrExpired
	}

	req.State = target
	req.DecidedBy = by
	req.Reason = reason
	req.DecidedAt = l.clock()

	l.appendEvent(ctx, req.JobID, "approval."+string(kind), map[string]any{
		"request_id": req.ID, "by": by, "reason": reason,
	})
	return req, nil
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

	// Write-ahead: claim execution before performing it.
	req.Executed = true
	req.ExecutedAt = l.clock()
	l.mu.Unlock()

	l.appendEvent(ctx, req.JobID, "payment.executing", map[string]any{
		"request_id": req.ID, "intent_hash": req.IntentHash,
	})

	grant := Grant{intent: in, requestID: req.ID, grantedAt: req.ExecutedAt}
	if err := exec(ctx, grant); err != nil {
		// The claim stands (no retry without a fresh intent) — mirrors the sidecar's
		// conservative posture: never risk a double-spend to save a retry.
		l.appendEvent(ctx, req.JobID, "payment.failed", map[string]any{
			"request_id": req.ID, "error": err.Error(),
		})
		return fmt.Errorf("execution failed (intent is consumed; submit a fresh one): %w", err)
	}

	l.appendEvent(ctx, req.JobID, "payment.executed", map[string]any{
		"request_id": req.ID, "intent_hash": req.IntentHash,
	})
	return nil
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

func (l *Lifecycle) appendEvent(ctx context.Context, jobID, kind string, payload map[string]any) {
	// Best-effort audit append; the store is the same WAL SQLite the rest of the
	// daemon writes, and Append is transactional (event + outbox together).
	_, _ = l.st.Append(ctx, store.Event{Kind: kind, EntityID: jobID, Actor: "approval", Payload: payload})
}
