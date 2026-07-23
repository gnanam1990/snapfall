// Package purchasing is the concrete brain.Purchaser: it routes a worker's Brain-stamped
// purchase intent through the REAL policy + approval pipeline and returns the structured
// decision. It is the dependency the DD-worker's adaptation logic (AT-04) is tested against
// — a genuine policy decision, not a stub that always answers the same way.
//
// WHAT IS REAL here: the policy evaluation, the escalation to HumanApprovalRequired, the
// owner's real approve/reject decision (with its structured reason), the injected-clock
// expiry, and the freeze gate inside approval.Execute. WHAT IS STUBBED: only the executor
// body — the sidecar /v1/pay money movement (the F2 client). An approved purchase runs the
// full execution gates (freeze, expiry, exactly-once) and returns
// "approved-pending-integration" with no data and no receipt — never a fabricated buy.
package purchasing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// Clock is injected time with a DEADLINE notifier. The Purchaser's expiry wake fires from
// the SAME clock the approval lifecycle enforces expiry on — no second independent timeout.
// The real clock uses a timer; a fake clock (tests) releases deadlines on Advance.
type Clock interface {
	Now() time.Time
	// Deadline returns a channel that receives once the clock reaches t (or immediately if
	// t is already past). Used in a select so a blocked Purchase wakes at the expiry.
	Deadline(t time.Time) <-chan struct{}
}

// RealClock is the production Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) Deadline(t time.Time) <-chan struct{} {
	ch := make(chan struct{})
	d := time.Until(t)
	if d <= 0 {
		close(ch)
		return ch
	}
	time.AfterFunc(d, func() { close(ch) })
	return ch
}

// Purchaser implements brain.Purchaser over the real policy+approval pipeline.
type Purchaser struct {
	life   *approval.Lifecycle
	store  *store.Store
	clock  Clock
	orgID  string
	window time.Duration // how long a HumanApprovalRequired request stays open
	// afterSubmit is a TEST-ONLY hook (nil in production) invoked with the request ID right
	// after Submit, so a test can drive the owner's decision deterministically instead of
	// polling for the pending request. Unexported, no setter — inert in production.
	afterSubmit func(reqID string)
}

// New builds the Purchaser. window is the approval expiry (SEC-006).
func New(life *approval.Lifecycle, st *store.Store, clock Clock, orgID string, window time.Duration) *Purchaser {
	return &Purchaser{life: life, store: st, clock: clock, orgID: orgID, window: window}
}

// Decide runs one purchase through policy + approval and returns the structured outcome.
// It implements brain.Purchaser. jobID/agentKind on the intent were stamped by Brain.
func (p *Purchaser) Decide(ctx context.Context, in brain.PurchaseIntent) (worker.PurchaseOutcome, error) {
	nonce, err := freshNonce()
	if err != nil {
		return worker.PurchaseOutcome{}, err
	}
	now := p.clock.Now()
	intent := approval.Intent{
		IntentID:        "pi_" + nonce[2:14],
		OrgID:           p.orgID,
		JobID:           in.JobID,
		AgentID:         in.AgentKind,
		Merchant:        in.Merchant,
		Resource:        in.Resource,
		AmountMicros:    in.AmountMicros,
		MaxAmountMicros: in.MaxAmountMicros,
		Purpose:         in.Purpose,
		Nonce:           nonce,
		ExpiresAt:       now.Add(p.window),
		AlternativeTo:   in.AlternativeTo,
	}

	res, err := p.life.Submit(ctx, intent)
	if err != nil {
		if errors.Is(err, approval.ErrDenied) {
			return denyOutcome(res.Decision), nil
		}
		return worker.PurchaseOutcome{}, err
	}
	if res.Request != nil && p.afterSubmit != nil {
		p.afterSubmit(res.Request.ID)
	}

	switch res.Decision.Outcome {
	case policy.Deny:
		return denyOutcome(res.Decision), nil
	case policy.AutoApprove:
		// Execute against the BOUND intent (Submit set PolicyVersion on it) or the AT-05
		// hash check refuses it.
		return p.execute(ctx, res.Request.Intent, res.Request.ID, res.Decision)
	case policy.HumanApprovalRequired:
		return p.awaitAndExecute(ctx, res.Request.Intent, res.Request)
	default:
		return worker.PurchaseOutcome{}, fmt.Errorf("purchasing: unknown outcome %q", res.Decision.Outcome)
	}
}

