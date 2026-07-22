// Owner chat surface (G5, FR-BRN-001).
//
// Brain is the only agent the owner talks to. The flow this file encodes IS the
// discipline: scope → present quote → RECORD the confirmation → only then assign.
// There is no code path from an unconfirmed job to a worker assignment.
//
// The scoper is an interface with a stub implementation this phase — the state
// machine and the recording are the point, not language understanding (G5 brief).
package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/logging"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// JobStage is Brain's job lifecycle for Phase 1.
type JobStage string

const (
	StageScoped    JobStage = "scoped"    // scope proposed, awaiting owner confirmation
	StageConfirmed JobStage = "confirmed" // owner confirmed; assignment may proceed
	StageAssigned  JobStage = "assigned"  // worker has the job
	StageComplete  JobStage = "complete"  // worker reported; report recorded (no-QA flow)
	StageRejected  JobStage = "rejected"  // owner declined the scope
	StageFailed    JobStage = "failed"    // worker failed

	// ── G9 stages. StageDeliveryReady is reachable ONLY through a passing QA verdict
	//    (single assignment site, pinned by TestG9_DeliveryReadyHasOneAssignmentSite). ──
	StageQAReview      JobStage = "qa_review"      // draft with the QA worker
	StageRevision      JobStage = "revision"       // QA bounced; author is revising
	StageDeliveryReady JobStage = "delivery_ready" // QA passed; deliverable may go out
	StageEscalated     JobStage = "escalated"      // revisions exhausted; owner decides
)

// jobState is Brain's in-memory view of one job. The event log and the memory file
// are the durable truth; this is the router's working copy.
type jobState struct {
	JobID     string
	Scope     string
	QuoteUSDC string
	Stage     JobStage
	Worker    string
	Report    string
	// G9 loop state.
	RevisionCount int
	Draft         *envelope.Deliverable
}

// Scoper turns an owner request into a scope + quote. StubScoper this phase; a real
// LLM-backed scoper later, behind the same interface.
type Scoper interface {
	Scope(ctx context.Context, request string) (scope, quoteUSDC string, workerKind string, err error)
}

// StubScoper produces a deterministic scope for the exit-gate DD job.
type StubScoper struct{}

// Scope implements Scoper with a canned due-diligence framing.
func (StubScoper) Scope(_ context.Context, request string) (string, string, string, error) {
	if request == "" {
		return "", "", "", fmt.Errorf("empty request")
	}
	scope := "Due-diligence report: " + request
	return scope, "25.00", "due-diligence", nil
}

// SetScoper installs the scoper. Must be called before owner requests arrive.
func (b *Brain) SetScoper(s Scoper) { b.scoper = s }

// ── Owner-side API ─────────────────────────────────────────────────────────
//
// These construct envelopes with From pinned to RoleOwner — the owner surface is a
// capability the daemon wires to the actual human channel, exactly as workerReport
// pins RoleWorker. Nothing else can speak as the owner.

// OwnerRequest is the payload of TypeOwnerRequest.
type OwnerRequest struct {
	Request string `json:"request"`
}

// OwnerDecision is the payload of TypeOwnerConfirm / TypeOwnerReject.
type OwnerDecision struct {
	By     string `json:"by"`
	Reason string `json:"reason,omitempty"`
}

// ScopeProposal is what Brain presents back to the owner for confirmation.
type ScopeProposal struct {
	JobID     string `json:"job_id"`
	Scope     string `json:"scope"`
	QuoteUSDC string `json:"quote_usdc"`
}

// HandleOwnerRequest is the chat entry point: owner text in, scope proposal out.
func (b *Brain) HandleOwnerRequest(ctx context.Context, jobID, request string) (ScopeProposal, error) {
	e, err := envelope.New(jobID, envelope.RoleOwner, envelope.TypeOwnerRequest, OwnerRequest{Request: request})
	if err != nil {
		return ScopeProposal{}, err
	}
	if err := b.Deliver(ctx, e); err != nil {
		return ScopeProposal{}, err
	}
	b.mu.Lock()
	js := b.jobs[jobID]
	b.mu.Unlock()
	return ScopeProposal{JobID: jobID, Scope: js.Scope, QuoteUSDC: js.QuoteUSDC}, nil
}

