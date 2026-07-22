// Package freeze is the G11 kill switch (AT-09, FR-APR-005, SEC-009, FR-PAY-008).
//
// A deterministic registry of frozen scopes — org, job, or agent — consulted by every
// gate that can start work or move money:
//
//   - new tasks:      brain.assign() (the single dispatch chokepoint) + owner intake
//   - new signatures: approval.Submit (before the nonce claim) and approval.Execute
//     (before the write-ahead claim and Grant minting — no Grant, no money)
//   - new advances:   NO advance-request path exists in the daemon yet (the advance is
//     a FloatPool contract call arriving with the Funding/H3 integration). AT-09's
//     advance clause is therefore STRUCTURALLY UNPROVEN here — recorded plainly, not
//     papered over. When the path lands it sits behind the same Grant chain this
//     package already gates, but that is a design intention, not a passing test.
//
// TIMING, honestly (SEC-009's "within 1 s"): enforcement is synchronous. Engage()
// appends the audit event and flips in-memory state BEFORE returning, and every gate
// consults that state on entry. The real guarantee is an ORDERING property, stronger
// than any wall-clock bound: no gated action that begins after Engage() returns can
// proceed. There is no polling loop and no propagation delay to measure.
//
// IN-FLIGHT WORK (the weaker promise, made visible): an execution that passed its
// gates before Engage() completes rather than aborting — aborting after the
// write-ahead claim recreates the exact double-pay hazard the claim exists to prevent.
// The owner is TOLD, not left to assume: Engage() probes how many executions are in
// flight, records the count in the freeze.engaged event, and the owner-facing Report
// carries it with the explanation.
//
// Freeze state REPLAYS from the event log on construction — a kill switch that
// silently lifts itself on restart is broken. Every Engage and Lift is an event in
// the tamper-evident log with actor, reason, and timestamp; both are idempotent
// (re-engaging an engaged scope is a recorded no-op).
package freeze

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Kind is the freeze scope kind (FR-PAY-008: global/job/agent; "global" = org here).
type Kind string

const (
	KindOrg   Kind = "org"
	KindJob   Kind = "job"
	KindAgent Kind = "agent"
)

// Entry is one active freeze.
type Entry struct {
	Kind             Kind      `json:"kind"`
	ID               string    `json:"id"`
	By               string    `json:"by"`
	Reason           string    `json:"reason"`
	At               time.Time `json:"at"`
	InFlightAtEngage int       `json:"in_flight_at_engage"`
}

func key(kind Kind, id string) string { return string(kind) + ":" + id }

// Registry holds active freezes and audits every change through the store.
type Registry struct {
	st    *store.Store
	clock func() time.Time
	// InFlightProbe reports executions currently past their gates (wired to
	// approval.Lifecycle.InFlight). Optional; nil probes as zero.
	InFlightProbe func() int

	mu     sync.Mutex
	active map[string]Entry
}

