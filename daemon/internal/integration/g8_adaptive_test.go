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
	"github.com/gnanam1990/snapfall/daemon/internal/discovery"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/purchasing"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// finderAdapter bridges discovery.Matcher to worker.Finder the same way the daemon's
// wiring layer does — worker and discovery never import each other (G10 boundary).
type finderAdapter struct{ m *discovery.Matcher }

func (f finderAdapter) Find(ctx context.Context, need string, maxAmountMicros int64) ([]worker.Found, error) {
	ms, err := f.m.Find(ctx, need, maxAmountMicros)
	if err != nil {
		return nil, err
	}
	out := make([]worker.Found, 0, len(ms))
	for _, m := range ms {
		out = append(out, worker.Found{
			Merchant: m.Merchant, Resource: m.Resource, Description: m.Description,
			AmountMicros: m.AmountMicros, Score: m.Score,
		})
	}
	return out, nil
}

// demoNeeds is the demo-script need order: profile (the $0.04 auto-approve beat) before
// market (the $4.00 escalation and adaptation) — a SLICE, because the beat order is
// part of the demo.
func demoNeeds() []string { return []string{discovery.DemoNeedProfile, discovery.DemoNeedMarket} }

// adaptiveRig wires the full served stack: Brain + the discovery-driven DD worker + QA +
// the REAL Purchaser. Owner decisions are driven deterministically through the
// lifecycle's Pending seam — the same notification surface a dashboard/Telegram owner
// UI will use. G10 migration decision: this rig exercises THE path the demo records —
// there is no scripted source plan anymore.
func adaptiveRig(t *testing.T, cat discovery.Catalog, needs []string, decide func(l *approval.Lifecycle, r approval.Request)) (*brain.Brain, *store.Store) {
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
	if err := b.RegisterWorker(worker.NewDiscoveryDD(worker.StubCompliance{}, finderAdapter{discovery.NewMatcher(cat)}, needs, 1)); err != nil {
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

// dearBenchmarkCatalog mirrors the V2 stand-in but prices the benchmark ABOVE the
// auto-approve threshold, so the discovered alternative escalates too (the bound test).
func dearBenchmarkCatalog() discovery.Static {
	return discovery.Static{
		{Merchant: policy.DemoMerchantProfile, Resource: "GET /v1/company-profile",
			Description: "Competitor company profile", AmountMicros: 40_000},
		{Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
			Description: "Premium market dataset (full competitive landscape)", AmountMicros: 4_000_000},
		{Merchant: policy.DemoMerchantBenchmark, Resource: "GET /v1/benchmark-summary",
			Description: "Coding-assistant benchmark summary", AmountMicros: 200_000},
	}
}

// settlementAmounts reads the executed purchases (pending_settlement records) in
// durable order — the purchase SEQUENCE is part of the demo script.
func settlementAmounts(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	rows, err := st.DB().Query(`SELECT payload_json FROM events WHERE kind='purchase.pending_settlement' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p struct {
			AmountMicros int64 `json:"amount_micros"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.AmountMicros)
	}
	return out
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
	b, st := adaptiveRig(t, discovery.V2StandIn(), demoNeeds(), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "too expensive — find a cheaper source"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_at04")

	// The demo-script SEQUENCE, pinned from the durable record (needs are a slice —
	// beat order must survive every take): profile ($0.04 auto-approve) FIRST, then the
	// premium escalation, then the linked benchmark alternative.
	reqs := approvalRequests(t, st)
	if len(reqs) != 3 {
		t.Fatalf("approval requests = %d, want 3 (profile, premium, then the linked alternative)", len(reqs))
	}
	if !strings.Contains(reqs[0].Resource, "company-profile") || reqs[0].AltTo != "" {
		t.Fatalf("first intent must be the auto-approve profile beat, unlinked: %+v", reqs[0])
	}
	if !strings.Contains(reqs[1].Resource, "premium-dataset") || reqs[1].AltTo != "" {
		t.Fatalf("second intent must be the premium escalation, unlinked: %+v", reqs[1])
	}
	if reqs[2].AltTo != reqs[1].RequestID {
		t.Fatalf("AlternativeTo = %q, want the answered request %q — the causal story is broken", reqs[2].AltTo, reqs[1].RequestID)
	}
	if !strings.Contains(reqs[2].Resource, "benchmark-summary") {
		t.Fatalf("the adaptation did not target the cheaper source: %q", reqs[2].Resource)
	}
	// Executed purchases in order: $0.04 then $0.06 — three beats, two spends, script order.
	if got := settlementAmounts(t, st); len(got) != 2 || got[0] != 40_000 || got[1] != 60_000 {
		t.Fatalf("settlement sequence %v, want [40000 60000]", got)
	}
	// The discovery beat is visible in the record: found by description, never by name.
	var discovered int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='brain.msg.worker.progress' AND payload_json LIKE '%source-discovered%'`).Scan(&discovered)
	if discovered != 2 {
		t.Fatalf("source-discovered notes = %d, want 2 (one per need)", discovered)
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
	if len(report.Provenance) != 2 || !strings.Contains(report.Provenance[0].Resource, "company-profile") ||
		!strings.Contains(report.Provenance[1].Resource, "benchmark-summary") ||
		report.Provenance[1].Status != "pending-integration" {
		t.Fatalf("provenance must carry profile then the adapted source, pending-integration: %+v", report.Provenance)
	}
}

// Pin 1's negative half: a DIFFERENT reason produces DIFFERENT behavior. The owner asks
// for an alternative for a NON-cost reason -> the worker does NOT buy a substitute; it
// abandons the source with a visible note. If this bought the cheaper source anyway, the
// adaptation would be a script, not a decision.
func TestG8_DifferentReasonDifferentBehavior(t *testing.T) {
	b, st := adaptiveRig(t, discovery.V2StandIn(), demoNeeds(), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "external data is not appropriate for this engagement — use internal sources only"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_nocost")

	if reqs := approvalRequests(t, st); len(reqs) != 2 {
		t.Fatalf("approval requests = %d, want exactly 2 (profile + premium) — a non-cost reason must not trigger the cheaper-source re-query", len(reqs))
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
	b, st := adaptiveRig(t, dearBenchmarkCatalog(), demoNeeds(), func(l *approval.Lifecycle, r approval.Request) {
		if _, err := l.Decide(context.Background(), r.ID, approval.DecideRequestAlternative,
			"gnanam", "still too expensive for this budget"); err != nil {
			t.Errorf("Decide: %v", err)
		}
	})
	runJob(t, b, "job_bound")

	if reqs := approvalRequests(t, st); len(reqs) != 3 {
		t.Fatalf("approval requests = %d, want exactly 3 (profile + premium + one bounded alternative) — the bound must stop a further attempt", len(reqs))
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

// The G10 zero-source decision, end to end: discovery finds nothing for ANY need (empty
// catalog) -> both needs land on the visible source-abandoned path -> the worker submits
// an honestly SOURCELESS draft -> QA bounces it as a genuine completeness failure (not
// pass-with-a-note, which was decided for stub-shaped provisional states) -> the bounded
// revision loop exhausts -> the job ESCALATES to the owner. No crash, no spin, no
// purchase, no report shipped on the back of nothing.
func TestG10_AllNeedsEmpty_ZeroSourceReportEscalates(t *testing.T) {
	b, st := adaptiveRig(t, discovery.Static{}, demoNeeds(), nil)
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_dry", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	if err := b.Confirm(ctx, "job_dry", "gnanam"); err != nil {
		t.Fatal(err)
	}
	// The task terminates cleanly into escalation — AwaitTask must not hang or crash.
	if err := b.AwaitTask("job_dry"); err != nil {
		t.Logf("task terminal err (acceptable if clean): %v", err)
	}

	js, ok := b.Job("job_dry")
	if !ok || js.Stage != brain.StageEscalated {
		t.Fatalf("job stage = %v, want escalated — a report with zero sources must not ship", js.Stage)
	}
	if reqs := approvalRequests(t, st); len(reqs) != 0 {
		t.Fatalf("no purchase should ever have been proposed: %+v", reqs)
	}
	if got := settlementAmounts(t, st); len(got) != 0 {
		t.Fatalf("no money should have moved: %v", got)
	}
	var abandoned int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='brain.msg.worker.progress' AND payload_json LIKE '%discovery-empty%'`).Scan(&abandoned)
	if abandoned < 2 {
		t.Fatalf("both abandonments must be visible in the record, got %d", abandoned)
	}
}
