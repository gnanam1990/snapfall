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
	"fmt"
	"strings"
	"sync"

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
	// AlternativeTo links a replacement purchase to the approval request the owner
	// answered with request-alternative (G7's causal link, validated at intake — a bogus
	// value is refused with ErrBadAlternativeLink, same-job only). The activity feed
	// renders the rejection and the adaptation as ONE story through this.
	AlternativeTo string `json:"alternative_to,omitempty"`
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
	// RequestID names the approval request this outcome came from ("" when policy denied
	// outright — no request was opened). A worker links its replacement purchase's
	// AlternativeTo to THIS, making the adaptation causally traceable.
	RequestID string `json:"request_id,omitempty"`
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

	draft := stubDraft(a)

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// stubDraft is the deterministic demo draft shared by StubDD and AdaptiveDD: the FIRST
// draft always carries exactly one planted unsupported claim (the QA-bounce beat); a
// revision produces the corrected draft with every claim sourced.
func stubDraft(a Assignment) envelope.Deliverable {
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
		draft.Claims = append(draft.Claims, envelope.Claim{
			Text: "target's customer churn is 40% annually", Sources: nil,
		})
	} else {
		draft.Claims = append(draft.Claims, envelope.Claim{
			Text: "target's customer churn is 40% annually", Sources: []string{"filing:churn-2026"},
		})
		draft.Sources = append(draft.Sources, "filing:churn-2026")
	}
	return draft
}

// ── FR-CMP-001: the compliance seam ────────────────────────────────────────

// Compliance is the screening step's interface. THE LAW keeps this package import-free,
// so the seam is defined here and an implementation is injected at wiring: today the
// labeled stub below; the Circle Compliance Engine client when credentials exist.
type Compliance interface {
	Screen(ctx context.Context, subject string) (envelope.ComplianceResult, error)
}

// StubCompliance is the EXPLICITLY-LABELED stand-in until Circle credentials exist. It
// fabricates no screening result: Decision is "not-screened", Stub is true, the provider
// says "stub", and every surface renders it as such (QA attaches a visible note).
type StubCompliance struct{}

// Screen implements Compliance without pretending to screen.
func (StubCompliance) Screen(context.Context, string) (envelope.ComplianceResult, error) {
	return envelope.ComplianceResult{
		Decision:   "not-screened",
		Confidence: "low",
		Provider:   "stub",
		Stub:       true,
		Disclaimer: "evidence, not a guarantee",
	}, nil
}

// ── The G8 adaptive DD worker ──────────────────────────────────────────────

// SourceNeed is one data source the DD task wants, with its scripted cheaper fallback
// (Discovery/G10 will produce these; today the plan is provided at wiring).
type SourceNeed struct {
	Primary PurchaseRequest
	// Cheaper is proposed ONLY when the owner answers request-alternative with a reason
	// that asks for cost (see interpretReason). nil = nothing cheaper exists.
	Cheaper *PurchaseRequest
}

// AdaptiveDD is the real G8 DD worker: it buys its sources through the Brain-granted
// Purchase capability and ADAPTS on the structured decision reason (AT-04) — the
// adaptation is driven by what the reason SAYS, not by a fixed script; a different
// reason produces different behavior, and every adaptation carries AlternativeTo so the
// rejection and the replacement are one causal story. The bounce/revision QA beat is
// inherited from the shared stubDraft.
type AdaptiveDD struct {
	Compliance Compliance
	Needs      []SourceNeed
	// MaxAdaptations bounds alternatives per source need (0 = default 1): an owner who
	// keeps rejecting terminates the loop into an abandonment note, never a spin — the
	// same bound-and-stop ruling as the QA revision loop.
	MaxAdaptations int

	mu sync.Mutex
	// jobs keeps the per-job purchase results between the first draft and a QA revision,
	// so the revised report carries the same compliance screen and provenance.
	jobs map[string]*jobArtifacts
}

type jobArtifacts struct {
	compliance *envelope.ComplianceResult
	provenance []envelope.SourceProvenance
}

// NewAdaptiveDD builds the worker with its scripted source plan.
func NewAdaptiveDD(c Compliance, needs []SourceNeed, maxAdaptations int) *AdaptiveDD {
	if maxAdaptations <= 0 {
		maxAdaptations = 1
	}
	return &AdaptiveDD{Compliance: c, Needs: needs, MaxAdaptations: maxAdaptations, jobs: make(map[string]*jobArtifacts)}
}

// Kind implements Worker.
func (*AdaptiveDD) Kind() string { return "due-diligence" }

