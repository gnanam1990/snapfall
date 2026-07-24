package ownerapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
)

// The customer surface — the settlement principal's routes (H2's daemon-side half of
// the fall). AUTH IS PER REQUEST AND PER PRINCIPAL: every route here demands the
// per-job accept credential on every call, including the read side — and the two
// principals never cross: the owner bearer does not open customer routes (these live
// OUTSIDE withAuth and check only the job credential), and the job credential opens
// nothing owner-side (withAuth checks only the owner token). A credential that merely
// had to exist while requests went unauthenticated would be the same auth bypass the
// owner token shipped with once — pinned by test this time, before shipping.

// POST /api/v1/customer/jobs/{id}/accept — the customer's Accept: authentication done
// by withCustomerAuth; Brain owns state, freeze, exactly-once, and the honest
// pending-chain stop.
func (s *Server) handleCustomerAccept(w http.ResponseWriter, r *http.Request) {
	state, err := s.Accept(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"jobId": r.PathValue("id"), "state": state})
	case errors.Is(err, brain.ErrFrozen):
		writeErr(w, http.StatusLocked, "FROZEN", "the kill switch is engaged; settlement actions are stopped", nil)
	case errors.Is(err, brain.ErrNotDeliveryReady):
		writeErr(w, http.StatusConflict, "NOT_READY", err.Error(), nil)
	default:
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error(), nil)
	}
}

// GET /api/v1/customer/jobs/{id}/acceptance — the customer's read: the job's stage as
// the customer may see it. Guarded by the same credential — the read side is not free.
func (s *Server) handleCustomerAcceptance(w http.ResponseWriter, r *http.Request) {
	stage, ok := s.JobState(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "UNKNOWN_JOB", "the daemon has no such job", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobId": r.PathValue("id"), "stage": stage, "accepted": stage == string(brain.StageAccepted),
	})
}

// GET /api/v1/customer/jobs/{id}/invoice — the customer's receipt (V9), credential-gated.
//
// THE COPY-SERVING DECISION, made concrete: each principal sees ONLY their own copy
// through their own auth. The owner route (owner token) serves the owner copy with
// internal gap detail, reconciliation, and alerts; THIS route (per-job accept
// credential) serves the CUSTOMER copy — plain-language gaps, internals stripped, and
// NO reconciliation or alerts (those are owner-only). The daemon already builds both
// copies in the billing Record; this is the read that finally serves the customer half,
// closing the seam flagged at G12.
func (s *Server) handleCustomerInvoice(w http.ResponseWriter, r *http.Request) {
	var payload string
	err := s.st.DB().QueryRowContext(r.Context(),
		`SELECT payload_json FROM events WHERE kind='billing.invoice' AND entity_id=?
		 ORDER BY seq DESC LIMIT 1`, r.PathValue("id")).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "NO_INVOICE", "no receipt has been generated for this job yet", nil)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error(), nil)
		return
	}
	var rec billing.Record
	if err := json.Unmarshal([]byte(payload), &rec); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "corrupt invoice record", nil)
		return
	}
	// The CUSTOMER copy only — no owner internals, no reconciliation, no alerts.
	writeJSON(w, http.StatusOK, map[string]any{"version": rec.Version, "invoice": rec.Customer})
}

// POST /api/v1/jobs/{id}/accept-link — OWNER surface (inside withAuth): mint or rotate
// the customer credential for a delivery-ready job. The plaintext appears in this
// response exactly once and is never stored or logged.
func (s *Server) handleMintAcceptLink(w http.ResponseWriter, r *http.Request) {
	if s.MintAccept == nil {
		writeErr(w, http.StatusServiceUnavailable, "NOT_WIRED", "accept credentials are not wired in this build", nil)
		return
	}
	token, err := s.MintAccept(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusConflict, "NOT_READY", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": r.PathValue("id"), "acceptToken": token,
		"note": "shown once; rotates any prior credential"})
}

// withCustomerAuth enforces the per-job credential on EVERY customer route — wrong or
// missing is 401 on the write AND the read side. Unwired seams refuse (fail closed),
// never fall open.
func (s *Server) withCustomerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.VerifyAccept == nil || s.Accept == nil || s.JobState == nil {
			writeErr(w, http.StatusServiceUnavailable, "NOT_WIRED", "customer surface is not wired in this build", nil)
			return
		}
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) || !s.VerifyAccept(r.PathValue("id"), strings.TrimPrefix(h, prefix)) {
			writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid accept credential for this job is required", nil)
			return
		}
		next(w, r)
	}
}
