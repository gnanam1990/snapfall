package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// Review batch: the report callback binds the assigned JOB, so a worker cannot report
// against a different job by swapping the envelope's JobID.
func TestReport_CrossJobReportRefused(t *testing.T) {
	b, _ := newQABrain(t)
	report := b.workerReportFor("due-diligence", "job_assigned")

	e, _ := envelope.New("job_OTHER", envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{"stage": "x", "completion_pct": 1})
	if err := report(context.Background(), e); err == nil || !strings.Contains(err.Error(), "cross-job") {
		t.Fatalf("cross-job report: %v, want a cross-job refusal", err)
	}
}

// Review batch: the QA worker cannot author a draft (inject a TypeWorkerReport that
// then gets reviewed as its own work).
func TestReport_QAWorkerCannotAuthorDraft(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_x", "Acme"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_x", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// Deliver a TypeWorkerReport stamped as the QA kind (as deliverFromWorker would
	// see it): it must be refused, not stored as an author draft.
	draft := envelope.Deliverable{Title: "self-authored", Summary: "x",
		Claims: []envelope.Claim{{Text: "c", Sources: []string{"s"}}}, Sources: []string{"s"}}
	e, _ := envelope.New("job_x", envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err := b.deliverFromWorker(ctx, qa.Kind, e); err == nil || !strings.Contains(err.Error(), "does not author") {
		t.Fatalf("QA authoring a draft: %v, want refusal", err)
	}
}

// A non-author worker kind cannot submit a draft for a job assigned to someone else.
func TestReport_NonAuthorWorkerRefused(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_y", "Acme"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_y", "gnanam"); err != nil { // assigns due-diligence, runs to delivery_ready
		t.Fatalf("confirm: %v", err)
	}
	draft := envelope.Deliverable{Title: "t", Summary: "s",
		Claims: []envelope.Claim{{Text: "c", Sources: []string{"s"}}}, Sources: []string{"s"}}
	e, _ := envelope.New("job_y", envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err := b.deliverFromWorker(ctx, "some-other-kind", e); err == nil {
		t.Fatal("a non-author kind reporting a draft must be refused")
	}
}

var _ worker.Worker = qa.Worker{}
