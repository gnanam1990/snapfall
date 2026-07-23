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

// ── The G8/G10 adaptive DD worker ──────────────────────────────────────────

// Found is one discovery suggestion as the worker consumes it: everything a
// PurchaseRequest needs and NOTHING more — no job, no approval state, no authority.
// The worker OWNS this type (AT-16 stays maximal: worker imports only envelope);
// internal/discovery implements the seam through an adapter at the wiring layer, so
// the two packages never import each other.
type Found struct {
	Merchant     string
	Resource     string
	Description  string
	AmountMicros int64
	Score        float64
}

// Finder is the G10 discovery seam. maxAmountMicros > 0 caps the results (the cheaper
// re-query after a cost rejection). An EMPTY result is a first-class honest outcome —
// the need lands on the source-abandoned path, never an error and never a fallback to
// the least-irrelevant service.
type Finder interface {
	Find(ctx context.Context, need string, maxAmountMicros int64) ([]Found, error)
}

// AdaptiveDD is the DD worker: it DISCOVERS its sources by description (G10 — no
// merchant or resource name appears in a need), buys them through the Brain-granted
// Purchase capability, and ADAPTS on the structured decision reason (AT-04): a cost
// reason triggers a capped re-query of discovery for a cheaper source, causally linked
// via AlternativeTo; any other reason abandons the source. There is NO scripted source
// plan — the discovery path is the only path (the demo cannot record anything else).
type AdaptiveDD struct {
	Compliance Compliance
	finder     Finder
	// needs is a SLICE in demo-script order — profile before market — because the
	// purchase sequence IS part of the demo (beats 0:45 and 1:10). A map here would
	// let Go's iteration order swap beats between takes.
	needs []string
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

// NewDiscoveryDD builds the worker with its discovery seam and ordered need texts.
func NewDiscoveryDD(c Compliance, f Finder, needs []string, maxAdaptations int) *AdaptiveDD {
	if maxAdaptations <= 0 {
		maxAdaptations = 1
	}
	return &AdaptiveDD{Compliance: c, finder: f, needs: needs, MaxAdaptations: maxAdaptations, jobs: make(map[string]*jobArtifacts)}
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
		// ── First pass: discover and acquire each need IN SLICE ORDER, then screen. ──
		art := &jobArtifacts{}
		for _, need := range w.needs {
			if err := w.discoverAndAcquire(ctx, jobID, need, report, purchase, art); err != nil {
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
	if len(art.provenance) == 0 {
		// DECIDED (G10): a deliverable with ZERO purchased sources is a genuine
		// completeness failure, not a provisional state — the draft goes out honestly
		// sourceless, QA's no-sources check bounces it, and an unresolved bounce loop
		// escalates to the owner. Pass-with-a-visible-note was decided for stub-shaped
		// provisional states; a report backed by nothing is not one.
		draft.Sources = nil
		for i := range draft.Claims {
			draft.Claims[i].Sources = nil
		}
		draft.Summary += " — NO PURCHASED SOURCES: every acquisition was abandoned or found nothing."
	}

	final, err := envelope.New(jobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// discoverAndAcquire resolves one need text through discovery, then runs the
// purchase-and-adapt loop to its terminal outcome. Discovery finding nothing — for the
// primary OR for a cheaper re-query — is the source-abandoned path: a visible note and
// a clean continuation, never a crash, a spin, or a guess.
func (w *AdaptiveDD) discoverAndAcquire(ctx context.Context, jobID, need string, report Report, purchase Purchase, art *jobArtifacts) error {
	matches, err := w.finder.Find(ctx, need, 0)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	if len(matches) == 0 {
		return w.progress(ctx, jobID, report, map[string]any{
			"stage": "source-abandoned", "need": need,
			"why": "discovery found no service matching the need", "code": "discovery-empty",
		})
	}
	top := matches[0]
	// The demo-visible discovery beat: WHAT was found, BY WHICH description, and how
	// strongly — no merchant or resource name ever appeared in the need.
	if err := w.progress(ctx, jobID, report, map[string]any{
		"stage": "source-discovered", "need": need,
		"resource": top.Resource, "description": top.Description,
		"amount_micros": top.AmountMicros, "score": top.Score,
	}); err != nil {
		return err
	}
	req := PurchaseRequest{
		Merchant: top.Merchant, Resource: top.Resource,
		AmountMicros: top.AmountMicros, MaxAmountMicros: top.AmountMicros,
		Purpose: need,
	}
	return w.acquire(ctx, jobID, need, req, report, purchase, art)
}

// cheaperFor re-queries discovery with a price cap strictly below the rejected amount.
// nil = nothing cheaper matches the need (the abandonment path takes over).
func (w *AdaptiveDD) cheaperFor(ctx context.Context, need string, rejected PurchaseRequest) (*PurchaseRequest, error) {
	matches, err := w.finder.Find(ctx, need, rejected.AmountMicros-1)
	if err != nil {
		return nil, fmt.Errorf("discovery re-query: %w", err)
	}
	if len(matches) == 0 {
		return nil, nil
	}
	top := matches[0]
	return &PurchaseRequest{
		Merchant: top.Merchant, Resource: top.Resource,
		AmountMicros: top.AmountMicros, MaxAmountMicros: top.AmountMicros,
		Purpose: need,
	}, nil
}

// acquire runs one discovered request through purchase-and-adapt to its terminal outcome.
func (w *AdaptiveDD) acquire(ctx context.Context, jobID, need string, req PurchaseRequest, report Report, purchase Purchase, art *jobArtifacts) error {
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
			adaptations < w.MaxAdaptations:
			// AT-04: the owner asked for an alternative ABOUT COST -> re-query discovery
			// with a cap below the rejected amount and propose what it finds, causally
			// linked to the request the owner answered. Discovery finding nothing falls
			// through to the abandonment note — the honest empty-result path.
			alt, err := w.cheaperFor(ctx, need, req)
			if err != nil {
				return err
			}
			if alt == nil {
				return w.progress(ctx, jobID, report, map[string]any{
					"stage": "source-abandoned", "need": need, "resource": req.Resource,
					"why":  "owner asked for a cheaper source and discovery found none under the rejected price",
					"code": "discovery-empty", "adaptations_used": adaptations,
				})
			}
			adaptations++
			alt.AlternativeTo = out.RequestID
			req = *alt
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
