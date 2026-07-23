package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/purchasing"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// adaptiveRig wires the full served stack: Brain + AdaptiveDD + QA + the REAL Purchaser.
// Owner decisions are driven deterministically through the lifecycle's Pending seam — the
// same notification surface a dashboard/Telegram owner UI will use.
func adaptiveRig(t *testing.T, needs []worker.SourceNeed, decide func(l *approval.Lifecycle, r approval.Request)) (*brain.Brain, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "g8.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mem, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}

	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, mem, nil)
	b.SetScoper(brain.StubScoper{})
	if err := b.RegisterWorker(worker.NewAdaptiveDD(worker.StubCompliance{}, needs, 1)); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterQAWorker(qa.Worker{}); err != nil {
		t.Fatal(err)
	}

	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	if decide != nil {
		life.Pending = func(r approval.Request) { decide(life, r) }
	}
	b.SetPurchaser(purchasing.New(life, st, purchasing.RealClock{}, "org_demo", 5*time.Minute))
	return b, st
}

func premiumWithCheaper(cheaperMicros int64) []worker.SourceNeed {
	return []worker.SourceNeed{{
		Primary: worker.PurchaseRequest{
			Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
			AmountMicros: 4_000_000, MaxAmountMicros: 4_000_000, Purpose: "premium market dataset",
		},
		Cheaper: &worker.PurchaseRequest{
			Merchant: policy.DemoMerchantBenchmark, Resource: "GET /v1/benchmark-summary",
			AmountMicros: cheaperMicros, MaxAmountMicros: cheaperMicros, Purpose: "benchmark summary (cheaper)",
		},
	}}
}

func runJob(t *testing.T, b *brain.Brain, jobID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, jobID, "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, jobID, "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := b.AwaitTask(jobID); err != nil {
		t.Fatalf("task: %v", err)
	}
}

func approvalRequests(t *testing.T, st *store.Store) []struct {
	RequestID string
	AltTo     string
	Resource  string
} {
	t.Helper()
	rows, err := st.DB().Query(`SELECT payload_json FROM events WHERE kind='approval.requested' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct {
		RequestID string
		AltTo     string
		Resource  string
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p struct {
			RequestID string `json:"request_id"`
			Intent    struct {
				AlternativeTo string `json:"AlternativeTo"`
				Resource      string `json:"Resource"`
			} `json:"intent"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, struct {
			RequestID string
			AltTo     string
			Resource  string
		}{p.RequestID, p.Intent.AlternativeTo, p.Intent.Resource})
	}
	return out
}

// Pin 1+2 — AT-04 for real: the owner answers request-alternative with a COST reason; the
// worker adapts to the cheaper source BECAUSE of what the reason says, and the replacement
// intent carries AlternativeTo = the answered request — asserted from the durable event
// the activity feed consumes, end to end.
func TestG8_RejectWithCostReasonAdaptsAndLinks(t *testing.T) {
	b, st := adaptiveRig(t, premiumWithCheaper(60_000), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "too expensive — find a cheaper source"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_at04")

	reqs := approvalRequests(t, st)
	if len(reqs) != 2 {
		t.Fatalf("approval requests = %d, want 2 (premium then the linked alternative)", len(reqs))
	}
	if reqs[0].AltTo != "" {
		t.Fatalf("the original intent must not carry a link, got %q", reqs[0].AltTo)
	}
	if reqs[1].AltTo != reqs[0].RequestID {
		t.Fatalf("AlternativeTo = %q, want the answered request %q — the causal story is broken", reqs[1].AltTo, reqs[0].RequestID)
	}
	if !strings.Contains(reqs[1].Resource, "benchmark-summary") {
		t.Fatalf("the adaptation did not target the cheaper source: %q", reqs[1].Resource)
	}

	// The delivered report carries the cheaper source's provenance + the labeled stub screen.
	js, ok := b.Job("job_at04")
	if !ok || js.Stage != brain.StageDeliveryReady {
		t.Fatalf("job stage = %v, want delivery_ready", js.Stage)
	}
	var report envelope.Deliverable
	var raw string
	if err := st.DB().QueryRow(
		`SELECT payload_json FROM events WHERE kind='brain.msg.worker.report' ORDER BY seq DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if err := env.Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Compliance == nil || !report.Compliance.Stub || report.Compliance.Decision != "not-screened" {
		t.Fatalf("report compliance must be the labeled stub: %+v", report.Compliance)
	}
	if len(report.Provenance) != 1 || !strings.Contains(report.Provenance[0].Resource, "benchmark-summary") ||
		report.Provenance[0].Status != "pending-integration" {
		t.Fatalf("provenance must carry the adapted source as pending-integration: %+v", report.Provenance)
	}
}

// Pin 1's negative half: a DIFFERENT reason produces DIFFERENT behavior. The owner asks
// for an alternative for a NON-cost reason -> the worker does NOT buy a substitute; it
// abandons the source with a visible note. If this bought the cheaper source anyway, the
// adaptation would be a script, not a decision.
func TestG8_DifferentReasonDifferentBehavior(t *testing.T) {
	b, st := adaptiveRig(t, premiumWithCheaper(60_000), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "external data is not appropriate for this engagement — use internal sources only"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_nocost")

	if reqs := approvalRequests(t, st); len(reqs) != 1 {
		t.Fatalf("approval requests = %d, want exactly 1 — a non-cost reason must not trigger the cheaper-source fallback", len(reqs))
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='brain.msg.worker.progress' AND payload_json LIKE '%source-abandoned%'`).Scan(&n)
	if n == 0 {
		t.Fatal("the abandonment must be visible (a source-abandoned progress event), not silent")
	}
	js, _ := b.Job("job_nocost")
	if js.Stage != brain.StageDeliveryReady {
		t.Fatalf("job must still complete without the source, got %v", js.Stage)
	}
}

// The bound: an owner who keeps answering request-alternative terminates the loop, never
// spins it — the same bound-and-stop ruling as the QA revision loop. Cheaper is priced
// above the auto-approve threshold so it escalates too; MaxAdaptations=1 means exactly
// two intents ever exist, then abandonment.
func TestG8_OwnerRejectsAlternativeToo_BoundStops(t *testing.T) {
	b, st := adaptiveRig(t, premiumWithCheaper(200_000), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "still too expensive for this budget"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_bound")

	if reqs := approvalRequests(t, st); len(reqs) != 2 {
		t.Fatalf("approval requests = %d, want exactly 2 — the bound must stop a third attempt", len(reqs))
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='brain.msg.worker.progress' AND payload_json LIKE '%source-abandoned%'`).Scan(&n)
	if n == 0 {
		t.Fatal("bound exhaustion must be visible as an abandonment note")
	}
	js, _ := b.Job("job_bound")
	if js.Stage != brain.StageDeliveryReady {
		t.Fatalf("the task must terminate cleanly after the bound, got %v", js.Stage)
	}
}
