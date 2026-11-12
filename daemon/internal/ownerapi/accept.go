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
// pending-chain stop. The response carries the same `settlement` status the acceptance
// read does (H2 §4.3), read after the attempt, so the portal never infers it from the
// state label. The Accept ran either way: if only the status read after it fails, the
// response is still 200 with `settlement: "unknown"` (the detail logged server-side), never
// a guessed value and never a hard error over an Accept that was processed.
func (s *Server) handleCustomerAccept(w http.ResponseWriter, r *http.Request) {
	state, err := s.Accept(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"jobId": r.PathValue("id"), "state": state,
			"settlement": s.settlementOrUnknown(r, "accept processed, settlement status unreadable", "state", state)})
	case errors.Is(err, brain.ErrFrozen):
		writeErr(w, http.StatusLocked, "FROZEN", "the kill switch is engaged; settlement actions are stopped", nil)
	case errors.Is(err, brain.ErrNotDeliveryReady):
		writeErr(w, http.StatusConflict, "NOT_READY", err.Error(), nil)
	default:
		s.customerInternal(w, r, "customer accept failed", err, msgAcceptFailed)
	}
}

// Customer routes are reachable by any accept-credential holder through the dashboard
// proxy, so a 500 there never echoes the underlying error: chain and RPC errors can carry
// the RPC URL with its API key, and store errors describe internals. The detail is logged
// server-side against the job; the client gets the code and a generic message. (Owner
// routes, behind the owner token, keep their detail.)
const (
	msgAcceptFailed      = "the acceptance could not be processed; try again shortly"
	msgInvoiceUnreadable = "the receipt could not be read; try again shortly"
)

// settlementOrUnknown is the job's settlement status for a customer response, or
// brain.SettlementUnknown when it cannot be read (a chain oracle or the record is
// unreadable). The detail is logged server-side against the job and never reaches the body:
// a chain error can carry the RPC URL with its API key.
func (s *Server) settlementOrUnknown(r *http.Request, what string, attrs ...any) string {
	settlement, err := s.Settlement(r.Context(), r.PathValue("id"))
	if err != nil {
		if s.log != nil {
			s.log.Error(what, append([]any{"job", r.PathValue("id"), "route", r.URL.Path, "err", err}, attrs...)...)
		}
		return brain.SettlementUnknown
	}
	return settlement
}

func (s *Server) customerInternal(w http.ResponseWriter, r *http.Request, what string, err error, msg string, attrs ...any) {
	if s.log != nil {
		s.log.Error(what, append([]any{"job", r.PathValue("id"), "route", r.URL.Path, "err", err}, attrs...)...)
	}
	writeErr(w, http.StatusInternalServerError, "INTERNAL", msg, nil)
}

// GET /api/v1/customer/jobs/{id}/acceptance — the customer's read: the job's stage as
// the customer may see it, and its settlement status (H2 §4.3): "none" | "settled" |
// "pending" | "retry" | "unknown". `accepted` alone says only that the claim happened, not
// that the job settled; `settlement` is what tells the portal whether Accept is worth
// pressing again. A settlement status that cannot be read (a chain oracle or the record is
// unreadable) degrades to "unknown" with the stage fields intact, never a guessed value and
// never a 500 over a stage that read fine; the detail is logged server-side. Guarded by the
// same credential — the read side is not free.
func (s *Server) handleCustomerAcceptance(w http.ResponseWriter, r *http.Request) {
	stage, ok := s.JobState(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "UNKNOWN_JOB", "the daemon has no such job", nil)
		return
	}
	out := map[string]any{
		"jobId": r.PathValue("id"), "stage": stage, "accepted": stage == string(brain.StageAccepted),
		"settlement": s.settlementOrUnknown(r, "customer acceptance read: settlement status unreadable"),
	}
	if s.VaultJobID != nil {
		if v := s.VaultJobID(r.PathValue("id")); v != "" {
			out["vaultJobId"] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
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
		s.customerInternal(w, r, "customer invoice read failed", err, msgInvoiceUnreadable)
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
		if s.VerifyAccept == nil || s.Accept == nil || s.JobState == nil || s.Settlement == nil {
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
