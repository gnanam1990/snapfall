package ownerapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/brain"
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
	r := httptest.NewRequest("POST", "/api/v1/jobs/job_acc/accept-link", nil)
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
		r := httptest.NewRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("customer route with %q: %d, want 401", auth[:13], w.Code)
		}
	}
	// The job credential opens nothing owner-side.
	r = httptest.NewRequest("GET", "/api/v1/approvals", nil)
	r.Header.Set("Authorization", credBearer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("owner route with the job credential: %d, want 401", w.Code)
	}

	// The real credential works on both customer routes.
	r = httptest.NewRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
	r.Header.Set("Authorization", credBearer)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "accepted-pending-chain") {
		t.Fatalf("accept with credential: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/v1/customer/jobs/job_acc/acceptance", nil)
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
	r := httptest.NewRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
	r.Header.Set("Authorization", "Bearer "+*tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 423 || !strings.Contains(w.Body.String(), "FROZEN") {
		t.Fatalf("frozen accept: %d %s, want 423 FROZEN", w.Code, w.Body.String())
	}

	s.Accept = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("%w: stage assigned", brain.ErrNotDeliveryReady)
	}
	r = httptest.NewRequest("POST", "/api/v1/customer/jobs/job_acc/accept", nil)
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
	if rootFuncs != 2 {
		t.Fatalf("root.HandleFunc sites = %d, want exactly the 2 customer routes (both withCustomerAuth-wrapped)", rootFuncs)
	}
	if rootHandles != 1 {
		t.Fatalf("root.Handle sites = %d, want exactly the 1 withAuth-wrapped owner mux", rootHandles)
	}
}
