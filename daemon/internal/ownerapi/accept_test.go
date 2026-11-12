package ownerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// wireAccept wires the customer seams the way the daemon does, over a tiny in-memory
// credential state (the Brain-side truth of mint/verify/accept is pinned in the brain
// package; these tests pin the HTTP auth contract).
func wireAccept(s *Server) (mintedToken *string) {
	token := ""
	accepted := false
	s.MintAccept = func(_ context.Context, jobID string) (string, error) {
		if jobID != "job_acc" {
			return "", fmt.Errorf("%w: unknown job", brain.ErrNotDeliveryReady)
		}
		token = "act_" + strings.Repeat("cd", 32)
		return token, nil
	}
	s.VerifyAccept = func(jobID, tok string) bool { return jobID == "job_acc" && token != "" && tok == token }
	s.Accept = func(_ context.Context, jobID string) (string, error) {
		accepted = true
		return "accepted-pending-chain", nil
	}
	s.JobState = func(jobID string) (string, bool) {
		if jobID != "job_acc" {
			return "", false
		}
		if accepted {
			return "accepted", true
		}
		return "delivery_ready", true
	}
	s.Settlement = func(_ context.Context, jobID string) (string, error) {
		if accepted {
			return brain.SettlementPending, nil
		}
		return brain.SettlementNone, nil
	}
	return &token
}

