// Package ownerapi is the H2 owner surface: the SSE events stream and the approval
// endpoints (docs/handshakes/H2-owner-api.md). It is the one thing standing between an
// escalated intent and a signature, so its two H2 decisions are enforced here exactly as
// written: the decision POST binds to the intentHash the owner was SHOWN (stale views are
// rejected at click time, 409 STALE_VIEW), and the auth posture is explicit — loopback
// only; a non-loopback bind without a bearer token is refused at startup, not warned.
package ownerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Server serves the H2 owner API over one Lifecycle and the shared store.
type Server struct {
	life *approval.Lifecycle
	st   *store.Store
	log  *slog.Logger
	// pollEvery is the SSE reader's cadence over the events table — the same pattern as
	// the outbox publisher (the store is the bus's source of truth; seq gives replay and
	// Last-Event-ID for free).
	pollEvery time.Duration
}

// New builds the server.
func New(life *approval.Lifecycle, st *store.Store, log *slog.Logger) *Server {
	return &Server{life: life, st: st, log: log, pollEvery: 200 * time.Millisecond}
}

// Handler returns the H2 routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/approvals", s.handleApprovals)
	mux.HandleFunc("POST /api/v1/approvals/{id}/decision", s.handleDecision)
	mux.HandleFunc("GET /api/v1/events/stream", s.handleStream)
	return mux
}

// Run serves until ctx is cancelled (H2 §1). The loopback guard is enforced BEFORE
// listening: any non-loopback bind requires SNAPFALL_OWNER_TOKEN (>=32 bytes) — refusing
// to start beats serving a payment-approval surface to a network by accident.
func (s *Server) Run(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("owner api addr %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		if len(os.Getenv("SNAPFALL_OWNER_TOKEN")) < 32 {
			return fmt.Errorf("owner api: refusing non-loopback bind %q without SNAPFALL_OWNER_TOKEN (>=32 bytes) — H2 §1", addr)
		}
	}

	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.log.Info("owner api serving", "addr", addr)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// ── Approvals (H2 §3) ──────────────────────────────────────────────────────

type approvalView struct {
	RequestID     string `json:"requestId"`
	JobID         string `json:"jobId"`
	IntentHash    string `json:"intentHash"`
	Merchant      string `json:"merchant"`
	Resource      string `json:"resource"`
	AmountUSDC    string `json:"amountUsdc"`
	Purpose       string `json:"purpose"`
	ExpiresAt     string `json:"expiresAt"`
	AlternativeTo string `json:"alternativeTo"`
}

func viewOf(r approval.Request) approvalView {
	return approvalView{
		RequestID:  r.ID,
		JobID:      r.JobID,
		IntentHash: r.IntentHash,
		Merchant:   r.Intent.Merchant,
		Resource:   r.Intent.Resource,
		AmountUSDC: strconv.FormatInt(r.Intent.AmountMicros, 10),
		Purpose:    r.Intent.Purpose,
		ExpiresAt:  r.ExpiresAt.UTC().Format(time.RFC3339),

		AlternativeTo: r.Intent.AlternativeTo,
	}
}

