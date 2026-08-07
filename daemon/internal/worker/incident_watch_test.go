package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

type fakeIncident struct {
	scan IncidentScan
	err  error
}

func (f fakeIncident) Watch(context.Context) (IncidentScan, error) { return f.scan, f.err }

// runWatch runs the worker and returns its reports and the Handle error, so a test can tell a
// produced report apart from a loud failure.
func runWatch(t *testing.T, source IncidentSource, purchase Purchase) ([]envelope.Envelope, error) {
	t.Helper()
	env, err := envelope.New("job_watch", envelope.RoleBrain, envelope.TypeAssignment, Assignment{})
	if err != nil {
		t.Fatal(err)
	}
	var reports []envelope.Envelope
	herr := NewIncidentWatch(source).Handle(context.Background(), env,
		func(_ context.Context, e envelope.Envelope) error { reports = append(reports, e); return nil },
		purchase)
	return reports, herr
}

func notesOf(t *testing.T, reports []envelope.Envelope) envelope.Deliverable {
	t.Helper()
	if len(reports) != 2 || reports[1].Type != envelope.TypeWorkerReport {
		t.Fatalf("reports = %d, want progress then report", len(reports))
	}
	var d envelope.Deliverable
	if err := reports[1].Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// THE ACCEPTANCE BAR: the observed state is the input, and the report must move with it. A monitor
// that reports the same thing whether or not incidents occurred is a stub with extra steps.
func TestIncidentWatchOutputMovesWithInput(t *testing.T) {
	withIncidents := fakeIncident{scan: IncidentScan{Connected: true, StreamURL: "u", StreamScanned: 9, Incidents: []Incident{
		{Kind: "freeze.engaged", Source: "stream", EntityID: "org", Detail: "at t1"},
		{Kind: "payment.failed", Source: "stream", EntityID: "job_2", Detail: "at t2"},
	}}}
	clean := fakeIncident{scan: IncidentScan{Connected: true, StreamURL: "u", StreamScanned: 9}}

	dirty, derr := runWatch(t, withIncidents, nil)
	quiet, qerr := runWatch(t, clean, nil)
	if derr != nil || qerr != nil {
		t.Fatalf("healthy observations must not error: %v / %v", derr, qerr)
	}
	dn, qn := notesOf(t, dirty), notesOf(t, quiet)
	if dn.Summary == qn.Summary {
		t.Fatalf("output did not move with input: both summaries are %q — a stub with extra steps", dn.Summary)
	}
	if len(dn.Claims) == len(qn.Claims) {
		t.Fatalf("claims did not move with input: %d both times", len(dn.Claims))
	}
}

// THE MONITOR'S DEFINING PROPERTY: an all-clear report and a broken monitor are never the same.
// A clean observation yields a report that STATES it connected and observed; a failed observation
// yields an ERROR and no report at all. A monitor silent when broken is worse than no monitor.
func TestIncidentWatchEmptyIsDistinctFromBroken(t *testing.T) {
	// All clear: connected, nothing wrong.
	reports, err := runWatch(t, fakeIncident{scan: IncidentScan{Connected: true, StreamURL: "u", StreamScanned: 7, Probed: 3}}, nil)
	if err != nil {
		t.Fatalf("a clean observation must not error: %v", err)
	}
	clear := notesOf(t, reports)
	if !strings.Contains(clear.Summary, "All clear") || !strings.Contains(clear.Summary, "connected") {
		t.Fatalf("all-clear report must state it connected and observed, got %q", clear.Summary)
	}
	if !strings.Contains(clear.Summary, "7 stream frame") {
		t.Fatalf("all-clear report must show how much it observed, got %q", clear.Summary)
	}

	// Broken: could not observe. Must be a loud failure, not a calm report.
	brokenReports, brokenErr := runWatch(t, fakeIncident{err: errors.New("connection refused")}, nil)
	if brokenErr == nil {
		t.Fatal("a broken monitor must return an error, never a silent all-clear")
	}
	if len(brokenReports) != 0 {
		t.Fatalf("a broken monitor must produce no report, got %d", len(brokenReports))
	}

	// A source that returns neither an error nor a connection is a broken contract, treated broken.
	_, contractErr := runWatch(t, fakeIncident{scan: IncidentScan{Connected: false}}, nil)
	if contractErr == nil {
		t.Fatal("an unconnected scan without an error must be treated as broken")
	}
}

func TestIncidentWatchNeverSpends(t *testing.T) {
	poison := func(context.Context, PurchaseRequest) (PurchaseOutcome, error) {
		t.Fatal("incident-watch attempted a purchase — a read-only operator tool must never spend")
		return PurchaseOutcome{}, nil
	}
	reports, err := runWatch(t, fakeIncident{scan: IncidentScan{Connected: true, StreamScanned: 1}}, poison)
	if err != nil {
		t.Fatal(err)
	}
	if notesOf(t, reports).Summary == "" {
		t.Fatal("report carries no summary")
	}
}

// ── the real SSE adapter ────────────────────────────────────────────────────

func sseServer(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
		// returning closes the body → the client's scanner hits EOF and terminates promptly
	}))
}

const (
	snapshotFrame = `{"kind":"snapshot","snapshot":{}}`
	freezeFrame   = `{"kind":"event","seq":3,"event":{"kind":"freeze.engaged","jobId":"org_demo","at":"2026-08-07T10:00:00Z"}}`
	paymentFrame  = `{"kind":"event","seq":4,"event":{"kind":"payment.failed","jobId":"job_9","at":"2026-08-07T10:01:00Z"}}`
	fundedFrame   = `{"kind":"event","seq":2,"event":{"kind":"JobFunded","entityId":"0xabc","at":"2026-08-07T09:00:00Z"}}`
)

