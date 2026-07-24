package ownerapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func newAPI(t *testing.T) (*Server, *approval.Lifecycle) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	l := approval.New(st, time.Now)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	return New(l, st, slog.New(slog.NewTextHandler(io.Discard, nil))), l
}

// submitPending opens one real $4.00 escalation and returns its request.
func submitPending(t *testing.T, l *approval.Lifecycle) approval.Request {
	t.Helper()
	res, err := l.Submit(context.Background(), approval.Intent{
		IntentID: "pi_api", OrgID: "org_demo", JobID: "job_api", AgentID: "due-diligence",
		Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
		AmountMicros: 4_000_000, MaxAmountMicros: 4_000_000, Purpose: "premium",
		Nonce: "0x" + strings.Repeat("ee", 32), ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return *res.Request
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// GET /approvals renders the pending request with the intentHash the decision must echo.
func TestAPI_PendingListCarriesIntentHash(t *testing.T) {
	s, l := newAPI(t)
	req := submitPending(t, l)

	w, out := do(t, s.Handler(), "GET", "/api/v1/approvals", "")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	list := out["approvals"].([]any)
	if len(list) != 1 {
		t.Fatalf("approvals = %d, want 1", len(list))
	}
	v := list[0].(map[string]any)
	if v["requestId"] != req.ID || v["intentHash"] != req.IntentHash || v["amountUsdc"] != "4000000" {
		t.Fatalf("view wrong: %+v", v)
	}
}

// The happy decision: request_alternative with the SHOWN hash lands as a real lifecycle
// decision the blocked Purchase wakes on.
func TestAPI_DecisionWithShownHashLands(t *testing.T) {
	s, l := newAPI(t)
	req := submitPending(t, l)

	body := `{"kind":"request_alternative","by":"gnanam","reason":"too expensive — find a cheaper source","intentHash":"` + req.IntentHash + `"}`
	w, out := do(t, s.Handler(), "POST", "/api/v1/approvals/"+req.ID+"/decision", body)
	if w.Code != 200 || out["state"] != "alternative_requested" {
		t.Fatalf("status %d out %+v", w.Code, out)
	}
	snap, _ := l.Snapshot(req.ID)
	if snap.State != approval.StateAlternativeRequested || snap.Reason == "" {
		t.Fatalf("lifecycle did not record the decision: %+v", snap)
	}
}

// H2 decision 2: a decision echoing a STALE hash is refused at click time with the
// CURRENT view — never a silent later failure.
func TestAPI_StaleViewRefusedWithCurrentState(t *testing.T) {
	s, l := newAPI(t)
	req := submitPending(t, l)

	body := `{"kind":"approve","by":"gnanam","reason":"ok","intentHash":"0xdeadbeef"}`
	w, out := do(t, s.Handler(), "POST", "/api/v1/approvals/"+req.ID+"/decision", body)
	if w.Code != 409 {
		t.Fatalf("status %d, want 409 STALE_VIEW", w.Code)
	}
	errObj := out["error"].(map[string]any)
	if errObj["code"] != "STALE_VIEW" {
		t.Fatalf("code %v", errObj["code"])
	}
	cur := out["current"].(map[string]any)
	if cur["intentHash"] != req.IntentHash {
		t.Fatal("the refusal must carry the CURRENT view so the UI re-renders and re-asks")
	}
	if snap, _ := l.Snapshot(req.ID); snap.State != approval.StatePending {
		t.Fatalf("a stale-view refusal must not decide anything: %v", snap.State)
	}
}

// Conflicting decision on a terminal request → 409; unknown id → 404; junk → 400.
func TestAPI_ConflictUnknownAndBadRequest(t *testing.T) {
	s, l := newAPI(t)
	req := submitPending(t, l)
	if _, err := l.Decide(context.Background(), req.ID, approval.DecideReject, "gnanam", "no"); err != nil {
		t.Fatal(err)
	}

	body := `{"kind":"approve","by":"gnanam","reason":"ok","intentHash":"` + req.IntentHash + `"}`
	if w, out := do(t, s.Handler(), "POST", "/api/v1/approvals/"+req.ID+"/decision", body); w.Code != 409 ||
		out["error"].(map[string]any)["code"] != "ALREADY_DECIDED" {
		t.Fatalf("conflicting decision: %d %+v", w.Code, out)
	}
	if w, _ := do(t, s.Handler(), "POST", "/api/v1/approvals/apr_nope/decision", body); w.Code != 404 {
		t.Fatalf("unknown id: %d, want 404", w.Code)
	}
	if w, _ := do(t, s.Handler(), "POST", "/api/v1/approvals/"+req.ID+"/decision", `{"kind":"maybe"}`); w.Code != 400 {
		t.Fatalf("bad body: %d, want 400", w.Code)
	}
}

func TestAPI_WorkforceCatalogAndHireStartConfiguredWatcher(t *testing.T) {
	s, _ := newAPI(t)
	s.WorkerCatalog = []WorkerManifest{{
		ID:            "build-monitor",
		Name:          "Build Monitor",
		Category:      "Engineering operations",
		Description:   "Reports committed milestone evidence to Brain.",
		Permissions:   []string{"Read-only repo", "No payments", "No shell"},
		ChecklistPath: ".snapfall/milestone.json",
	}}
	w, out := do(t, s.Handler(), "GET", "/api/v1/workforce/manifests", "")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want 200", w.Code)
	}
	manifests := out["manifests"].([]any)
	if len(manifests) != 1 || manifests[0].(map[string]any)["id"] != "build-monitor" {
		t.Fatalf("catalog = %+v", manifests)
	}

	var hired HireWorkerRequest
	s.HireWorker = func(_ context.Context, req HireWorkerRequest) (HireWorkerResult, error) {
		hired = req
		return HireWorkerResult{
			JobID: "milestone_watch_1", VaultJobID: "0xwatch", State: "watching",
		}, nil
	}
	body := `{"repository":"/work/acme","quoteUsdc":"25.00","by":"anandan"}`
	w, out = do(t, s.Handler(), "POST", "/api/v1/workforce/build-monitor/hire", body)
	if w.Code != http.StatusCreated || out["state"] != "watching" || out["jobId"] != "milestone_watch_1" {
		t.Fatalf("hire status=%d out=%+v", w.Code, out)
	}
	if hired.ManifestID != "build-monitor" || hired.Repository != "/work/acme" ||
		hired.QuoteUSDC != "25.00" || hired.By != "anandan" {
		t.Fatalf("hire request = %+v", hired)
	}
}

func TestAPI_WorkforceHireRefusesBadOrUnwiredRequests(t *testing.T) {
	s, _ := newAPI(t)
	w, out := do(t, s.Handler(), "POST", "/api/v1/workforce/build-monitor/hire", `{}`)
	if w.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "BAD_REQUEST" {
		t.Fatalf("bad hire status=%d out=%+v", w.Code, out)
	}

	body := `{"repository":"/work/acme","quoteUsdc":"25.00","by":"anandan"}`
	w, out = do(t, s.Handler(), "POST", "/api/v1/workforce/build-monitor/hire", body)
	if w.Code != http.StatusServiceUnavailable || out["error"].(map[string]any)["code"] != "NOT_WIRED" {
		t.Fatalf("unwired hire status=%d out=%+v", w.Code, out)
	}
}

// H2 decision 3, enforced not warned: a non-loopback bind without the token is refused
// at startup.
func TestAPI_NonLoopbackBindRefusedWithoutToken(t *testing.T) {
	s, _ := newAPI(t)
	t.Setenv("SNAPFALL_OWNER_TOKEN", "")
	err := s.Run(context.Background(), "0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "refusing non-loopback bind") {
		t.Fatalf("non-loopback bind without token: %v, want refusal", err)
	}
}

// Security-review fix: when a token is configured it is ENFORCED on every request —
// wrong or missing bearer is 401 on every route, and the correct bearer works. A token
// that only gated startup while requests went unauthenticated was an auth bypass.
func TestAPI_TokenEnforcedOnEveryRequest(t *testing.T) {
	t.Setenv("SNAPFALL_OWNER_TOKEN", strings.Repeat("t", 32))
	s, l := newAPI(t) // captures the token at construction
	req := submitPending(t, l)
	h := s.Handler()

	// No bearer -> 401 on both surfaces; nothing is decided.
	if w, _ := do(t, h, "GET", "/api/v1/approvals", ""); w.Code != 401 {
		t.Fatalf("approvals without bearer: %d, want 401", w.Code)
	}
	body := `{"kind":"approve","by":"gnanam","reason":"ok","intentHash":"` + req.IntentHash + `"}`
	if w, _ := do(t, h, "POST", "/api/v1/approvals/"+req.ID+"/decision", body); w.Code != 401 {
		t.Fatalf("decision without bearer: %d, want 401", w.Code)
	}
	if snap, _ := l.Snapshot(req.ID); snap.State != approval.StatePending {
		t.Fatalf("an unauthenticated decision landed: %v", snap.State)
	}

	// Wrong bearer -> 401. Correct bearer -> the decision lands.
	r := httptest.NewRequest("POST", "/api/v1/approvals/"+req.ID+"/decision", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("wrong bearer: %d, want 401", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/approvals/"+req.ID+"/decision", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("correct bearer: %d, want 200 (%s)", w.Code, w.Body.String())
	}
}