func (s *Server) handleApprovals(w http.ResponseWriter, _ *http.Request) {
	pending := s.life.PendingRequests()
	views := make([]approvalView, 0, len(pending))
	for _, r := range pending {
		views = append(views, viewOf(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": views})
}

type decisionBody struct {
	Kind       string `json:"kind"`
	By         string `json:"by"`
	Reason     string `json:"reason"`
	IntentHash string `json:"intentHash"`
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body decisionBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "malformed JSON body", nil)
		return
	}
	kind, ok := map[string]approval.DecisionKind{
		"approve":             approval.DecideApprove,
		"reject":              approval.DecideReject,
		"request_alternative": approval.DecideRequestAlternative,
	}[body.Kind]
	if !ok || body.By == "" || body.IntentHash == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST",
			"decision requires kind (approve|reject|request_alternative), by, and the intentHash you were shown", nil)
		return
	}

	// H2 decision 2: the POST binds to the intent the owner was SHOWN. A stale view is
	// rejected at click time with the CURRENT view, not discovered at execution.
	snap, exists := s.life.Snapshot(id)
	if !exists {
		writeErr(w, http.StatusNotFound, "UNKNOWN_REQUEST", "no approval request with that id", nil)
		return
	}
	if snap.IntentHash != body.IntentHash {
		writeErr(w, http.StatusConflict, "STALE_VIEW",
			"the intent changed since this view was rendered; re-render and decide again", viewOf(snap))
		return
	}

	decided, err := s.life.Decide(r.Context(), id, kind, body.By, body.Reason)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"requestId": decided.ID, "state": string(decided.State),
			"decidedBy": decided.DecidedBy, "reason": decided.Reason,
		})
	case errors.Is(err, approval.ErrExpired):
		writeErr(w, http.StatusGone, "APPROVAL_EXPIRED", "the approval window elapsed before the decision", nil)
	case errors.Is(err, approval.ErrAlreadyDecided):
		writeErr(w, http.StatusConflict, "ALREADY_DECIDED", err.Error(), nil)
	case errors.Is(err, approval.ErrUnknownRequest):
		writeErr(w, http.StatusNotFound, "UNKNOWN_REQUEST", "no approval request with that id", nil)
	default:
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error(), nil)
	}
}

// ── The events stream (H2 §2) ──────────────────────────────────────────────

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	// Resume point: Last-Event-ID carries the last daemon seq the client saw.
	var lastSeq int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		lastSeq, _ = strconv.ParseInt(v, 10, 64)
	}

	// Snapshot first (H2 §2.1). Fields whose source is absent are null — the V5 mock's
	// fixtures are placeholders, not contract.
	snapshot := map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"pendingApprovals": len(s.life.PendingRequests()),
			"treasuryUsdc":     nil, "pool": nil, "openAdvances": nil, "activeJobs": nil,
		},
	}
	if err := sseWrite(w, fl, 0, snapshot); err != nil {
		return
	}

	// Live daemon events: read the events table by seq — the same store the outbox
	// publisher drains, so ordering and replay come from the log itself. Chain events
	// (source:"chain") are relayed here once the chain_* tables exist in this build's
	// schema (H2 §4); until then they simply do not occur.
	tick := time.NewTicker(s.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		rows, err := s.st.DB().QueryContext(r.Context(),
			`SELECT seq, ts, kind, COALESCE(entity_id,''), COALESCE(actor,''), COALESCE(payload_json,'')
			 FROM events WHERE seq > ? ORDER BY seq LIMIT 256`, lastSeq)
		if err != nil {
			return
		}
		type row struct {
			seq, ts             int64
			kind, entity, actor string
			payload             string
		}
		var batch []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.seq, &x.ts, &x.kind, &x.entity, &x.actor, &x.payload); err != nil {
				rows.Close()
				return
			}
			batch = append(batch, x)
		}
		rows.Close()
		for _, x := range batch {
			var payload any
			if x.payload != "" {
				_ = json.Unmarshal([]byte(x.payload), &payload)
			}
			msg := map[string]any{
				"kind": "event", "source": "daemon", "seq": x.seq,
				"event": map[string]any{
					"kind": x.kind, "jobId": x.entity, "actor": x.actor,
					"at":      time.UnixMilli(x.ts).UTC().Format(time.RFC3339),
					"payload": payload,
				},
			}
			if err := sseWrite(w, fl, x.seq, msg); err != nil {
				return
			}
			lastSeq = x.seq
		}
	}
}

func sseWrite(w http.ResponseWriter, fl http.Flusher, id int64, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, current any) {
	body := map[string]any{"error": map[string]any{"code": code, "message": msg}}
	if current != nil {
		body["current"] = current
	}
	writeJSON(w, status, body)
}