// The owner-token lesson, applied before shipping: the credential is enforced PER
// REQUEST on every customer route — write AND read — and the two principals never
// cross: the OWNER bearer does not open customer routes; the job credential opens
// nothing owner-side.
func TestAPI_AcceptCredentialEnforcedPerRequestAndPerPrincipal(t *testing.T) {
	t.Setenv("SNAPFALL_OWNER_TOKEN", strings.Repeat("t", 32))
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	h := s.Handler()
	ownerBearer := "Bearer " + strings.Repeat("t", 32)

	// Mint lives on the OWNER surface: no owner bearer -> 401; with it -> the token.
	if w, _ := do(t, h, "POST", "/api/v1/jobs/job_acc/accept-link", ""); w.Code != 401 {
		t.Fatalf("mint without owner bearer: %d, want 401", w.Code)
	}
	r := ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil)
	r.Header.Set("Authorization", ownerBearer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || *tokPtr == "" {
		t.Fatalf("owner mint: %d %s", w.Code, w.Body.String())
	}
	credBearer := "Bearer " + *tokPtr

	// Customer routes: missing credential -> 401 on BOTH the write and the READ side.
	if w, _ := do(t, h, "POST", "/api/v1/customer/jobs/job_acc/accept", ""); w.Code != 401 {
		t.Fatalf("accept without credential: %d, want 401", w.Code)
	}
	if w, _ := do(t, h, "GET", "/api/v1/customer/jobs/job_acc/acceptance", ""); w.Code != 401 {
		t.Fatalf("acceptance READ without credential: %d, want 401 — the read side is not free", w.Code)
	}
	// Wrong credential -> 401. The OWNER bearer on a customer route -> 401: different
	// principal, different door.
	for _, auth := range []string{"Bearer act_" + strings.Repeat("00", 32), ownerBearer} {
		r := ownerRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("customer route with %q: %d, want 401", auth[:13], w.Code)
		}
	}
	// The job credential opens nothing owner-side.
	r = ownerRequest("GET", "/api/v1/approvals", nil)
	r.Header.Set("Authorization", credBearer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("owner route with the job credential: %d, want 401", w.Code)
	}

	// The real credential works on both customer routes.
	r = ownerRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
	r.Header.Set("Authorization", credBearer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "accepted-pending-chain") {
		t.Fatalf("accept with credential: %d %s", w.Code, w.Body.String())
	}
	r = ownerRequest("GET", "/api/v1/customer/jobs/job_acc/acceptance", nil)
	r.Header.Set("Authorization", credBearer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"accepted":true`) {
		t.Fatalf("acceptance read: %d %s", w.Code, w.Body.String())
	}
}

// Unwired seams REFUSE — a build without the customer surface serves 503, never an
// unauthenticated pass-through.
func TestAPI_CustomerSurfaceUnwiredRefuses(t *testing.T) {
	s, _ := newAPI(t)
	h := s.Handler()
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/customer/jobs/job_x/accept"},
		{"GET", "/api/v1/customer/jobs/job_x/acceptance"},
	} {
		if w, out := do(t, h, route.method, route.path, ""); w.Code != 503 ||
			out["error"].(map[string]any)["code"] != "NOT_WIRED" {
			t.Fatalf("%s %s unwired: %d %+v, want 503 NOT_WIRED", route.method, route.path, w.Code, out)
		}
	}
}

// Freeze and not-ready map to distinct, honest statuses at the HTTP boundary.
func TestAPI_AcceptErrorMapping(t *testing.T) {
	s, _ := newAPI(t)
	tok := wireAccept(s)
	s.Accept = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("%w: job %q is frozen", brain.ErrFrozen, "job_acc")
	}
	h := s.Handler()
	// mint (no owner token set -> loopback posture, no bearer needed)
	if w, _ := do(t, h, "POST", "/api/v1/jobs/job_acc/accept-link", ""); w.Code != 200 {
		t.Fatalf("mint: %d", w.Code)
	}
	r := ownerRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
	r.Header.Set("Authorization", "Bearer "+*tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 423 || !strings.Contains(w.Body.String(), "FROZEN") {
		t.Fatalf("frozen accept: %d %s, want 423 FROZEN", w.Code, w.Body.String())
	}

	s.Accept = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("%w: stage assigned", brain.ErrNotDeliveryReady)
	}
	r = ownerRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
	r.Header.Set("Authorization", "Bearer "+*tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "NOT_READY") {
		t.Fatalf("not-ready accept: %d %s, want 409 NOT_READY", w.Code, w.Body.String())
	}
}

// The route-group law (part of the boot-pins class closure): the ROOT mux admits
// exactly the two credential-wrapped customer routes plus the single withAuth-wrapped
// owner mux — a route registered on root outside either wrapper would bypass both
// principals' auth, and this scan makes that a red test instead of a quiet hole.
func TestAPI_RootMuxAdmitsOnlyWrappedRouteGroups(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	rootFuncs, rootHandles := 0, 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		rootFuncs += strings.Count(src, "root.HandleFunc(")
		rootHandles += strings.Count(src, "root.Handle(")
	}
	// The three customer routes — accept, acceptance, invoice (V9) — all
	// withCustomerAuth-wrapped. A route registered on root outside the wrapper would
	// bypass both principals' auth; this count makes that a red test, not a quiet hole.
	if rootFuncs != 3 {
		t.Fatalf("root.HandleFunc sites = %d, want exactly the 3 customer routes (all withCustomerAuth-wrapped)", rootFuncs)
	}
	if rootHandles != 1 {
		t.Fatalf("root.Handle sites = %d, want exactly the 1 withAuth-wrapped owner mux", rootHandles)
	}
}

// V9 copy-serving decision, pinned: the customer route serves the CUSTOMER copy —
// plain-language gaps, NO owner internals, NO reconciliation, NO alerts — credential-
// gated like every customer route, and 401 without it.
func TestAPI_CustomerInvoiceServesCustomerCopyOnly(t *testing.T) {
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	h := s.Handler()

	// Mint the credential (loopback posture, no owner token set) so VerifyAccept knows it.
	if w, _ := do(t, h, "POST", "/api/v1/jobs/job_acc/accept-link", ""); w.Code != 200 {
		t.Fatalf("mint: %d", w.Code)
	}

	// Seed a billing.invoice record whose OWNER copy carries internal gap detail and
	// alerts, and whose CUSTOMER copy is plain — the two-copy Record G12 builds.
	rec := billing.Record{
		Version: 2,
		Owner: billing.Invoice{
			Copy: billing.CopyOwner, JobID: "job_acc", Status: billing.StatusPartial,
			Gaps: []billing.Gap{{Stage: "settlement", Cause: "no on-chain settlement", Detail: "OWNER-ONLY internal detail"}},
		},
		Customer: billing.Invoice{
			Copy: billing.CopyCustomer, JobID: "job_acc", Status: billing.StatusPartial,
			Gaps: []billing.Gap{{Stage: "settlement", Cause: "no on-chain settlement"}},
		},
		Alerts: []billing.Alert{{Kind: billing.AlertExpenseOutsidePolicy, Message: "m", Data: map[string]string{}}},
	}
	raw, _ := json.Marshal(rec)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if _, err := s.st.Append(context.Background(), store.Event{Kind: "billing.invoice", EntityID: "job_acc", Actor: "billing", Payload: payload}); err != nil {
		t.Fatal(err)
	}

	// No credential → 401 (the read side is not free).
	if w, _ := do(t, h, "GET", "/api/v1/customer/jobs/job_acc/invoice", ""); w.Code != 401 {
		t.Fatalf("invoice without credential: %d, want 401", w.Code)
	}

	// With the credential → the CUSTOMER copy, and owner internals absent.
	r := ownerRequest("GET", "/api/v1/customer/jobs/job_acc/invoice", nil)
	r.Header.Set("Authorization", "Bearer "+*tokPtr)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("invoice with credential: %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"copy":"customer"`) {
		t.Fatalf("must serve the customer copy: %s", body)
	}
	for _, leak := range []string{"OWNER-ONLY internal detail", "expense-outside-policy", "reconciliation"} {
		if strings.Contains(body, leak) {
			t.Fatalf("customer copy leaked owner-only content %q: %s", leak, body)
		}
	}
}