// Confirm records the owner's confirmation and triggers assignment.
func (b *Brain) Confirm(ctx context.Context, jobID, by string) error {
	e, err := envelope.New(jobID, envelope.RoleOwner, envelope.TypeOwnerConfirm, OwnerDecision{By: by})
	if err != nil {
		return err
	}
	return b.Deliver(ctx, e)
}

// Reject records the owner declining the scope; the job goes no further.
func (b *Brain) Reject(ctx context.Context, jobID, by, reason string) error {
	e, err := envelope.New(jobID, envelope.RoleOwner, envelope.TypeOwnerReject, OwnerDecision{By: by, Reason: reason})
	if err != nil {
		return err
	}
	return b.Deliver(ctx, e)
}

// Job returns Brain's current view of a job.
func (b *Brain) Job(jobID string) (jobState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	js, ok := b.jobs[jobID]
	if !ok {
		return jobState{}, false
	}
	return *js, true
}

// ── Route handlers ─────────────────────────────────────────────────────────

func (b *Brain) onOwnerRequest(ctx context.Context, e envelope.Envelope) error {
	if b.scoper == nil {
		return fmt.Errorf("no scoper installed")
	}
	var req OwnerRequest
	if err := e.Decode(&req); err != nil {
		return err
	}

	b.mu.Lock()
	if _, dup := b.jobs[e.JobID]; dup {
		b.mu.Unlock()
		return fmt.Errorf("job %s already exists", e.JobID)
	}
	b.mu.Unlock()

	scope, quote, kind, err := b.scoper.Scope(ctx, req.Request)
	if err != nil {
		return fmt.Errorf("scoping job %s: %w", e.JobID, err)
	}

	b.mu.Lock()
	b.jobs[e.JobID] = &jobState{JobID: e.JobID, Scope: scope, QuoteUSDC: quote, Stage: StageScoped, Worker: kind}
	b.mu.Unlock()

	if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
		jm.Scope = scope
		jm.QuoteUSDC = quote
		jm.Stage = string(StageScoped)
		jm.EscrowState = "none"
	}); err != nil {
		return err
	}

	// Present the proposal back to the owner — recorded as an event like everything else.
	proposal, err := envelope.New(e.JobID, envelope.RoleBrain, envelope.TypeScopeProposal,
		ScopeProposal{JobID: e.JobID, Scope: scope, QuoteUSDC: quote})
	if err != nil {
		return err
	}
	_, err = b.store.Append(ctx, store.Event{
		Kind: "brain.msg." + string(envelope.TypeScopeProposal), EntityID: e.JobID,
		Actor: string(envelope.RoleBrain), Payload: proposal,
	})
	return err
}

func (b *Brain) onOwnerConfirm(ctx context.Context, e envelope.Envelope) error {
	var d OwnerDecision
	if err := e.Decode(&d); err != nil {
		return err
	}
	if d.By == "" {
		return fmt.Errorf("confirmation for %s names no owner", e.JobID)
	}

	b.mu.Lock()
	js, ok := b.jobs[e.JobID]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("confirm for unknown job %s", e.JobID)
	}
	if js.Stage != StageScoped {
		stage := js.Stage
		b.mu.Unlock()
		return fmt.Errorf("job %s is %s, only a scoped job can be confirmed", e.JobID, stage)
	}
	js.Stage = StageConfirmed
	kind := js.Worker
	b.mu.Unlock()

	// THE DISCIPLINE: the confirmation is durably recorded BEFORE any assignment.
	if err := b.memory.AddConfirmation(e.JobID, d.By, "scope+quote confirmed"); err != nil {
		return err
	}
	if err := b.memory.Update(e.JobID, func(jm *JobMemory) { jm.Stage = string(StageConfirmed) }); err != nil {
		return err
	}

	logging.From(ctx, b.log).Info("owner confirmed, assigning", "worker_kind", kind)

	b.mu.Lock()
	js.Stage = StageAssigned
	b.mu.Unlock()
	return b.assign(ctx, e.JobID, kind, nil, nil)
}

