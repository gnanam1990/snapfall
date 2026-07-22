package brain

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// The acceptance criterion, verbatim. AT-19 is NOT in the v4 annex (which ends at
// AT-15) — it is defined by the v7.1 Constitution and the work split:
//
//   docs/PRD.md §9: "AT-19 QA rejection bounces the deliverable and blocks
//   DeliveryReady until revised."
//
//   docs/WORK-SPLIT.md G9: "Done when: AT-19 passes (a planted unsupported claim
//   blocks DeliveryReady until revised)."

func newQABrain(t *testing.T) (*Brain, *MemoryStore) {
	t.Helper()
	b, _, _ := newTestBrain(t) // registers StubDD
	if err := b.RegisterQAWorker(qa.Worker{}); err != nil {
		t.Fatalf("RegisterQAWorker: %v", err)
	}
	return b, b.memory
}

// ═══════════════════════════════════════════════════════════════════════════
// AT-19 — the planted unsupported claim blocks DeliveryReady until revised
// ═══════════════════════════════════════════════════════════════════════════

func TestAT19_PlantedClaimBlocksDeliveryReadyUntilRevised(t *testing.T) {
	b, mem := newQABrain(t)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_at19", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	// Confirm triggers: DD draft #1 (planted claim) → QA bounce → revision → QA pass.
	if err := b.Confirm(ctx, "job_at19", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	js, _ := b.Job("job_at19")
	if js.Stage != StageDeliveryReady {
		t.Fatalf("final stage %q, want delivery_ready", js.Stage)
	}
	if js.RevisionCount != 1 {
		t.Fatalf("revisions = %d, want exactly 1 — the planted claim bounces exactly once", js.RevisionCount)
	}

	jm, _ := mem.Get("job_at19")
	if len(jm.QANotes) != 2 {
		t.Fatalf("QA notes = %v, want [bounce, pass]", jm.QANotes)
	}
	if !strings.Contains(jm.QANotes[0], "QA BOUNCE") || !strings.Contains(jm.QANotes[0], "churn is 40% annually") {
		t.Errorf("bounce note must name the planted claim: %q", jm.QANotes[0])
	}
	if jm.QANotes[1] != "QA PASS" {
		t.Errorf("second note = %q, want QA PASS", jm.QANotes[1])
	}
	if !strings.Contains(jm.QADisclaimer, "not a guarantee") {
		t.Errorf("memory must surface the disclaimer, got %q", jm.QADisclaimer)
	}
	// The delivered report is the REVISED draft: the claim now carries its source.
	if !strings.Contains(jm.Report, "filing:churn-2026") {
		t.Errorf("delivered report is not the revised draft: %s", jm.Report)
	}
}

// The interim states, observed mid-flight: before any verdict, the draft sits in
// qa_review and is NOT DeliveryReady — a report alone moves nothing to ready.
func TestG9_AuthorReportAloneNeverReachesDeliveryReady(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()

	// A QA worker that never verdicts: the flow stops at qa_review, forever.
	b.mu.Lock()
	b.workers[b.qaKind] = silentWorker{kind: b.qaKind}
	b.mu.Unlock()

	if _, err := b.HandleOwnerRequest(ctx, "job_stuck", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_stuck", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	js, _ := b.Job("job_stuck")
	if js.Stage != StageQAReview {
		t.Fatalf("stage %q, want qa_review — the author's report must park at review, never at ready", js.Stage)
	}
}

// silentWorker accepts its assignment and says nothing.
type silentWorker struct{ kind string }

func (w silentWorker) Kind() string { return w.kind }
func (silentWorker) Handle(context.Context, envelope.Envelope, worker.Report) error {
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════
// G9 pin 1 — "can QA be skipped": the three guards
// ═══════════════════════════════════════════════════════════════════════════

// A non-QA worker forging a passing TypeQAVerdict is refused — the kind stamp comes
// from Brain's assignment closure, not from anything the worker controls.
func TestG9_ForgedVerdictFromAuthorWorkerRefused(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()

	// The author worker itself tries to certify its own draft.
	forger := verdictForger{}
	b.mu.Lock()
	b.workers["due-diligence"] = forger // replace the author slot
	b.mu.Unlock()

	if _, err := b.HandleOwnerRequest(ctx, "job_forge", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	err := b.Confirm(ctx, "job_forge", "gnanam")
	if err == nil {
		t.Fatal("a self-certifying author must be refused")
	}
	if !strings.Contains(err.Error(), "not the registered QA reviewer") {
		t.Fatalf("wrong refusal: %v", err)
	}

	js, _ := b.Job("job_forge")
	if js.Stage == StageDeliveryReady {
		t.Fatal("self-certification reached DeliveryReady — QA was skipped")
	}
}

// verdictForger reports a PASSING QA verdict for its own work.
type verdictForger struct{}

func (verdictForger) Kind() string { return "due-diligence" }
func (verdictForger) Handle(ctx context.Context, a envelope.Envelope, report worker.Report) error {
	forged, _ := envelope.New(a.JobID, envelope.RoleWorker, envelope.TypeQAVerdict, envelope.QAVerdict{
		Passed: true, Disclaimer: qa.Disclaimer,
	})
	return report(ctx, forged)
}

// A verdict for a job that is not under QA review is refused (replay/out-of-band).
func TestG9_VerdictOutsideReviewRefused(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()

	// Run a job to completion (delivery_ready), then replay a verdict at it.
	if _, err := b.HandleOwnerRequest(ctx, "job_done", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_done", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	v, _ := envelope.New("job_done", envelope.RoleWorker, envelope.TypeQAVerdict,
		envelope.QAVerdict{Passed: false, Reasons: []string{"replayed"}, Disclaimer: qa.Disclaimer})
	if err := b.deliverFromWorker(ctx, b.qaKind, v); err == nil {
		t.Fatal("a verdict outside qa_review must be refused")
	}
}

// The single-assignment-site pin. Package boundaries do NOT allow an import-graph
// proof here (the whole loop lives inside brain), so this is the strongest structural
// statement available: exactly ONE line in the package assigns StageDeliveryReady,
// and it is inside markDeliveryReady — which only the QA verdict handler calls.
// Anyone adding a second assignment site turns this red.
func TestG9_DeliveryReadyHasOneAssignmentSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Stage\s*=\s*StageDeliveryReady`)
	sites := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sites += len(re.FindAll(raw, -1))
	}
	if sites != 1 {
		t.Fatalf("StageDeliveryReady has %d assignment sites, want exactly 1 (inside markDeliveryReady) — a second site is a QA bypass", sites)
	}
}

// A verdict that omits the honesty disclaimer is malformed and refused (pin 3).
func TestG9_VerdictWithoutDisclaimerRefused(t *testing.T) {
	b, _ := newQABrain(t)
	ctx := context.Background()

	b.mu.Lock()
	b.workers[b.qaKind] = silentWorker{kind: b.qaKind}
	b.mu.Unlock()
	if _, err := b.HandleOwnerRequest(ctx, "job_nodisc", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_nodisc", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	v, _ := envelope.New("job_nodisc", envelope.RoleWorker, envelope.TypeQAVerdict,
		envelope.QAVerdict{Passed: true /* no disclaimer */})
	if err := b.deliverFromWorker(ctx, b.qaKind, v); err == nil {
		t.Fatal("a verdict presenting itself as a guarantee (no disclaimer) must be refused")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// G9 pin 2 — bounce-loop termination
// ═══════════════════════════════════════════════════════════════════════════

// hopelessWorker's drafts NEVER pass QA: the planted claim stays unsourced no matter
// how many bounce reasons it receives.
type hopelessWorker struct{}

func (hopelessWorker) Kind() string { return "due-diligence" }
func (hopelessWorker) Handle(ctx context.Context, a envelope.Envelope, report worker.Report) error {
	draft := envelope.Deliverable{
		Title: "Hopeless report", Summary: "still unsourced",
		Claims:  []envelope.Claim{{Text: "unfixable claim", Sources: nil}},
		Sources: []string{"src:1"},
	}
	e, err := envelope.New(a.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, e)
}

func TestG9_ExhaustedRevisionsEscalateToOwner(t *testing.T) {
	b, mem := newQABrain(t)
	ctx := context.Background()

	b.mu.Lock()
	b.workers["due-diligence"] = hopelessWorker{}
	b.mu.Unlock()
	b.SetMaxRevisions(2)

	if _, err := b.HandleOwnerRequest(ctx, "job_hopeless", "Doomed Co"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_hopeless", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// The loop TERMINATED: escalated, not spinning, not silently complete.
	js, _ := b.Job("job_hopeless")
	if js.Stage != StageEscalated {
		t.Fatalf("stage %q, want escalated — the loop must fail loudly, not spin", js.Stage)
	}
	if js.RevisionCount != 3 { // bounce 1 → rev 1, bounce 2 → rev 2, bounce 3 → exhausted
		t.Fatalf("revision count %d, want 3 (maxRevisions 2 + the exhausting bounce)", js.RevisionCount)
	}

	jm, _ := mem.Get("job_hopeless")
	if jm.Stage != string(StageEscalated) {
		t.Errorf("memory stage %q, want escalated", jm.Stage)
	}
	// Every bounce is on the record; nothing reached DeliveryReady.
	if len(jm.QANotes) != 3 {
		t.Errorf("QA notes %d, want 3 bounces", len(jm.QANotes))
	}
	for _, n := range jm.QANotes {
		if !strings.Contains(n, "QA BOUNCE") {
			t.Errorf("unexpected note %q", n)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// No-QA regression: without a registered QA slot, Phase-1 behavior is untouched
// ═══════════════════════════════════════════════════════════════════════════

func TestG9_WithoutQARegisteredPhase1FlowUnchanged(t *testing.T) {
	b, _, _ := newTestBrain(t) // NO RegisterQAWorker
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_p1", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_p1", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	js, _ := b.Job("job_p1")
	if js.Stage != StageComplete {
		t.Fatalf("stage %q, want complete (Phase-1 flow)", js.Stage)
	}
}
