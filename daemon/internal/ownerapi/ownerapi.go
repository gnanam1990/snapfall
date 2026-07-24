// Package ownerapi is the H2 owner surface: the SSE events stream and the approval
// endpoints (docs/handshakes/H2-owner-api.md). It is the one thing standing between an
// escalated intent and a signature, so its two H2 decisions are enforced here exactly as
// written: the decision POST binds to the intentHash the owner was SHOWN (stale views are
// rejected at click time, 409 STALE_VIEW), and the auth posture is explicit — loopback
// only; a non-loopback bind without a bearer token is refused at startup, not warned.
package ownerapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Server serves the H2 owner API over one Lifecycle and the shared store.
type Server struct {
	life *approval.Lifecycle
	st   *store.Store
	log  *slog.Logger
	// token is SNAPFALL_OWNER_TOKEN, captured at construction. When set it is ENFORCED on
	// every request (bearer auth, constant-time) — not a startup permission slip. When
	// empty, the posture is loopback-only and Run refuses any non-loopback bind (H2 §1).
	token string
	// pollEvery is the SSE reader's cadence over the events table — the same pattern as
	// the outbox publisher (the store is the bus's source of truth; seq gives replay and
	// Last-Event-ID for free).
	pollEvery time.Duration
	// Generate is the invoice-generation seam (H2 §4.1), wired by the daemon to Brain's
	// GenerateInvoice with the owner-request trigger. Kept as a seam so this package
	// never holds the billing agent — Brain does, from its single pinned site. Nil until
	// wired: POST then refuses honestly (503 NOT_WIRED) while reads still work.
	Generate func(ctx context.Context, jobID string) (billing.Record, error)

	// The customer-surface seams (accept.go), wired to Brain: MintAccept (owner-gated
	// mint/rotate), VerifyAccept (constant-time per-job credential check), Accept (the
	// state/freeze/exactly-once transition), JobState (the customer's read). Nil seams
	// REFUSE (503) — the surface never fails open.
	MintAccept   func(ctx context.Context, jobID string) (string, error)
	VerifyAccept func(jobID, token string) bool
	Accept       func(ctx context.Context, jobID string) (string, error)
	JobState     func(jobID string) (string, bool)

	// ProposeAdvance is the owner-initiated advance trigger (the snap's proposal),
	// wired to Brain's single site. The proposal lands in THIS API's own approvals
	// inbox pre-marked for human approval — proposing and approving are two separate
	// owner actions, on the record.
	ProposeAdvance func(ctx context.Context, jobID string) (approval.Request, error)

	// WorkerCatalog is the reviewed manifest gallery exposed to the owner dashboard.
	// HireWorker is the activation seam; the daemon wires it to Brain so the API never
	// constructs or directly controls a worker.
	WorkerCatalog         []WorkerManifest
	HireWorker            func(ctx context.Context, req HireWorkerRequest) (HireWorkerResult, error)
	ListWorkerActivations func(ctx context.Context) ([]WorkerActivation, error)
}

// WorkerManifest is the owner-facing, capability-focused projection of one hireable
// worker manifest. It intentionally excludes implementation and secret material.
type WorkerManifest struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	Description   string   `json:"description"`
	Permissions   []string `json:"permissions"`
	ChecklistPath string   `json:"checklistPath,omitempty"`
}

// HireWorkerRequest carries the owner-confirmed configuration for one worker.
type HireWorkerRequest struct {
	ManifestID string `json:"manifestId"`
	Repository string `json:"repository"`
	QuoteUSDC  string `json:"quoteUsdc"`
	By         string `json:"by"`
}

// HireWorkerResult identifies the newly activated watcher cycle.
type HireWorkerResult struct {
	JobID      string `json:"jobId"`
	VaultJobID string `json:"vaultJobId"`
	State      string `json:"state"`
}

// WorkerActivation is one durable manifest activation rendered after dashboard reloads.
type WorkerActivation struct {
	ManifestID string `json:"manifestId"`
	Repository string `json:"repository"`
	QuoteUSDC  string `json:"quoteUsdc"`
	JobID      string `json:"jobId"`
	VaultJobID string `json:"vaultJobId"`
	State      string `json:"state"`
}

// New builds the server.
func New(life *approval.Lifecycle, st *store.Store, log *slog.Logger) *Server {
	return &Server{life: life, st: st, log: log, token: os.Getenv("SNAPFALL_OWNER_TOKEN"), pollEvery: 200 * time.Millisecond}
}