// The acceptance read names the job's on-chain id when one is bound, so a customer with
// their own wallet can settle it; with none bound (or the seam unwired) the field is absent
// rather than empty.
func TestAPI_AcceptanceReadNamesTheVaultJob(t *testing.T) {
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	h := s.Handler()
	r := ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if *tokPtr == "" {
		t.Fatal("no credential minted")
	}
	read := func() map[string]any {
		t.Helper()
		r := ownerRequest("GET", "/api/v1/customer/jobs/job_acc/acceptance", nil)
		r.Header.Set("Authorization", "Bearer "+*tokPtr)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("acceptance read: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	if _, has := read()["vaultJobId"]; has {
		t.Fatal("an unwired seam must leave vaultJobId out")
	}
	vault := ""
	s.VaultJobID = func(string) string { return vault }
	if _, has := read()["vaultJobId"]; has {
		t.Fatal("an unbound job must leave vaultJobId out, not send it empty")
	}
	vault = "0x" + strings.Repeat("ab", 32)
	if got := read()["vaultJobId"]; got != vault {
		t.Fatalf("vaultJobId = %v, want %s", got, vault)
	}
}

// customerCall sends one credential-bearing request to a customer route and decodes it.
func customerCall(t *testing.T, s *Server, method, path, token string) (int, map[string]any, string) {
	t.Helper()
	r := ownerRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// The acceptance read and the Accept response both carry `settlement`, exactly the value the
// Brain derives, for each of the four values; the existing fields are unchanged.
func TestAPI_AcceptanceAndAcceptCarryTheSettlementStatus(t *testing.T) {
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	s.Handler().ServeHTTP(httptest.NewRecorder(), ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil))
	if *tokPtr == "" {
		t.Fatal("no credential minted")
	}
	for _, v := range []string{brain.SettlementNone, brain.SettlementSettled, brain.SettlementPending, brain.SettlementRetry} {
		s.Settlement = func(context.Context, string) (string, error) { return v, nil }
		code, out, body := customerCall(t, s, "GET", "/api/v1/customer/jobs/job_acc/acceptance", *tokPtr)
		if code != 200 || out["settlement"] != v || out["jobId"] != "job_acc" || out["stage"] == nil {
			t.Fatalf("acceptance read with %q: %d %s", v, code, body)
		}
		if _, has := out["accepted"]; !has {
			t.Fatalf("acceptance read dropped the accepted field: %s", body)
		}
		code, out, body = customerCall(t, s, "POST", "/api/v1/customer/jobs/job_acc/accept", *tokPtr)
		if code != 200 || out["settlement"] != v || out["state"] != "accepted-pending-chain" {
			t.Fatalf("accept with %q: %d %s", v, code, body)
		}
	}
}

// A customer-route 500 never echoes the underlying error: chain errors can carry the RPC
// URL with its API key. The code stays INTERNAL; the message is generic.
func TestAPI_CustomerRoute500sDoNotEchoTheError(t *testing.T) {
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	s.Handler().ServeHTTP(httptest.NewRecorder(), ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil))
	secret := "https://rpc.example/v2/SECRET-API-KEY-123: dial tcp: sqlite: database is locked"
	s.Accept = func(context.Context, string) (string, error) { return "", errors.New(secret) }
	code, out, body := customerCall(t, s, "POST", "/api/v1/customer/jobs/job_acc/accept", *tokPtr)
	if code != 500 || out["error"] == nil || out["error"].(map[string]any)["code"] != "INTERNAL" {
		t.Fatalf("accept fails: %d %s, want 500 INTERNAL", code, body)
	}
	for _, leak := range []string{"SECRET-API-KEY-123", "rpc.example", "sqlite", "database is locked"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the 500 body leaks %q: %s", leak, body)
		}
	}
	if _, has := out["settlement"]; has {
		t.Fatalf("a failed accept must not carry a settlement: %s", body)
	}
}

