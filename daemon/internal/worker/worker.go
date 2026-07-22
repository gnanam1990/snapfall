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

// Report is the ONLY outbound channel a Worker has. Brain constructs it; it delivers to
// Brain and nowhere else. A worker holding this can say things TO BRAIN — that is all.
type Report func(ctx context.Context, e envelope.Envelope) error

// Worker executes exactly one assignment and reports back.
type Worker interface {
	// Kind names the worker slot, e.g. "due-diligence".
	Kind() string
	// Handle runs one assignment. Everything it wants the world to know goes through report.
	Handle(ctx context.Context, assignment envelope.Envelope, report Report) error
}

// ── The Phase-1 stub DD worker ─────────────────────────────────────────────

// Assignment is the payload Brain sends with TypeAssignment.
type Assignment struct {
	Scope string `json:"scope"`
}

// DDReport is the payload the stub DD worker sends back with TypeWorkerReport.
type DDReport struct {
	Summary  string   `json:"summary"`
	Findings []string `json:"findings"`
	Complete bool     `json:"complete"`
}

// StubDD is the scripted due-diligence worker for the Sun-26 exit gate. Real source
// purchases (via PaymentIntents through Brain) and the compliance step arrive in G8;
// the loop it exercises is already the real one.
type StubDD struct{}

// Kind implements Worker.
func (StubDD) Kind() string { return "due-diligence" }

// Handle produces a canned report for the assigned scope: one progress update, then the report.
func (StubDD) Handle(ctx context.Context, assignment envelope.Envelope, report Report) error {
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

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, DDReport{
		Summary: "Stub due-diligence report for: " + a.Scope,
		Findings: []string{
			"corporate registry entry located (stub)",
			"no adverse media found (stub)",
			"beneficial ownership chain resolved (stub)",
		},
		Complete: true,
	})
	if err != nil {
		return err
	}
	return report(ctx, final)
}