func (b *Brain) onOwnerReject(ctx context.Context, e envelope.Envelope) error {
	var d OwnerDecision
	if err := e.Decode(&d); err != nil {
		return err
	}
	b.mu.Lock()
	js, ok := b.jobs[e.JobID]
	if ok {
		js.Stage = StageRejected
	}
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("reject for unknown job %s", e.JobID)
	}
	if err := b.memory.AddConfirmation(e.JobID, d.By, "scope rejected: "+d.Reason); err != nil {
		return err
	}
	return b.memory.Update(e.JobID, func(jm *JobMemory) { jm.Stage = string(StageRejected) })
}

func (b *Brain) onWorkerProgress(ctx context.Context, e envelope.Envelope) error {
	var p struct {
		Stage         string `json:"stage"`
		CompletionPct int    `json:"completion_pct"`
	}
	if err := e.Decode(&p); err != nil {
		return err
	}
	return b.memory.Update(e.JobID, func(jm *JobMemory) {
		jm.Stage = p.Stage
		jm.CompletionPct = p.CompletionPct
	})
}

func (b *Brain) onWorkerReport(ctx context.Context, kind string, e envelope.Envelope) error {
	b.mu.Lock()
	js, ok := b.jobs[e.JobID]
	qaKind := b.qaKind
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("report for unknown job %s", e.JobID)
	}

	// ── G9: with a QA slot registered, an author draft NEVER completes the job
	//    directly — it goes to review. There is no branch from here to
	//    StageDeliveryReady or StageComplete when QA is active. ──
	if qaKind != "" {
		var draft envelope.Deliverable
		if err := e.Decode(&draft); err != nil {
			return fmt.Errorf("author report for %s is not a deliverable: %w", e.JobID, err)
		}
		b.mu.Lock()
		js.Draft = &draft
		js.Stage = StageQAReview
		b.mu.Unlock()
		if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
			jm.Stage = string(StageQAReview)
		}); err != nil {
			return err
		}
		return b.assign(ctx, e.JobID, qaKind, nil, &draft)
	}

	// Phase-1 flow (no QA slot): report completes the job.
	b.mu.Lock()
	js.Stage = StageComplete
	js.Report = string(e.Payload)
	b.mu.Unlock()

	if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
		jm.Stage = string(StageComplete)
		jm.CompletionPct = 100
		jm.Report = string(e.Payload)
	}); err != nil {
		return err
	}

	// Relay to the owner as a job report — recorded, like every message.
	report, err := envelope.New(e.JobID, envelope.RoleBrain, envelope.TypeJobReport,
		map[string]any{"report": string(e.Payload)})
	if err != nil {
		return err
	}
	_, err = b.store.Append(ctx, store.Event{
		Kind: "brain.msg." + string(envelope.TypeJobReport), EntityID: e.JobID,
		Actor: string(envelope.RoleBrain), Payload: report,
	})
	return err
}

