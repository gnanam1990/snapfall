// Package qa is the G9 Quality-Reviewer worker (PRD §3, FR-QA-001, AT-19).
//
// An INDEPENDENT worker routed by Brain — not a self-review step inside Delivery.
// Self-certification is the failure mode the architecture exists to prevent: the
// reviewer and the author must be different agents, and the bounce must flow through
// Brain like every other message.
//
// The reviewer is deterministic rule-based code this phase (same posture as the stub
// scoper): four checks — completeness, source coverage, unsupported-claim detection,
// customer-data-leakage — each contributing machine-readable reasons. Determinism is
// what makes the demo's QA beat land identically on every take.
//
// HONESTY: a passing verdict is EVIDENCE OF REVIEW, NOT A GUARANTEE. These checks
// catch structural problems (a claim with no source, a leaked marker string); they
// cannot certify that a sourced claim is TRUE or that every leak matches a known
// marker. False negatives ship. Every verdict carries Disclaimer verbatim.
package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// Disclaimer is the honesty sentence every verdict carries and every surface renders.
const Disclaimer = "QA verdict is evidence of review, not a guarantee of correctness; unsupported claims or leaks the checks did not catch may remain."

// Kind is the QA worker's registered kind.
const Kind = "qa-reviewer"

// Reviewer holds the rule configuration.
type Reviewer struct {
	// LeakMarkers are customer-confidential strings that must never appear in a
	// deliverable (FR-QA-001 "customer-data-leakage check"). Configured per org.
	LeakMarkers []string
}

// Review runs the four deterministic checks and returns the verdict.
func (r Reviewer) Review(d envelope.Deliverable) envelope.QAVerdict {
	var reasons []string

	// ── 1. Completeness: the report has a title, a summary, claims, and sources. ──
	if strings.TrimSpace(d.Title) == "" {
		reasons = append(reasons, "completeness: title is empty")
	}
	if strings.TrimSpace(d.Summary) == "" {
		reasons = append(reasons, "completeness: summary is empty")
	}
	if len(d.Claims) == 0 {
		reasons = append(reasons, "completeness: no claims — an empty report cannot be delivered")
	}
	if len(d.Sources) == 0 {
		reasons = append(reasons, "completeness: no sources listed")
	}

	// ── 2 + 3. Source coverage / unsupported claims: every claim carries >=1 source,
	//    and every cited source is in the deliverable's source list. ──
	listed := make(map[string]bool, len(d.Sources))
	for _, s := range d.Sources {
		listed[s] = true
	}
	for i, c := range d.Claims {
		if len(c.Sources) == 0 {
			reasons = append(reasons,
				fmt.Sprintf("unsupported claim #%d: %q cites no source; attach a source or remove the claim", i+1, c.Text))
			continue
		}
		for _, s := range c.Sources {
			if !listed[s] {
				reasons = append(reasons,
					fmt.Sprintf("source coverage: claim #%d cites %q, which is not in the deliverable's source list", i+1, s))
			}
		}
	}

	// ── 4. Customer-data leakage: configured markers must not appear anywhere. ──
	for _, marker := range r.LeakMarkers {
		if marker == "" {
			continue
		}
		if strings.Contains(d.Title, marker) || strings.Contains(d.Summary, marker) {
			reasons = append(reasons, fmt.Sprintf("customer-data leakage: confidential marker %q appears in the deliverable", marker))
			continue
		}
		leaked := false
		for _, c := range d.Claims {
			if strings.Contains(c.Text, marker) {
				reasons = append(reasons, fmt.Sprintf("customer-data leakage: confidential marker %q appears in claim %q", marker, c.Text))
				leaked = true
				break
			}
			for _, src := range c.Sources { // "anywhere" includes citations (review-batch fix)
				if strings.Contains(src, marker) {
					reasons = append(reasons, fmt.Sprintf("customer-data leakage: confidential marker %q appears in a citation for claim %q", marker, c.Text))
					leaked = true
					break
				}
			}
			if leaked {
				break
			}
		}
		if leaked {
			continue
		}
		for _, src := range d.Sources { // and the deliverable's own source list
			if strings.Contains(src, marker) {
				reasons = append(reasons, fmt.Sprintf("customer-data leakage: confidential marker %q appears in the source list", marker))
				break
			}
		}
	}

	return envelope.QAVerdict{
		Passed:     len(reasons) == 0,
		Reasons:    reasons,
		Checked:    len(d.Claims),
		Disclaimer: Disclaimer,
	}
}

// Worker adapts the Reviewer to the Brain-routed worker slot. It receives the draft
// in its assignment, reviews it, and reports the verdict — through the one Report
// callback, like every worker. It cannot mark anything DeliveryReady itself; only
// Brain's verdict handler does that.
type Worker struct {
	Reviewer Reviewer
}

// Kind implements worker.Worker.
func (Worker) Kind() string { return Kind }

// Handle reviews the draft carried in the assignment and reports the verdict.
func (w Worker) Handle(ctx context.Context, assignment envelope.Envelope, report worker.Report) error {
	var a worker.Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}
	if a.Draft == nil {
		return fmt.Errorf("qa assignment for %s carries no draft", assignment.JobID)
	}

	verdict := w.Reviewer.Review(*a.Draft)

	e, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeQAVerdict, verdict)
	if err != nil {
		return err
	}
	return report(ctx, e)
}
