package brain

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func newFrozenBrain(t *testing.T) (*Brain, *freeze.Registry) {
	t.Helper()
	b, st, _ := newTestBrain(t)
	reg, err := freeze.NewRegistry(context.Background(), st, time.Now)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	b.SetFreeze(reg, "org_demo")
	return b, reg
}

// Test 11 — AT-09 "stops new claims" on the task side: a frozen job cannot be
// assigned; a frozen org accepts no new jobs; a frozen agent kind is not dispatched;
// the withheld dispatch is recorded.
func TestAT09_FrozenJobStopsNewTasks(t *testing.T) {
	b, reg := newFrozenBrain(t)
	ctx := context.Background()

	// Frozen JOB: scoped fine, but confirm→assign refuses.
	if _, err := b.HandleOwnerRequest(ctx, "job_f1", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	reg.Engage(ctx, freeze.KindJob, "job_f1", "gnanam", "incident")
	// Async (G8): Confirm dispatches and returns; the freeze gate at worker-start withholds
	// the run, surfacing as the task's terminal error.
	if err := b.Confirm(ctx, "job_f1", "gnanam"); err != nil {
		t.Fatalf("confirm dispatch: %v", err)
	}
	if err := b.AwaitTask("job_f1"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("frozen job task: %v, want frozen refusal", err)
	}
	js, _ := b.Job("job_f1")
	if js.Stage == StageComplete || js.Stage == StageDeliveryReady {
		t.Fatalf("frozen job ran to %s", js.Stage)
	}

	// Frozen ORG: intake refused outright.
	reg.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "org-wide stop")
	if _, err := b.HandleOwnerRequest(ctx, "job_f2", "Beta Corp"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("intake on frozen org: %v, want frozen refusal", err)
	}
	reg.Lift(ctx, freeze.KindOrg, "org_demo", "gnanam", "org clear")

	// Frozen AGENT kind: dispatch of that worker refused.
	reg.Engage(ctx, freeze.KindAgent, "due-diligence", "gnanam", "agent quarantined")
	if _, err := b.HandleOwnerRequest(ctx, "job_f3", "Gamma Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_f3", "gnanam"); err != nil {
		t.Fatalf("confirm dispatch: %v", err)
	}
	if err := b.AwaitTask("job_f3"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("dispatch of frozen agent task: %v, want frozen refusal", err)
	}
}

// The QA loop respects the freeze mid-loop: a bounce re-assignment is withheld when
// the job freezes between draft and verdict handling.
func TestAT09_FreezeMidQALoopWithholdsReassignment(t *testing.T) {
	b, _ := newFrozenBrain(t)
	if err := b.RegisterQAWorker(freezeMidLoopQA{b: b}); err != nil {
		t.Fatalf("RegisterQAWorker: %v", err)
	}
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_mid", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	// The QA worker below engages a job freeze BEFORE returning its bounce verdict,
	// so the bounce's re-assignment must be withheld.
	if err := b.Confirm(ctx, "job_mid", "gnanam"); err != nil {
		t.Fatalf("confirm dispatch: %v", err)
	}
	// The QA worker engages a job freeze before its bounce verdict; the bounce's
	// re-assignment is withheld at worker-start and surfaces as the task's terminal error.
	if err := b.AwaitTask("job_mid"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("bounce re-assignment task: %v, want frozen refusal", err)
	}
	js, _ := b.Job("job_mid")
	if js.Stage != StageRevision {
		t.Fatalf("stage %q — the job parks at revision with the freeze recorded, it does not advance", js.Stage)
	}
}

// freezeMidLoopQA bounces every draft, engaging a job freeze right before the verdict.
type freezeMidLoopQA struct{ b *Brain }

func (freezeMidLoopQA) Kind() string { return qa.Kind }
func (w freezeMidLoopQA) Handle(ctx context.Context, a envelope.Envelope, report worker.Report) error {
	w.b.mu.Lock()
	reg := w.b.freezeReg
	w.b.mu.Unlock()
	if _, err := reg.Engage(ctx, freeze.KindJob, a.JobID, "gnanam", "mid-loop kill"); err != nil {
		return err
	}
	verdict, err := envelope.New(a.JobID, envelope.RoleWorker, envelope.TypeQAVerdict, envelope.QAVerdict{
		Passed: false, Reasons: []string{"bounced under freeze"}, Disclaimer: qa.Disclaimer,
	})
	if err != nil {
		return err
	}
	return report(ctx, verdict)
}

