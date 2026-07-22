package brain

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func newTestBrain(t *testing.T) (*Brain, *store.Store, *funding.Agent) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "brain.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mem, err := NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	fund := funding.New()
	b := New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, mem, fund)
	b.SetScoper(StubScoper{})
	if err := b.RegisterWorker(worker.StubDD{}); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	return b, st, fund
}

// G3: a message with no entry in the routing table does not route. The table IS the
// complete set of flows; anything else is an error, not a fallthrough.
func TestRouter_UnroutedMessageIsAnError(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()

	// A worker trying to speak an owner verb: no such route exists.
	e, _ := envelope.New("job_1", envelope.RoleWorker, envelope.TypeOwnerConfirm, nil)
	if err := b.Deliver(ctx, e); err == nil {
		t.Fatal("worker speaking an owner type must not route")
	}

	// An unknown type entirely.
	e2, _ := envelope.New("job_1", envelope.RoleOwner, envelope.Type("owner.transfer_everything"), nil)
	if err := b.Deliver(ctx, e2); err == nil {
		t.Fatal("unknown message types must not route")
	}
}

// G3: every delivered message is in the event log BEFORE its handler runs — the log
// is what Brain replays from, so an unlogged message must be impossible.
func TestRouter_EveryMessageIsLogged(t *testing.T) {
	b, st, _ := newTestBrain(t)
	ctx := context.Background()

	before, _ := st.EventCount(ctx)

	// Route one full job through; count messages that hit the log.
	if _, err := b.HandleOwnerRequest(ctx, "job_log", "Acme Corp"); err != nil {
		t.Fatalf("HandleOwnerRequest: %v", err)
	}
	if err := b.Confirm(ctx, "job_log", "owner"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	after, _ := st.EventCount(ctx)
	// owner.request + scope_proposal + owner.confirm + assignment + progress + report + job_report = 7
	if after-before != 7 {
		t.Errorf("event log grew by %d, want 7 — a message moved without being recorded", after-before)
	}
}

// G3: the Report callback pins From to RoleWorker. A worker that stamps its envelope
// as RoleOwner still arrives as a worker — it cannot impersonate the owner to reach
// owner-only routes.
func TestRouter_WorkerCannotSpoofOwnerRole(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()

	// A malicious worker that reports with From=RoleOwner and an owner-only type.
	evil := spoofingWorker{}
	if err := b.RegisterWorker(evil); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	b.mu.Lock()
	b.jobs["job_evil"] = &jobState{JobID: "job_evil", Scope: "x", Stage: StageConfirmed, Worker: evil.Kind()}
	b.mu.Unlock()

	err := b.assign(ctx, "job_evil", evil.Kind())
	if err == nil {
		t.Fatal("the spoofed owner.confirm must fail to route: From was pinned to worker, and no worker->owner.confirm route exists")
	}
}

// spoofingWorker tries to speak as the owner through its report callback.
type spoofingWorker struct{}

func (spoofingWorker) Kind() string { return "spoofer" }
func (spoofingWorker) Handle(ctx context.Context, a envelope.Envelope, report worker.Report) error {
	forged, _ := envelope.New(a.JobID, envelope.RoleOwner, envelope.TypeOwnerConfirm, OwnerDecision{By: "forged"})
	return report(ctx, forged) // report() must pin From back to RoleWorker
}

// FR-BRN-004 defense in depth: even a correctly-routed instruction is refused by the
// funding agent when it carries no owner approval.
func TestFunding_RefusesUnapprovedInstruction(t *testing.T) {
	_, _, fund := newTestBrain(t)

	err := fund.Execute(context.Background(), funding.Instruction{JobID: "job_x", Kind: "request_advance", AmountMicros: 12_500_000})
	if err == nil {
		t.Fatal("an instruction without owner approval must be refused")
	}
	if got := len(fund.Executed()); got != 0 {
		t.Errorf("refused instruction was recorded as executed: %d", got)
	}
}

func TestRegisterWorker_RejectsDuplicateKind(t *testing.T) {
	b, _, _ := newTestBrain(t)
	if err := b.RegisterWorker(worker.StubDD{}); err == nil {
		t.Fatal("duplicate worker kind must be rejected")
	}
}
