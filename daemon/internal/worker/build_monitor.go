package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// BuildSnapshot is one measured repository state. CompletionPct is evidence reported
// to Brain; it never authorizes a release or moves money.
type BuildSnapshot struct {
	CompletionPct int
	Revision      string
	Completed     []string
	Pending       []string
}

// BuildProgressSource is the Build-Monitor's repository seam. A filesystem/git adapter
// supplies production measurements; deterministic adapters drive integration tests.
type BuildProgressSource interface {
	Snapshot(ctx context.Context, repository string) (BuildSnapshot, error)
}

// BuildMonitor observes a repository and reports through Brain's existing Worker seam.
// It has no release, payment, advance, or signing capability.
type BuildMonitor struct {
	source BuildProgressSource
}

// BuildMonitorKind is the Brain worker slot used by standing-pipeline milestones.
const BuildMonitorKind = "build-monitor"

// NewBuildMonitor constructs the worker around its read-only repository source.
func NewBuildMonitor(source BuildProgressSource) *BuildMonitor {
	return &BuildMonitor{source: source}
}

// Kind implements Worker.
func (*BuildMonitor) Kind() string { return BuildMonitorKind }

// Handle measures once per assignment. The progress envelope is deliberately emitted
// before the evidence report so Brain durably sees completion before any later release
// decision can be made by a human/customer-facing flow.
func (w *BuildMonitor) Handle(ctx context.Context, assignment envelope.Envelope, report Report, _ Purchase) error {
	if w == nil || w.source == nil {
		return fmt.Errorf("build-monitor source is not configured")
	}
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}
	repository := strings.TrimSpace(a.Scope)
	if repository == "" {
		return fmt.Errorf("build-monitor assignment has no repository")
	}
	snapshot, err := w.source.Snapshot(ctx, repository)
	if err != nil {
		return fmt.Errorf("measure repository %s: %w", repository, err)
	}
	if snapshot.CompletionPct < 0 || snapshot.CompletionPct > 100 {
		return fmt.Errorf("repository completion %d is outside 0..100", snapshot.CompletionPct)
	}
	if strings.TrimSpace(snapshot.Revision) == "" {
		return fmt.Errorf("repository snapshot has no revision")
	}

	progress, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{
			"stage":          "build-monitored",
			"completion_pct": snapshot.CompletionPct,
			"revision":       snapshot.Revision,
			"completed":      snapshot.Completed,
			"pending":        snapshot.Pending,
		})
	if err != nil {
		return err
	}
	if err := report(ctx, progress); err != nil {
		return err
	}

	source := "git:" + snapshot.Revision
	status := "release blocked: milestone checks remain"
	if len(snapshot.Pending) == 0 && snapshot.CompletionPct == 100 {
		status = "milestone evidence complete; ready for independent release decision"
	}
	draft := envelope.Deliverable{
		Title:   "Build milestone evidence",
		Summary: fmt.Sprintf("%d%% complete at %s — %s", snapshot.CompletionPct, snapshot.Revision, status),
		Claims: []envelope.Claim{{
			Text:    fmt.Sprintf("%d completed check(s), %d pending check(s)", len(snapshot.Completed), len(snapshot.Pending)),
			Sources: []string{source},
		}},
		Sources:    []string{source},
		Disclaimer: "Repository completion is measured evidence, not authorization to release escrowed funds.",
	}
	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, final)
}