// Test 13 — "dashboard remains readable": reads work while everything is frozen.
func TestAT09_ReadsRemainWhileFrozen(t *testing.T) {
	b, reg := newFrozenBrain(t)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_r", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_r", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b, "job_r")
	reg.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "full stop")

	if _, ok := b.Job("job_r"); !ok {
		t.Error("Job() read failed while frozen")
	}
	if _, err := b.memory.Get("job_r"); err != nil {
		t.Errorf("memory read failed while frozen: %v", err)
	}
	if rep := reg.StatusReport(); len(rep.Active) != 1 {
		t.Error("freeze status report unavailable while frozen")
	}
}

// Test 12 — the dispatch chokepoint is single: every worker invocation flows through
// assign(), where the freeze gate lives. Same source-scan technique as the
// StageDeliveryReady and Grant-site pins.
func TestAT09_DispatchChokepointIsSingle(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\.Handle\(`)
	sites := 0
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(re.FindAll(raw, -1)); n > 0 {
			sites += n
			files = append(files, e.Name())
		}
	}
	if sites != 1 || len(files) != 1 || files[0] != "router.go" {
		t.Fatalf("worker dispatch sites = %d in %v, want exactly 1 in router.go (assign) — a second site bypasses the freeze gate", sites, files)
	}
}

// Test 17 — Brain rehydrates job stages from memory files after restart.
func TestRecover_BrainRehydratesJobStages(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "jobs")

	b1, st, _ := newTestBrain(t)
	mem, err := NewMemoryStore(memDir)
	if err != nil {
		t.Fatal(err)
	}
	b1.memory = mem
	ctx := context.Background()

	if _, err := b1.HandleOwnerRequest(ctx, "job_h", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b1.Confirm(ctx, "job_h", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b1, "job_h")

	// "Restart": a fresh Brain over the same memory dir, jobs map empty until Recover.
	b2 := New(b1.log, st, mem, b1.funding)
	b2.SetScoper(StubScoper{})
	if _, ok := b2.Job("job_h"); ok {
		t.Fatal("fresh brain should not know the job before Recover")
	}
	if err := b2.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	js, ok := b2.Job("job_h")
	if !ok {
		t.Fatal("job lost across restart")
	}
	if js.Stage != StageComplete || js.Scope == "" {
		t.Fatalf("rehydrated job wrong: %+v", js)
	}
}

// Review decision #3 (G8 freeze × async): a freeze that engages in the dispatch->start
// WINDOW — after dispatchTask launched the goroutine, before the worker's freeze-check —
// still stops the worker. "Begins" means worker-start. The test-only beforeFreezeCheck
// hook engages the freeze deterministically inside that window.
func TestAT09_FreezeInDispatchWindowStopsWorker(t *testing.T) {
	b, reg := newFrozenBrain(t)
	ctx := context.Background()

	var ran atomic.Bool
	b.mu.Lock()
	b.workers["due-diligence"] = windowWorker{ran: &ran}
	// Engage a JOB freeze in the dispatch->start window (invoked just before the gate).
	b.beforeFreezeCheck = func() {
		reg.Engage(ctx, freeze.KindJob, "job_win", "gnanam", "freeze in the dispatch window")
	}
	b.mu.Unlock()

	if _, err := b.HandleOwnerRequest(ctx, "job_win", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_win", "gnanam"); err != nil {
		t.Fatalf("confirm dispatch: %v", err)
	}
	if err := b.AwaitTask("job_win"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("task: %v, want frozen refusal", err)
	}
	if ran.Load() {
		t.Fatal("the worker RAN despite a freeze in the dispatch->start window — \"begins\" is not worker-start")
	}
}

// windowWorker records whether its Handle body ever executed.
type windowWorker struct{ ran *atomic.Bool }

func (windowWorker) Kind() string { return "due-diligence" }
func (w windowWorker) Handle(context.Context, envelope.Envelope, worker.Report) error {
	w.ran.Store(true)
	return nil
}