// adaptation classifies what the owner's structured reason asks for. Rule-based ON
// PURPOSE: deterministic for the demo; an LLM interpreter can replace interpretReason
// behind the same contract.
type adaptation int

const (
	adaptCheaper adaptation = iota // the reason asks about cost -> propose the cheaper source
	adaptAbandon                   // anything else -> do not buy a substitute; note and move on
)

func interpretReason(reason string) adaptation {
	r := strings.ToLower(reason)
	for _, kw := range []string{"cheaper", "expensive", "cost", "budget", "price"} {
		if strings.Contains(r, kw) {
			return adaptCheaper
		}
	}
	return adaptAbandon
}

// Handle implements Worker: acquire sources (adapting on structured reasons), run the
// compliance screen, and submit the draft with compliance + provenance attached.
func (w *AdaptiveDD) Handle(ctx context.Context, assignment envelope.Envelope, report Report, purchase Purchase) error {
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}
	jobID := assignment.JobID

	if len(a.BounceReasons) == 0 {
		// ── First pass: acquire sources, then screen. ──
		art := &jobArtifacts{}
		for _, need := range w.Needs {
			if err := w.acquire(ctx, jobID, need, report, purchase, art); err != nil {
				return err
			}
		}
		screen, err := w.Compliance.Screen(ctx, a.Scope)
		if err != nil {
			return fmt.Errorf("compliance screen: %w", err)
		}
		art.compliance = &screen
		w.mu.Lock()
		w.jobs[jobID] = art
		w.mu.Unlock()
	}

	w.mu.Lock()
	art := w.jobs[jobID]
	w.mu.Unlock()
	if art == nil {
		return fmt.Errorf("revision for %s but no first-pass artifacts exist", jobID)
	}

	draft := stubDraft(a)
	draft.Compliance = art.compliance
	draft.Provenance = art.provenance
	draft.Disclaimer = "This report is evidence of review, not a guarantee of correctness."

	final, err := envelope.New(jobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// acquire runs one source need through purchase-and-adapt to its terminal outcome.
func (w *AdaptiveDD) acquire(ctx context.Context, jobID string, need SourceNeed, report Report, purchase Purchase, art *jobArtifacts) error {
	req := need.Primary
	adaptations := 0
	for {
		out, err := purchase(ctx, req)
		if err != nil {
			return fmt.Errorf("source purchase: %w", err) // interrupted/unwired: fail loudly
		}
		if err := w.progress(ctx, jobID, report, map[string]any{
			"stage": "source-purchase", "resource": req.Resource,
			"decision": out.Decision, "status": out.Status, "reason": out.Reason, "code": out.Code,
			"alternative_to": req.AlternativeTo,
		}); err != nil {
			return err
		}

		switch {
		case out.Status == "delivered" || out.Status == "approved-pending-integration":
			entry := envelope.SourceProvenance{
				Resource: req.Resource, Merchant: req.Merchant,
				AmountAtomic: fmt.Sprintf("%d", req.AmountMicros),
				Status:       "pending-integration",
			}
			if out.Receipt != nil { // a real settled buy (post-F2)
				entry.ReceiptHash, entry.PaymentID, entry.Status = out.Receipt.ReceiptHash, out.Receipt.PaymentID, "proven"
			}
			art.provenance = append(art.provenance, entry)
			return nil

		case out.Code == "owner-alternative_requested" &&
			interpretReason(out.Reason) == adaptCheaper &&
			need.Cheaper != nil && adaptations < w.MaxAdaptations:
			// AT-04: the owner asked for an alternative ABOUT COST -> propose the cheaper
			// source, causally linked to the request the owner answered.
			adaptations++
			alt := *need.Cheaper
			alt.AlternativeTo = out.RequestID
			req = alt
			continue

		default:
			// A rejection whose reason does not ask for cost, a policy deny, an expiry, or
			// an exhausted adaptation bound: do NOT buy a substitute. Note it and move on.
			return w.progress(ctx, jobID, report, map[string]any{
				"stage": "source-abandoned", "resource": req.Resource,
				"why": out.Reason, "code": out.Code, "adaptations_used": adaptations,
			})
		}
	}
}

func (w *AdaptiveDD) progress(ctx context.Context, jobID string, report Report, payload map[string]any) error {
	e, err := envelope.New(jobID, envelope.RoleWorker, envelope.TypeWorkerProgress, payload)
	if err != nil {
		return err
	}
	return report(ctx, e)
}