// awaitAndExecute blocks for the owner's decision OR the approval expiry — whichever comes
// first — then acts. The expiry wake is the SAME ExpiresAt approval enforces, driven by the
// injected clock: no second timeout, and the goroutine always terminates.
func (p *Purchaser) awaitAndExecute(ctx context.Context, intent approval.Intent, req *approval.Request) (worker.PurchaseOutcome, error) {
	select {
	case <-ctx.Done():
		// Shutdown (or caller cancellation) while awaiting the owner: safe interruption —
		// no claim exists yet; the task ends and restart escalates it (serve pin 3).
		return worker.PurchaseOutcome{}, fmt.Errorf("purchase interrupted awaiting the owner's decision: %w", ctx.Err())
	case <-p.life.DecisionSignal(req.ID):
		// The owner decided (or expiry was marked). Read the terminal state.
	case <-p.clock.Deadline(req.ExpiresAt):
		// The approval window elapsed on the injected clock — fall through to execute,
		// where the lifecycle's own expiry check returns ErrExpired (same clock, consistent).
	}

	snap, ok := p.life.Snapshot(req.ID)
	if !ok {
		return worker.PurchaseOutcome{}, approval.ErrUnknownRequest
	}
	switch snap.State {
	case approval.StateApproved:
		return p.execute(ctx, intent, req.ID, policy.Decision{Outcome: policy.HumanApprovalRequired})
	case approval.StateExpired:
		return expiredOutcome(), nil
	case approval.StateRejected, approval.StateAlternativeRequested:
		return worker.PurchaseOutcome{
			Decision: string(policy.Deny), Status: "denied",
			Reason:    nonEmpty(snap.Reason, "owner declined the purchase"),
			Code:      "owner-" + string(snap.State),
			RequestID: req.ID, // the anchor a linked alternative's AlternativeTo points at
		}, nil
	case approval.StatePending:
		// We only wake on a decision (which leaves Pending) or the deadline. Still Pending
		// here means the deadline fired: the approval window elapsed on the injected clock.
		// (An owner approval that raced past expiry would show StateApproved and be caught
		// by Execute's own expiry check — same clock, consistent.)
		return expiredOutcome(), nil
	default:
		return worker.PurchaseOutcome{}, fmt.Errorf("purchasing: unexpected state %q", snap.State)
	}
}

// execute runs the approval Execute gates — INCLUDING the freeze gate — and, on success,
// the F2-STUB executor: it moves no money and records the pending settlement honestly. A
// freeze that engaged since the policy decision is refused HERE by the existing gate.
func (p *Purchaser) execute(ctx context.Context, intent approval.Intent, reqID string, d policy.Decision) (worker.PurchaseOutcome, error) {
	// SHUTDOWN RULE (serve pin 2, the freeze in-flight ruling applied to SIGTERM):
	// cancellation is honored ONLY here, BEFORE the write-ahead claim — a safe refusal
	// that consumes nothing. Past this check, Execute runs under a shielded context:
	// cancellation can never abort between the claim and completion, because aborting
	// there recreates the exact double-pay hazard the in-flight ruling exists to avoid.
	if err := ctx.Err(); err != nil {
		return worker.PurchaseOutcome{}, fmt.Errorf("purchase interrupted before execution (no claim written): %w", err)
	}
	shielded := context.WithoutCancel(ctx)
	execErr := p.life.Execute(shielded, intent, reqID, func(ctx context.Context, g approval.Grant) error {
		// ── THE F2 SEAM ── the sidecar /v1/pay call lands here. Until it does, no money
		// moves; record the intent to settle so the log is honest, and return success so
		// the approval is consumed exactly once (the real payer replaces this body in F2).
		_, err := p.store.Append(ctx, store.Event{
			Kind: "purchase.pending_settlement", EntityID: g.RequestID(), Actor: "purchaser",
			Payload: map[string]any{
				"job_id": intent.JobID, "merchant": intent.Merchant,
				"amount_micros": intent.AmountMicros, "note": "approved; money movement pending F2 sidecar client",
			},
		})
		return err
	})
	switch {
	case execErr == nil:
		return worker.PurchaseOutcome{
			Decision: decisionLabel(d), Status: "approved-pending-integration",
			Reason:    "approved by policy/owner; money movement pending the F2 sidecar client",
			RequestID: reqID,
		}, nil
	case errors.Is(execErr, approval.ErrExpired):
		return expiredOutcome(), nil
	default:
		// A freeze engaging between decision and execution lands here (frozen: ... from the
		// gate), as does any other execution refusal — reported as denied, never executed.
		return worker.PurchaseOutcome{
			Decision: string(policy.Deny), Status: "denied", Code: "execution-refused",
			Reason: execErr.Error(),
		}, nil
	}
}

func denyOutcome(d policy.Decision) worker.PurchaseOutcome {
	out := worker.PurchaseOutcome{Decision: string(policy.Deny), Status: "denied"}
	if d.Reason != nil {
		out.Reason, out.Code = d.Reason.Message, d.Reason.Code
	}
	return out
}

func expiredOutcome() worker.PurchaseOutcome {
	return worker.PurchaseOutcome{
		Decision: string(policy.HumanApprovalRequired), Status: "expired", Code: "approval-expired",
		Reason: "the approval window elapsed before the owner decided",
	}
}

func decisionLabel(d policy.Decision) string {
	if d.Outcome == "" {
		return string(policy.AutoApprove)
	}
	return string(d.Outcome)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func freshNonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(b[:]), nil
}
