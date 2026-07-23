package ownerapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// wireGenerate wires the server's Generate seam the way the daemon does: build a
// billing.Record, append the durable billing.invoice event, return the record. The
// generation TRUTH (versions, chain reads, triggers) is proven in the brain tests;
// these tests pin the HTTP contract (H2 §4).
func wireGenerate(s *Server, st *store.Store) {
	version := 0
	s.Generate = func(ctx context.Context, jobID string) (billing.Record, error) {
		if jobID == "job_unknown" {
			return billing.Record{}, billing.ErrUnknownJob
		}
		version++
		rec := billing.Record{
			Version: version, Trigger: "owner-request",
			Owner: billing.Invoice{
				Copy: billing.CopyOwner, JobID: jobID, Status: billing.StatusPartial,
				Gaps:        []billing.Gap{{Stage: "settlement", Cause: "no on-chain settlement", Detail: "internal detail"}},
				GeneratedAt: time.Unix(0, 0).UTC(), Disclaimer: billing.Disclaimer,
			},
			Customer: billing.Invoice{
				Copy: billing.CopyCustomer, JobID: jobID, Status: billing.StatusPartial,
				Gaps:        []billing.Gap{{Stage: "settlement", Cause: "no on-chain settlement"}},
				GeneratedAt: time.Unix(0, 0).UTC(), Disclaimer: billing.Disclaimer,
			},
			Alerts: []billing.Alert{{Kind: billing.AlertProjectionDivergence, Message: "m", Data: map[string]string{}}},
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return billing.Record{}, err
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return billing.Record{}, err
		}
		if _, err := st.Append(ctx, store.Event{Kind: "billing.invoice", EntityID: jobID, Actor: "billing", Payload: payload}); err != nil {
			return billing.Record{}, err
		}
		return rec, nil
	}
}

// H2 §4: POST generates and returns the owner copy; GET serves the latest recorded
// version and never generates. The customer copy is NEVER on the wire — the copy-serving
// seam is undecided, and until it is, no daemon route serves it.
func TestAPI_InvoiceEndpointsServeOwnerCopyOnly(t *testing.T) {
	s, _ := newAPI(t)
	wireGenerate(s, s.st)
	h := s.Handler()

	// Nothing generated yet: GET refuses rather than generating.
	if w, out := do(t, h, "GET", "/api/v1/jobs/job_g12/invoice", ""); w.Code != 404 ||
		out["error"].(map[string]any)["code"] != "NO_INVOICE" {
		t.Fatalf("GET before any generation: %d %+v", w.Code, out)
	}

	// POST generates v1: owner copy, alerts included, customer copy absent from the wire.
	w, out := do(t, h, "POST", "/api/v1/jobs/job_g12/invoice", "")
	if w.Code != 200 || out["version"].(float64) != 1 {
		t.Fatalf("POST: %d %+v", w.Code, out)
	}
	inv := out["invoice"].(map[string]any)
	if inv["copy"] != billing.CopyOwner || inv["gaps"].([]any)[0].(map[string]any)["detail"] != "internal detail" {
		t.Fatalf("POST must serve the owner copy with internals: %+v", inv)
	}
	if body := w.Body.String(); strings.Contains(body, `"customer"`) {
		t.Fatalf("the customer copy reached the wire: %s", body)
	}

	// Second POST appends v2; GET now serves the LATEST version, same owner-only shape.
	if w, out := do(t, h, "POST", "/api/v1/jobs/job_g12/invoice", ""); w.Code != 200 || out["version"].(float64) != 2 {
		t.Fatalf("second POST: %d %+v", w.Code, out)
	}
	w, out = do(t, h, "GET", "/api/v1/jobs/job_g12/invoice", "")
	if w.Code != 200 || out["version"].(float64) != 2 {
		t.Fatalf("GET latest: %d %+v", w.Code, out)
	}
	if body := w.Body.String(); strings.Contains(body, `"customer"`) {
		t.Fatalf("GET leaked the customer copy: %s", body)
	}

	// Unknown job on the generate path maps to 404 UNKNOWN_JOB.
	if w, out := do(t, h, "POST", "/api/v1/jobs/job_unknown/invoice", ""); w.Code != 404 ||
		out["error"].(map[string]any)["code"] != "UNKNOWN_JOB" {
		t.Fatalf("unknown job: %d %+v", w.Code, out)
	}
}

// The daemon can serve reads even when generation is not wired (e.g. a stripped-down
// build): POST refuses honestly instead of pretending.
func TestAPI_InvoiceGenerateUnwiredRefuses(t *testing.T) {
	s, _ := newAPI(t)
	if w, out := do(t, s.Handler(), "POST", "/api/v1/jobs/job_x/invoice", ""); w.Code != 503 ||
		out["error"].(map[string]any)["code"] != "NOT_WIRED" {
		t.Fatalf("unwired POST: %d %+v", w.Code, out)
	}
}

// The §1 auth posture covers the new routes with no special-casing: token set ⇒ bearer
// enforced on invoice reads and generation like every other route.
func TestAPI_InvoiceRoutesEnforceToken(t *testing.T) {
	t.Setenv("SNAPFALL_OWNER_TOKEN", strings.Repeat("t", 32))
	s, _ := newAPI(t)
	wireGenerate(s, s.st)
	h := s.Handler()
	if w, _ := do(t, h, "POST", "/api/v1/jobs/job_g12/invoice", ""); w.Code != 401 {
		t.Fatalf("POST without bearer: %d, want 401", w.Code)
	}
	if w, _ := do(t, h, "GET", "/api/v1/jobs/job_g12/invoice", ""); w.Code != 401 {
		t.Fatalf("GET without bearer: %d, want 401", w.Code)
	}
}
