package ownerapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
)

// POST /api/v1/jobs/{id}/advance — the OWNER-INITIATED advance proposal (the snap's
// exercisable trigger today; the funding-observed trigger is the seeded-row-tested
// watcher that has never fired for real). The proposal comes back PENDING in this same
// API's approvals inbox: proposing and approving are two separate owner actions, both
// on the record, and the approval is what mints the Grant.
func (s *Server) handleProposeAdvance(w http.ResponseWriter, r *http.Request) {
	if s.ProposeAdvance == nil {
		writeErr(w, http.StatusServiceUnavailable, "NOT_WIRED", "advance proposals are not wired in this build", nil)
		return
	}
	req, err := s.ProposeAdvance(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"requestId": req.ID, "jobId": req.JobID, "state": "pending",
			"principalUsdc": strconv.FormatInt(req.Intent.AmountMicros, 10),
			"expiresAt":     req.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"note":          "human approval required — decide in the approvals inbox",
		})
	case errors.Is(err, billing.ErrUnknownJob):
		writeErr(w, http.StatusNotFound, "UNKNOWN_JOB", "the daemon has no such job", nil)
	case strings.HasPrefix(err.Error(), "frozen:"):
		// freeze.Err's stable prefix — the kill switch refused the proposal at intake.
		writeErr(w, http.StatusLocked, "FROZEN", "the kill switch is engaged; advance proposals are stopped", nil)
	default:
		writeErr(w, http.StatusConflict, "NOT_READY", err.Error(), nil)
	}
}