// NewRegistry builds a registry and REPLAYS freeze state from the event log, so a
// freeze engaged before a crash is still engaged after restart.
func NewRegistry(ctx context.Context, st *store.Store, clock func() time.Time) (*Registry, error) {
	r := &Registry{st: st, clock: clock, active: make(map[string]Entry)}
	if err := r.replay(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) replay(ctx context.Context) error {
	rows, err := r.st.DB().QueryContext(ctx,
		`SELECT kind, payload_json FROM events WHERE kind IN ('freeze.engaged','freeze.lifted') ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("replaying freeze state: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var eventKind, payload string
		if err := rows.Scan(&eventKind, &payload); err != nil {
			return err
		}
		var e Entry
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return fmt.Errorf("corrupt freeze event: %w", err)
		}
		switch eventKind {
		case "freeze.engaged":
			r.active[key(e.Kind, e.ID)] = e
		case "freeze.lifted":
			delete(r.active, key(e.Kind, e.ID))
		}
	}
	return rows.Err()
}

// Engage freezes a scope. Idempotent: an already-frozen scope is a recorded no-op.
// The in-flight execution count is probed and recorded so the owner KNOWS whether
// anything was mid-flight when the switch landed (it completes; see package doc).
func (r *Registry) Engage(ctx context.Context, kind Kind, id, by, reason string) (Entry, error) {
	if id == "" || by == "" {
		return Entry{}, fmt.Errorf("freeze: scope id and actor are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.active[key(kind, id)]; ok {
		// Recorded no-op: the audit trail shows the repeated command.
		r.append(ctx, "freeze.engaged.duplicate", existing)
		return existing, nil
	}

	inFlight := 0
	if r.InFlightProbe != nil {
		inFlight = r.InFlightProbe()
	}
	e := Entry{Kind: kind, ID: id, By: by, Reason: reason, At: r.clock().UTC(), InFlightAtEngage: inFlight}

	// Event FIRST (durable, transactional with its outbox row), then the flag. A crash
	// between the two re-engages on replay — fail-frozen, never fail-open.
	if err := r.append(ctx, "freeze.engaged", e); err != nil {
		return Entry{}, err
	}
	r.active[key(kind, id)] = e
	return e, nil
}

// Lift unfreezes a scope. Owner-surface capability: the registry handle is held by
// the owner surface and Brain, never by workers (same placement as the funding
// pointer). Audited like Engage; lifting a non-frozen scope is a recorded no-op.
//
// SCOPE HIERARCHY: Lift removes exactly the named scope. Work covered by a BROADER
// still-active freeze stays frozen — Check consults every applicable scope, so
// lifting a job freeze under a frozen org changes nothing until the org lifts too.
func (r *Registry) Lift(ctx context.Context, kind Kind, id, by, reason string) error {
	if id == "" || by == "" {
		return fmt.Errorf("freeze: scope id and actor are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.active[key(kind, id)]
	if !ok {
		r.append(ctx, "freeze.lifted.duplicate", Entry{Kind: kind, ID: id, By: by, Reason: reason, At: r.clock().UTC()})
		return nil
	}
	e.By, e.Reason, e.At = by, reason, r.clock().UTC()
	if err := r.append(ctx, "freeze.lifted", e); err != nil {
		return err
	}
	delete(r.active, key(kind, id))
	return nil
}

// Check returns the covering freeze entry for the given identifiers, broadest scope
// first (org beats job beats agent), or nil when the scope is clear. Empty
// identifiers skip their level.
func (r *Registry) Check(org, job, agent string) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, k := range []string{key(KindOrg, org), key(KindJob, job), key(KindAgent, agent)} {
		if e, ok := r.active[k]; ok && e.ID != "" {
			return &e
		}
	}
	return nil
}

// Err converts a Check hit into the machine-readable refusal every gate returns.
func Err(e *Entry) error {
	return fmt.Errorf("frozen: %s %q is frozen by %s (%s) since %s — the kill switch is engaged",
		e.Kind, e.ID, e.By, e.Reason, e.At.Format(time.RFC3339))
}

// Report is the owner-facing freeze state (FR-APR-005: read-only inspection remains —
// this call works while everything else is frozen).
type Report struct {
	Active []Entry `json:"active"`
	// InFlightNote is non-empty when any active freeze landed with executions in
	// flight: the owner is told those completed rather than aborting, and why.
	InFlightNote string `json:"in_flight_note,omitempty"`
}

// StatusReport builds the owner-facing view of the kill switch.
func (r *Registry) StatusReport() Report {
	r.mu.Lock()
	defer r.mu.Unlock()

	rep := Report{}
	inFlight := 0
	for _, e := range r.active {
		rep.Active = append(rep.Active, e)
		inFlight += e.InFlightAtEngage
	}
	if inFlight > 0 {
		rep.InFlightNote = fmt.Sprintf(
			"%d execution(s) were in flight when the freeze engaged; they COMPLETED rather than aborting — "+
				"aborting after the signing claim risks a double-pay. No new execution has started since.", inFlight)
	}
	return rep
}

// append writes one audit event through the transactional store.
func (r *Registry) append(ctx context.Context, kind string, e Entry) error {
	_, err := r.st.Append(ctx, store.Event{Kind: kind, EntityID: e.ID, Actor: e.By, Payload: e})
	return err
}