// onQAVerdict is the ONLY code path to StageDeliveryReady (G9 pin 1).
//
// Guards, in order: the speaker's brain-stamped kind must be the registered QA worker
// (a DD worker forging TypeQAVerdict is refused — the stamp comes from the assignment
// closure, not the envelope); the job must actually be in qa_review; then pass ->
// delivery-ready, fail -> bounded revision loop -> escalation.
func (b *Brain) onQAVerdict(ctx context.Context, kind string, e envelope.Envelope) error {
	b.mu.Lock()
	js, ok := b.jobs[e.JobID]
	qaKind := b.qaKind
	maxRev := b.maxRevisions
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("verdict for unknown job %s", e.JobID)
	}
	if kind == "" || kind != qaKind {
		return fmt.Errorf("verdict refused: worker kind %q is not the registered QA reviewer %q — only QA issues verdicts", kind, qaKind)
	}
	if js.Stage != StageQAReview {
		return fmt.Errorf("verdict refused: job %s is %s, not under QA review", e.JobID, js.Stage)
	}

	var v envelope.QAVerdict
	if err := e.Decode(&v); err != nil {
		return err
	}
	if v.Disclaimer == "" {
		// Honesty is part of the contract (G9 pin 3): a verdict that presents itself
		// as a guarantee is malformed.
		return fmt.Errorf("verdict refused: missing the evidence-not-guarantee disclaimer")
	}

	note := "QA PASS"
	if !v.Passed {
		note = "QA BOUNCE: " + strings.Join(v.Reasons, " | ")
	}
	if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
		jm.QANotes = append(jm.QANotes, note)
		jm.QADisclaimer = v.Disclaimer
	}); err != nil {
		return err
	}

	if v.Passed {
		return b.markDeliveryReady(ctx, js, v)
	}

	// ── The bounce loop, bounded (G9 pin 2). ──
	b.mu.Lock()
	js.RevisionCount++
	rev := js.RevisionCount
	author := js.Worker
	b.mu.Unlock()

	if rev > maxRev {
		b.mu.Lock()
		js.Stage = StageEscalated
		b.mu.Unlock()
		if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
			jm.Stage = string(StageEscalated)
			jm.RevisionCount = rev
		}); err != nil {
			return err
		}
		// Loudly to the owner — an invisible loop is worse than a loud failure.
		esc, err := envelope.New(e.JobID, envelope.RoleBrain, envelope.TypeJobUpdate, map[string]any{
			"escalation": fmt.Sprintf("QA could not pass the deliverable after %d revisions; owner decision required", maxRev),
			"qa_reasons": v.Reasons,
			"disclaimer": v.Disclaimer,
		})
		if err != nil {
			return err
		}
		_, err = b.store.Append(ctx, store.Event{
			Kind: "brain.msg." + string(envelope.TypeJobUpdate), EntityID: e.JobID,
			Actor: string(envelope.RoleBrain), Payload: esc,
		})
		return err
	}

	b.mu.Lock()
	js.Stage = StageRevision
	b.mu.Unlock()
	if err := b.memory.Update(e.JobID, func(jm *JobMemory) {
		jm.Stage = string(StageRevision)
		jm.RevisionCount = rev
	}); err != nil {
		return err
	}
	// Bounce back to the AUTHOR with the reasons — never silently (FR-QA-001).
	return b.assign(ctx, e.JobID, author, v.Reasons, nil)
}

// markDeliveryReady is the single assignment site for StageDeliveryReady — called
// exclusively from onQAVerdict's pass branch, pinned by a source-scan test.
func (b *Brain) markDeliveryReady(ctx context.Context, js *jobState, v envelope.QAVerdict) error {
	b.mu.Lock()
	js.Stage = StageDeliveryReady
	draft := js.Draft
	b.mu.Unlock()

	raw := ""
	if draft != nil {
		if enc, err := json.Marshal(draft); err == nil {
			raw = string(enc)
		}
	}
	if err := b.memory.Update(js.JobID, func(jm *JobMemory) {
		jm.Stage = string(StageDeliveryReady)
		jm.CompletionPct = 100
		jm.Report = raw
	}); err != nil {
		return err
	}

	report, err := envelope.New(js.JobID, envelope.RoleBrain, envelope.TypeJobReport, map[string]any{
		"report":     raw,
		"qa":         "passed",
		"disclaimer": v.Disclaimer,
	})
	if err != nil {
		return err
	}
	_, err = b.store.Append(ctx, store.Event{
		Kind: "brain.msg." + string(envelope.TypeJobReport), EntityID: js.JobID,
		Actor: string(envelope.RoleBrain), Payload: report,
	})
	return err
}

func (b *Brain) onWorkerFailure(ctx context.Context, e envelope.Envelope) error {
	b.mu.Lock()
	if js, ok := b.jobs[e.JobID]; ok {
		js.Stage = StageFailed
	}
	b.mu.Unlock()
	return b.memory.Update(e.JobID, func(jm *JobMemory) { jm.Stage = string(StageFailed) })
}