// A settlement status that cannot be read (a chain oracle down, say) degrades instead of
// failing the route: the acceptance read is still 200 with its stage fields and
// `settlement: "unknown"`, and an Accept that was processed is still 200 with its state and
// `settlement: "unknown"`. The detail is logged server-side and never reaches the body.
func TestAPI_UnreadableSettlementDegradesToUnknown(t *testing.T) {
	s, _ := newAPI(t)
	var logs strings.Builder
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	tokPtr := wireAccept(s)
	s.Handler().ServeHTTP(httptest.NewRecorder(), ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil))
	secret := "https://rpc.example/v2/SECRET-API-KEY-123: cannot read on-chain settlement"
	s.Settlement = func(context.Context, string) (string, error) { return "", errors.New(secret) }
	s.VaultJobID = func(string) string { return "0x" + strings.Repeat("b", 64) }

	noLeak := func(name, body string) {
		t.Helper()
		for _, leak := range []string{"SECRET-API-KEY-123", "rpc.example", "cannot read"} {
			if strings.Contains(body, leak) {
				t.Fatalf("%s: the body leaks %q: %s", name, leak, body)
			}
		}
	}
	code, out, body := customerCall(t, s, "GET", "/api/v1/customer/jobs/job_acc/acceptance", *tokPtr)
	if code != 200 || out["settlement"] != brain.SettlementUnknown || out["jobId"] != "job_acc" ||
		out["stage"] != "delivery_ready" || out["accepted"] != false || out["vaultJobId"] == nil {
		t.Fatalf("acceptance read over an unreadable status: %d %s, want 200 with the stage fields and settlement unknown", code, body)
	}
	noLeak("acceptance read", body)

	code, out, body = customerCall(t, s, "POST", "/api/v1/customer/jobs/job_acc/accept", *tokPtr)
	if code != 200 || out["settlement"] != brain.SettlementUnknown || out["state"] != "accepted-pending-chain" {
		t.Fatalf("accept over an unreadable status: %d %s, want 200 with the state and settlement unknown", code, body)
	}
	noLeak("accept", body)

	code, out, body = customerCall(t, s, "GET", "/api/v1/customer/jobs/job_acc/acceptance", *tokPtr)
	if code != 200 || out["settlement"] != brain.SettlementUnknown || out["accepted"] != true || out["stage"] != "accepted" {
		t.Fatalf("acceptance read after the accept: %d %s", code, body)
	}
	if got := logs.String(); strings.Count(got, "SECRET-API-KEY-123") != 3 || !strings.Contains(got, "level=ERROR") ||
		!strings.Contains(got, "job=job_acc") {
		t.Fatalf("the detail is not logged server-side against the job for each read: %s", got)
	}
}

// The settlement seam is part of the customer surface: unwired, the surface refuses.
func TestAPI_CustomerSurfaceRefusesWithoutTheSettlementSeam(t *testing.T) {
	s, _ := newAPI(t)
	tokPtr := wireAccept(s)
	s.Handler().ServeHTTP(httptest.NewRecorder(), ownerRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil))
	s.Settlement = nil
	if code, _, body := customerCall(t, s, "GET", "/api/v1/customer/jobs/job_acc/acceptance", *tokPtr); code != 503 {
		t.Fatalf("acceptance read with no settlement seam: %d %s, want 503", code, body)
	}
}