// Handler returns the H2 routes. TWO PRINCIPALS, two auth domains, one handler: owner
// routes live behind withAuth (the owner bearer); the customer routes live behind
// withCustomerAuth (the per-job accept credential) and are NOT under the owner token —
// the settlement principal is the customer, and neither credential opens the other's
// surface.
func (s *Server) Handler() http.Handler {
	owner := http.NewServeMux()
	owner.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	owner.HandleFunc("GET /api/v1/approvals", s.handleApprovals)
	owner.HandleFunc("POST /api/v1/approvals/{id}/decision", s.handleDecision)
	owner.HandleFunc("GET /api/v1/events/stream", s.handleStream)
	owner.HandleFunc("POST /api/v1/jobs/{id}/invoice", s.handleInvoiceGenerate)
	owner.HandleFunc("GET /api/v1/jobs/{id}/invoice", s.handleInvoiceLatest)
	owner.HandleFunc("POST /api/v1/jobs/{id}/accept-link", s.handleMintAcceptLink)
	owner.HandleFunc("POST /api/v1/jobs/{id}/advance", s.handleProposeAdvance)
	owner.HandleFunc("GET /api/v1/workforce/manifests", s.handleWorkerManifests)
	owner.HandleFunc("GET /api/v1/workforce/activations", s.handleWorkerActivations)
	owner.HandleFunc("POST /api/v1/workforce/{id}/hire", s.handleHireWorker)

	root := http.NewServeMux()
	root.HandleFunc("POST /api/v1/customer/jobs/{id}/accept", s.withCustomerAuth(s.handleCustomerAccept))
	root.HandleFunc("GET /api/v1/customer/jobs/{id}/acceptance", s.withCustomerAuth(s.handleCustomerAcceptance))
	root.HandleFunc("GET /api/v1/customer/jobs/{id}/invoice", s.withCustomerAuth(s.handleCustomerInvoice))
	root.Handle("/", s.withAuth(owner))
	return root
}

func (s *Server) handleWorkerManifests(w http.ResponseWriter, _ *http.Request) {
	manifests := s.WorkerCatalog
	if manifests == nil {
		manifests = []WorkerManifest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": manifests})
}

func (s *Server) handleWorkerActivations(w http.ResponseWriter, r *http.Request) {
	if s.ListWorkerActivations == nil {
		writeJSON(w, http.StatusOK, map[string]any{"activations": []WorkerActivation{}})
		return
	}
	activations, err := s.ListWorkerActivations(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ACTIVATIONS_UNAVAILABLE", err.Error(), nil)
		return
	}
	if activations == nil {
		activations = []WorkerActivation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"activations": activations})
}

func (s *Server) handleHireWorker(w http.ResponseWriter, r *http.Request) {
	var body HireWorkerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "malformed JSON body", nil)
		return
	}
	body.ManifestID = strings.TrimSpace(r.PathValue("id"))
	body.Repository = strings.TrimSpace(body.Repository)
	body.QuoteUSDC = strings.TrimSpace(body.QuoteUSDC)
	body.By = strings.TrimSpace(body.By)
	if body.ManifestID == "" || body.Repository == "" || body.QuoteUSDC == "" || body.By == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST",
			"hire requires a manifest id, repository, quoteUsdc, and owner identity", nil)
		return
	}
	if s.HireWorker == nil {
		writeErr(w, http.StatusServiceUnavailable, "NOT_WIRED",
			"worker activation is not wired in this build", nil)
		return
	}
	result, err := s.HireWorker(r.Context(), body)
	if err != nil {
		writeErr(w, http.StatusConflict, "HIRE_FAILED", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// withAuth enforces the bearer token on EVERY route whenever a token is configured —
// closing the gap the security review named: a token that only gates startup while
// requests go unauthenticated is an auth bypass, not auth. With no token configured the
// surface is loopback-only (Run refuses other binds) and requests carry no credential;
// `by` on a decision is then a recorded label, not an authenticated identity — exactly
// the written H2 §1 posture, no more.
func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.token == "" {
		return next
	}
	want := []byte(s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, prefix)), want) != 1 {
			writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "bearer token required", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
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
		if len(s.token) < 32 {
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

// ── Invoices (H2 §4) ───────────────────────────────────────────────────────

// ownerInvoiceView projects a Record for the wire: version, trigger, the OWNER copy,
// the reconciliation, and the alerts. The customer copy is deliberately absent — the
// copy-serving seam is undecided (H2 §4.2), so no daemon route serves it; putting it
// on this wire "because the owner is trusted anyway" is how an unserved copy quietly
// becomes a served one.
func ownerInvoiceView(rec billing.Record) map[string]any {
	return map[string]any{
		"version": rec.Version, "trigger": rec.Trigger,
		"invoice":        rec.Owner,
		"reconciliation": rec.Reconciliation,
		"alerts":         rec.Alerts,
	}
}

// POST /api/v1/jobs/{id}/invoice — generate now (H2 §4.1): the owner-request trigger.
func (s *Server) handleInvoiceGenerate(w http.ResponseWriter, r *http.Request) {
	if s.Generate == nil {
		writeErr(w, http.StatusServiceUnavailable, "NOT_WIRED", "invoice generation is not wired in this build", nil)
		return
	}
	rec, err := s.Generate(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, ownerInvoiceView(rec))
	case errors.Is(err, billing.ErrUnknownJob):
		writeErr(w, http.StatusNotFound, "UNKNOWN_JOB", "the daemon has no such job", nil)
	default:
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error(), nil)
	}
}

// GET /api/v1/jobs/{id}/invoice — the latest recorded version (H2 §4.2). Never
// generates: reads the durable billing.invoice log the same way the stream does.
func (s *Server) handleInvoiceLatest(w http.ResponseWriter, r *http.Request) {
	var payload string
	err := s.st.DB().QueryRowContext(r.Context(),
		`SELECT payload_json FROM events WHERE kind='billing.invoice' AND entity_id=?
		 ORDER BY seq DESC LIMIT 1`, r.PathValue("id")).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "NO_INVOICE", "no invoice version has been generated for this job", nil)
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
	writeJSON(w, http.StatusOK, ownerInvoiceView(rec))
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