func TestStreamIncidentSourceReadsRecordedIncidents(t *testing.T) {
	srv := sseServer(t, snapshotFrame, fundedFrame, freezeFrame, paymentFrame)
	defer srv.Close()

	scan, err := (StreamIncidentSource{URL: srv.URL, Window: 2 * time.Second}).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !scan.Connected || scan.StreamScanned != 4 {
		t.Fatalf("scan = %+v, want connected and 4 frames read", scan)
	}
	if len(scan.Incidents) != 2 {
		t.Fatalf("want 2 recorded incidents (freeze + payment), got %+v", scan.Incidents)
	}
	kinds := scan.Incidents[0].Kind + "," + scan.Incidents[1].Kind
	if !strings.Contains(kinds, "freeze.engaged") || !strings.Contains(kinds, "payment.failed") {
		t.Fatalf("incident kinds = %s", kinds)
	}
	// JobFunded is not an incident kind — a benign event must not be flagged.
	for _, inc := range scan.Incidents {
		if inc.Kind == "JobFunded" {
			t.Fatal("a benign event was reported as an incident")
		}
	}
}

// Adapter-level variance: two different streams yield two different incident sets.
func TestStreamIncidentSourceVariesWithStream(t *testing.T) {
	one := sseServer(t, snapshotFrame, freezeFrame)
	defer one.Close()
	two := sseServer(t, snapshotFrame, freezeFrame, paymentFrame)
	defer two.Close()

	a, err := (StreamIncidentSource{URL: one.URL, Window: time.Second}).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := (StreamIncidentSource{URL: two.URL, Window: time.Second}).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Incidents) == len(b.Incidents) {
		t.Fatalf("incident count did not move with the stream: %d both times", len(a.Incidents))
	}
}

// Connected, no incidents: a valid empty scan — NOT an error, but provably a live read.
func TestStreamIncidentSourceEmptyWhenNoIncidents(t *testing.T) {
	srv := sseServer(t, snapshotFrame, fundedFrame)
	defer srv.Close()

	scan, err := (StreamIncidentSource{URL: srv.URL, Window: time.Second}).Watch(context.Background())
	if err != nil {
		t.Fatalf("a clean stream must not error: %v", err)
	}
	if !scan.Connected || scan.StreamScanned == 0 {
		t.Fatalf("clean scan must still prove a live read: %+v", scan)
	}
	if len(scan.Incidents) != 0 {
		t.Fatalf("clean stream reported incidents: %+v", scan.Incidents)
	}
}

// THE CRITICAL TEST: a monitor that cannot connect must ERROR, never return a silent empty scan.
func TestStreamIncidentSourceErrorsWhenUnreachable(t *testing.T) {
	srv := sseServer(t)
	url := srv.URL
	srv.Close() // nothing listens now → connection refused

	scan, err := (StreamIncidentSource{URL: url, Window: time.Second}).Watch(context.Background())
	if err == nil {
		t.Fatalf("an unreachable stream must error, not return an empty scan: %+v", scan)
	}
	if scan.Connected {
		t.Fatal("a failed connection must not report Connected")
	}
}

func TestStreamIncidentSourceErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := (StreamIncidentSource{URL: srv.URL, Window: time.Second}).Watch(context.Background()); err == nil {
		t.Fatal("a non-200 stream response must error")
	}
}

// ── the health probe ────────────────────────────────────────────────────────

func TestHealthProbeFlagsServerErrorsNotClientRefusals(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	// 402 is the sidecar's paid endpoint answering — alive, must NOT be an incident.
	paid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(402) }))
	defer paid.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer ok.Close()
	dead := ok.URL
	deadSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close() // unreachable

	scan, err := (HealthProbeSource{Endpoints: []string{down.URL, paid.URL, ok.URL, deadURL}, Timeout: time.Second}).
		Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Probed != 4 {
		t.Fatalf("probed = %d, want 4", scan.Probed)
	}
	flagged := map[string]string{}
	for _, inc := range scan.Incidents {
		flagged[inc.EntityID] = inc.Kind
	}
	if flagged[down.URL] != "endpoint-down" {
		t.Errorf("a 500 endpoint must be flagged down, got %q", flagged[down.URL])
	}
	if flagged[deadURL] != "endpoint-unreachable" {
		t.Errorf("an unreachable endpoint must be flagged, got %q", flagged[deadURL])
	}
	if _, ok := flagged[paid.URL]; ok {
		t.Error("a 402 (alive, refusing) endpoint must NOT be flagged down")
	}
	_ = dead
}

// The combined source: the stream's failure is the monitor's failure, even if the probe is fine.
func TestCombinedIncidentSourceFailsWhenStreamBroken(t *testing.T) {
	broken := fakeIncident{err: errors.New("stream refused")}
	healthy := fakeIncident{scan: IncidentScan{Connected: true, Probed: 1}}
	if _, err := NewCombinedIncidentSource(broken, healthy).Watch(context.Background()); err == nil {
		t.Fatal("a broken stream must fail the combined monitor even when the probe is healthy")
	}
}

func TestCombinedIncidentSourceMergesProbeIncidents(t *testing.T) {
	stream := fakeIncident{scan: IncidentScan{Connected: true, StreamScanned: 5, Incidents: []Incident{
		{Kind: "freeze.engaged", Source: "stream", EntityID: "org"},
	}}}
	health := fakeIncident{scan: IncidentScan{Connected: true, Probed: 2, Incidents: []Incident{
		{Kind: "endpoint-down", Source: "health", EntityID: "http://x"},
	}}}
	scan, err := NewCombinedIncidentSource(stream, health).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Incidents) != 2 || scan.StreamScanned != 5 || scan.Probed != 2 {
		t.Fatalf("combined scan did not merge both sources: %+v", scan)
	}
}
