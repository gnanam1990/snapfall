// Package worker defines the Worker slot (G3, PRD §3).
//
// THE LAW, expressed as an import graph: this package imports internal/envelope and the
// standard library — NOTHING else. There is no reference to funding, billing, the store,
// the signer, or the owner surface anywhere in this package or its dependencies. A Worker
// cannot call what it cannot name: the Worker→Funding channel is not blocked, it is absent.
// AT-16 (brain/at16_law_test.go) asserts this property over the compiled import graph, so
// adding such an import is a failing test, not a code review hope.
//
// A Worker's entire capability surface is:
//   - receive exactly one assignment envelope from Brain
//   - do its bounded work
//   - report back to Brain through the single Report callback it was handed
package worker

import (
	"context"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// Report is the ONLY outbound channel a Worker has for saying things. Brain constructs it;
// it delivers to Brain and nowhere else. A worker holding this can say things TO BRAIN.
type Report func(ctx context.Context, e envelope.Envelope) error

// Purchase is the Brain-mediated capability to SPEND — the mirror of Report. Brain binds
// the job and the worker's identity in the closure (never supplied by the worker), routes
// the request through the deterministic policy+approval pipeline, and returns the
// structured decision INTACT. A worker holding this can propose a spend TO BRAIN; it never
// touches keys and cannot spend against another job's budget (there is no jobID to supply).
type Purchase func(ctx context.Context, req PurchaseRequest) (PurchaseOutcome, error)

// PurchaseRequest is a worker's structured proposal to buy a source. Note the ABSENCE of a
// jobID: the worker cannot target another job — Brain stamps that in the closure.
type PurchaseRequest struct {
	Merchant        string `json:"merchant"`
	Resource        string `json:"resource"`
	AmountMicros    int64  `json:"amount_micros"`
	MaxAmountMicros int64  `json:"max_amount_micros"`
	Purpose         string `json:"purpose"`
}

// PurchaseOutcome is the structured result. The policy decision REASON flows back intact
// (FR-BLK-001) so the worker can ADAPT (AT-04): "denied, find cheaper" vs "needs approval"
// vs "expired" are distinguishable, never one opaque error.
type PurchaseOutcome struct {
	Decision string `json:"decision"` // AUTO_APPROVE | HUMAN_APPROVAL_REQUIRED | DENY
	Reason   string `json:"reason"`   // human-readable structured reason
	Code     string `json:"code"`     // machine-readable reason code (e.g. per-tx-limit)
	// Status is the execution outcome. Until the sidecar client (F2) + merchant identity
	// (F4) land, an APPROVED purchase returns "approved-pending-integration" with no data
	// and no receipt — NEVER a fabricated buy.
	Status  string           `json:"status"` // delivered | approved-pending-integration | denied | needs-approval | expired
	Data    []byte           `json:"data,omitempty"`
	Receipt *PurchaseReceipt `json:"receipt,omitempty"`
}

// PurchaseReceipt is the provenance one real purchase leaves — folded into the report so
// Billing joins EXACTLY on ReceiptHash (JobVault.recordExpense) rather than jobId+amount.
type PurchaseReceipt struct {
	Merchant     string `json:"merchant"`
	AmountAtomic string `json:"amount_atomic"`
	ReceiptHash  string `json:"receipt_hash"` // bytes32 0x-hex
	PaymentID    string `json:"payment_id"`
}

// Worker executes exactly one assignment and reports back.
type Worker interface {
	// Kind names the worker slot, e.g. "due-diligence".
	Kind() string
	// Handle runs one assignment. It says things through report and spends through
	// purchase — the only two ways it can affect the world, both Brain-mediated.
	Handle(ctx context.Context, assignment envelope.Envelope, report Report, purchase Purchase) error
}

// ── The Phase-1 stub DD worker ─────────────────────────────────────────────

// Assignment is the payload Brain sends with TypeAssignment.
type Assignment struct {
	Scope string `json:"scope"`
	// BounceReasons is non-empty when this assignment is a REVISION: QA bounced the
	// prior draft and these are its reasons (G9). Deterministic workers key their
	// revision behavior off this.
	BounceReasons []string `json:"bounce_reasons,omitempty"`
	// Draft carries the deliverable under review when the assignee is the QA worker.
	Draft *envelope.Deliverable `json:"draft,omitempty"`
}

// StubDD is the scripted due-diligence worker for the Sun-26 exit gate. Real source
// purchases (via PaymentIntents through Brain) and the compliance step arrive in G8;
// the loop it exercises is already the real one.
type StubDD struct{}

// Kind implements Worker.
func (StubDD) Kind() string { return "due-diligence" }

// Handle produces a draft for the assigned scope: one progress update, then the draft.
//
// SCRIPTED-MODE DETERMINISM (the demo's QA beat): the FIRST draft always contains
// exactly one planted unsupported claim; a revision assignment (BounceReasons set)
// always produces the corrected draft with every claim sourced. Rule-based reviewer +
// scripted revision = the bounce happens exactly once, on every take, no LLM variance.
func (StubDD) Handle(ctx context.Context, assignment envelope.Envelope, report Report, _ Purchase) error {
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}

	progress, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{"stage": "researching", "completion_pct": 50})
	if err != nil {
		return err
	}
	if err := report(ctx, progress); err != nil {
		return err
	}

	draft := envelope.Deliverable{
		Title:   "Due-diligence report",
		Summary: "Stub due-diligence report for: " + a.Scope,
		Claims: []envelope.Claim{
			{Text: "corporate registry entry located", Sources: []string{"registry:acme-2201"}},
			{Text: "no adverse media found", Sources: []string{"media-scan:2026-07"}},
			{Text: "beneficial ownership chain resolved", Sources: []string{"registry:acme-2201", "filing:bo-114"}},
		},
		Sources: []string{"registry:acme-2201", "media-scan:2026-07", "filing:bo-114"},
	}
	if len(a.BounceReasons) == 0 {
		// First draft: the PLANTED unsupported claim (the demo's QA-bounce beat).
		draft.Claims = append(draft.Claims, envelope.Claim{
			Text: "target's customer churn is 40% annually", Sources: nil,
		})
	} else {
		// Revision: the claim returns with its source attached.
		draft.Claims = append(draft.Claims, envelope.Claim{
			Text: "target's customer churn is 40% annually", Sources: []string{"filing:churn-2026"},
		})
		draft.Sources = append(draft.Sources, "filing:churn-2026")
	}

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, final)
}
