// Package budget is the reservation ledger: the daemon's only cumulative spend truth,
// and the thing that makes policy rule 1 (job-budget) and rule 3 (daily-cap) live
// (FR-PAY-006 "reserve budget when signing", FR-PAY-007 the release half, H3 §7 step 2
// "hold intent.maxAmount pre-sign against the per-job budget").
//
// WHY A FOLD OF THE EVENT LOG AND NOT A TABLE. Both rival stores are disqualified by a
// fact, not a preference:
//
//   - jobs.committed_spend_usdc / settled_spend_usdc exist (store/schema.sql:21) but
//     brain/jobs_projection_test.go:74-101 fails the build if the jobs projection gains a
//     second write site or is ever read as truth, and brain/project.go:20-27 declares the
//     file-based JobMemory the single source. Those columns stay dead on purpose; filling
//     them is a change to Brain's projection, never a change here.
//   - payment_intents would need max_amount_micros / payment_id columns and real status
//     transitions, and there is NO ALTER TABLE path: store.Open applies schema.sql
//     wholesale and every statement is CREATE TABLE IF NOT EXISTS (store.go:27,40), so a
//     new column is a silent no-op on every existing .db file. The row is a nonce claim
//     (approval/lifecycle.go:679-695) and nothing in the repo ever UPDATEs it. Its
//     amount_usdc is a formatted decimal STRING (policy.FormatUSDC) and its
//     policy_version INTEGER column receives the string "pol_7", so numeric comparison on
//     that table is already broken. Do not build money truth on it.
//
// A fold replayed at boot is also exactly this codebase's existing posture:
// approval.Lifecycle.Recover and freeze.NewRegistry both rebuild from the log, and
// store.Append writes the event and its outbox row in ONE transaction, so every hold
// transition is atomic with bus publication for free.
//
// THE FOUR KINDS. Three are written here:
//
//	budget.reserved   opens a hold   {request_id, intent_id, job_id, org_id,
//	                                  reserved_micros, payment_id, nonce, merchant,
//	                                  pay_to, expires_at}   expires_at = unix SECONDS
//	budget.committed  hold -> spend  {request_id, job_id, committed_micros,
//	                                  released_micros, payment_id, receipt_hash, state}
//	budget.released   hold -> gone   {request_id, job_id, released_micros, payment_id,
//	                                  reason, code, pre_sign}
//
// The fourth is READ and never written: payment.executing, the approval lifecycle's
// write-ahead execution claim (approval/lifecycle.go:554). The ledger needs it to tell
// "reserved but never attempted" from "reserved and possibly SIGNED", which is the whole
// difference between a hold that is safe to reclaim and one that must never be touched.
// That is a deliberate cross-package read of an approval event kind, in the same spirit
// as billing reading chain events, and it is named here rather than hidden.
//
// KEYED BY request_id, SET-BASED. A hold is OPEN iff a budget.reserved exists for its
// request_id and no budget.committed or budget.released carries the same request_id.
// Because it is a set membership and not a counter, a duplicate release folds to one
// closed hold instead of refunding the budget twice. request_id is the natural
// idempotency key: the lifecycle already mints exactly one request per intent.
//
// All amounts are int64 micro-USDC. No float ever touches money here.
//
// THE THREE RECLAIM PATHS, because an unreclaimable hold permanently shrinks the budget:
//
//  1. Sweep, for holds that were never attempted. expires_at past AND no
//     payment.executing claim means Execute can never run it (lifecycle.go:514-519
//     refuses execution past expiry unconditionally), so the hold is PROVABLY dead and
//     releasing it needs no external check. This does not depend on approval.expired
//     ever being appended, which is why it is correct where an event-triggered release
//     would leak: today a request that expires with nobody waiting on it is never marked
//     expired at all.
//  2. The boot status reconciler, for a crash between reserve and pay. Unresolved()
//     yields exactly the set to poll; the caller commits, releases or leaves held per the
//     sidecar's durable record.
//  3. The reconcile deadline, which is an honest stop and NOT an auto-release. Nothing in
//     the sidecar ever advances RECONCILING, and H3 forbids releasing on a bare
//     FACILITATOR_ERROR because the signed authorization is a bearer instrument that may
//     still settle. StalledSince yields the hold, NoteReconcileStalled records it, and the
//     owner decides. A post-sign hold is never released by machinery.
//
// This package deliberately imports NEITHER approval NOR funding, so the ledger stays
// foldable and independently testable against nothing but a *store.Store and a clock.
// The Intent-to-Hold translation therefore lives in the wiring, not in here.
package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Event kinds. All carry EntityID = jobID and Actor = "funding": the log's majority
// convention (approval's appendEvent and advancing's advance.* events are all job-keyed;
// purchase.pending_settlement is the outlier), so the fold needs one predicate on
// entity_id and the request id rides the payload.
const (
	// KindReserved opens a hold against a job budget.
	KindReserved = "budget.reserved"
	// KindCommitted converts an open hold into settled spend at the paid figure and
	// releases the remainder in the same event.
	KindCommitted = "budget.committed"
	// KindReleased returns an open hold to the budget in full.
	KindReleased = "budget.released"
	// KindReconcileStalled records a post-sign hold that has outlived the reconcile
	// deadline. It is a RECORD, not a transition: the hold stays open and only an owner
	// action closes it.
	KindReconcileStalled = "budget.reconcile_stalled"
	// KindChainDivergence records that a job's on-chain operating budget disagrees with
	// the daemon's policy budget. Also a record, not a transition (the two budgets are
	// deliberately left unreconciled; resolving them is a policy-engine task).
	KindChainDivergence = "budget.chain_divergence"
)

// kindPaymentExecuting is the approval lifecycle's write-ahead execution claim
// (approval/lifecycle.go:554), which is durable BEFORE the executor runs. The ledger
// reads it and never writes it: it is the only evidence that a hold may already be
// signed, and therefore the one thing standing between Sweep and a released bearer
// instrument.
const kindPaymentExecuting = "payment.executing"

// actor tags every ledger event. Funding owns money movement, so it owns the record of
// money held.
const actor = "funding"

// CodeExpiredUnattempted is the release code Sweep records: the approval window elapsed
// with no execution claim, so this hold can never be paid.
const CodeExpiredUnattempted = "EXPIRED_UNATTEMPTED"

// sweepReason is the human-readable half of a Sweep release.
const sweepReason = "expired with no execution claim: the approval window elapsed and no " +
	"payment.executing exists, so Execute can never run this intent (lifecycle refuses " +
	"execution past expiry unconditionally)"

// Sentinel errors. Every one fails closed and says why, rather than returning a bare
// refusal a caller might mistake for success.
var (
	// ErrInvalidHold refuses a reservation that could not be reclaimed later.
	ErrInvalidHold = errors.New("invalid-hold: a reservation needs a request id, a job id, a positive micro amount and a non-zero expiry (an expiry-less hold can never be proven dead, so Sweep could never reclaim it)")
	// ErrNoSuchHold refuses closing a hold that was never opened.
	ErrNoSuchHold = errors.New("no-such-hold: no budget.reserved is folded for that request id; refusing to close a hold that was never opened")
	// ErrHoldClosed refuses reopening a terminal hold.
	ErrHoldClosed = errors.New("hold-closed: that request's hold is already committed or released; the fold is set-based and a closed hold cannot be reopened")
	// ErrCommitAfterRelease is the loud case: the ledger declared a hold released, and
	// then the payment turned out to have settled after all.
	ErrCommitAfterRelease = errors.New("commit-after-release: this hold was already RELEASED and the payment now reports settled spend; the budget figure and the sidecar's record disagree and an owner must reconcile them (no release is fabricated and no commit is silently dropped)")
)

// Hold is one reservation: the durable record budget.reserved carries, plus the two
// fields the fold derives rather than reads.
type Hold struct {
	// RequestID is the approval request this hold belongs to, and the ledger's
	// idempotency key. Exactly one hold per request, ever.
	RequestID string `json:"request_id"`
	// IntentID is the payment intent the request was opened for.
	IntentID string `json:"intent_id"`
	// JobID is the budget the hold is taken against, and the event's EntityID.
	JobID string `json:"job_id"`
	// OrgID scopes the daily cumulative window (policy rule 3 is org-wide).
	OrgID string `json:"org_id"`
	// ReserveMicros is the figure held: H3 §7 step 2's intent.maxAmount, in micro-USDC.
	ReserveMicros int64 `json:"reserved_micros"`
	// PaymentID is the deterministic H3 payment id, PRECOMPUTED at reserve time. It is
	// persisted rather than derived because the boot reconciler must be able to reach
	// GET /v1/status even when the pay response is lost and the 409 envelope carries
	// paymentId: null.
	PaymentID string `json:"payment_id"`
	// Nonce is the intent nonce, so a recovery path can recompute the payment id and
	// compare it against the persisted one.
	Nonce string `json:"nonce"`
	// Merchant is the policy-facing merchant HOST (what the allowlist matches).
	Merchant string `json:"merchant"`
	// PayTo is the x402 payee ADDRESS the wire intent carries.
	PayTo string `json:"pay_to"`
	// ExpiresAt is the intent's expiry, at SECOND precision (the wire's precision).
	// Sweep's only proof that a hold is dead.
	ExpiresAt time.Time `json:"expires_at"`
	// ReservedAt is the budget.reserved event's timestamp at millisecond precision, and
	// the key for the daily window (policy.DailyWindowStartUTC). The ledger stamps it
	// from its clock; a value passed to Reserve is IGNORED, so a caller can neither
	// backdate a hold out of today's window nor forward-date one into tomorrow's.
	ReservedAt time.Time `json:"reserved_at"`
}

// entry is the folded state of one request id. A claim or a close may be folded for a
// request whose reserve has not been seen (a corrupt or truncated log), so reserved is
// tracked explicitly rather than inferred from a non-empty hold.
type entry struct {
	hold     Hold
	reserved bool   // a budget.reserved landed
	claimed  bool   // a payment.executing landed: this hold may already be signed
	closedBy string // "" open, else KindCommitted or KindReleased (FIRST close wins)
	// committedMicros is what actually moved. Zero for a released hold, which is why a
	// released hold contributes nothing to either sum.
	committedMicros int64
}

func (e *entry) open() bool { return e.reserved && e.closedBy == "" }

// jobGate serializes intake for one job. waiters is counted under the ledger's gate lock
// so the map entry cannot be deleted while another intake is blocked on it.
type jobGate struct {
	mu      sync.Mutex
	waiters int
}

// Ledger is the reservation ledger: the daemon's only cumulative spend truth. It is
// FOLDED from the event log at boot, never a table (see the package doc for why both
// rival tables were disqualified).
type Ledger struct {
	st    *store.Store
	clock func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	// jobOrg remembers which org a job's holds were reserved under, first writer wins.
	// A map lookup rather than a scan, so Spend's daily scoping is deterministic and does
	// not depend on Go's randomized map iteration order.
	jobOrg map[string]string

	gmu   sync.Mutex
	gates map[string]*jobGate
}

// New builds a ledger over the given store and clock. The clock is injected so expiry
// and daily-window behaviour is deterministic in tests, never sleep-based; a nil clock
// defaults to time.Now().UTC() rather than panicking on a money path.
func New(st *store.Store, clock func() time.Time) *Ledger {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Ledger{
		st:      st,
		clock:   clock,
		entries: make(map[string]*entry),
		jobOrg:  make(map[string]string),
		gates:   make(map[string]*jobGate),
	}
}

// Recover folds budget.reserved / budget.committed / budget.released plus the
// lifecycle's payment.executing claim, ORDER BY seq.
//
// Idempotent over the same log: the fold builds a FRESH state and swaps it in, so
// replaying does not merge into what was already there (a merge would be idempotent for
// these three kinds today, but only by accident of them all being monotone).
//
// Call once at boot, before serving, and AFTER approval.Lifecycle.Recover: the lifecycle
// owns request state, the ledger only owns money held against it. A concurrent Reserve
// during Recover would be discarded by the swap, which is exactly why this is a
// before-serving call.
func (l *Ledger) Recover(ctx context.Context) error {
	entries, jobOrg, err := l.fold(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.entries, l.jobOrg = entries, jobOrg
	l.mu.Unlock()
	return nil
}

// eventPayload is every payload key the fold reads, across all four kinds. Unknown keys
// (payment.executing's intent_hash, budget.released's reason/code/pre_sign) are ignored
// by encoding/json, which is what lets one struct serve every kind.
type eventPayload struct {
	RequestID       string `json:"request_id"`
	IntentID        string `json:"intent_id"`
	JobID           string `json:"job_id"`
	OrgID           string `json:"org_id"`
	ReservedMicros  int64  `json:"reserved_micros"`
	PaymentID       string `json:"payment_id"`
	Nonce           string `json:"nonce"`
	Merchant        string `json:"merchant"`
	PayTo           string `json:"pay_to"`
	ExpiresAt       int64  `json:"expires_at"` // unix SECONDS
	CommittedMicros int64  `json:"committed_micros"`
}

// fold replays the ledger kinds into fresh state. It takes no lock: the caller owns the
// swap.
func (l *Ledger) fold(ctx context.Context) (map[string]*entry, map[string]string, error) {
	rows, err := l.st.DB().QueryContext(ctx, `SELECT kind, ts, payload_json FROM events
		WHERE kind IN ('budget.reserved','budget.committed','budget.released','payment.executing')
		ORDER BY seq`)
	if err != nil {
		return nil, nil, fmt.Errorf("folding the reservation ledger: %w", err)
	}
	defer rows.Close()

	entries := make(map[string]*entry)
	jobOrg := make(map[string]string)

	for rows.Next() {
		var kind string
		var ts int64
		var payload string
		if err := rows.Scan(&kind, &ts, &payload); err != nil {
			return nil, nil, err
		}
		var p eventPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return nil, nil, fmt.Errorf("corrupt %s event: %w", kind, err)
		}
		// Every ledger kind is keyed by request_id. A payload without one names no hold
		// and is skipped rather than guessed at.
		if p.RequestID == "" {
			continue
		}

		e := entries[p.RequestID]
		if e == nil {
			e = &entry{}
			entries[p.RequestID] = e
		}

		switch kind {
		case KindReserved:
			e.reserved = true
			e.hold = Hold{
				RequestID:     p.RequestID,
				IntentID:      p.IntentID,
				JobID:         p.JobID,
				OrgID:         p.OrgID,
				ReserveMicros: p.ReservedMicros,
				PaymentID:     p.PaymentID,
				Nonce:         p.Nonce,
				Merchant:      p.Merchant,
				PayTo:         p.PayTo,
				ExpiresAt:     expiryFromUnix(p.ExpiresAt),
				ReservedAt:    time.UnixMilli(ts).UTC(),
			}
			if p.JobID != "" && p.OrgID != "" {
				if _, known := jobOrg[p.JobID]; !known {
					jobOrg[p.JobID] = p.OrgID
				}
			}
		case KindCommitted:
			// FIRST close wins, so a commit folded after a release does not overwrite the
			// released figure. The disagreement is surfaced by Commit at write time
			// (ErrCommitAfterRelease), not silently resolved during a replay.
			if e.closedBy == "" {
				e.closedBy = KindCommitted
				e.committedMicros = p.CommittedMicros
			}
		case KindReleased:
			if e.closedBy == "" {
				e.closedBy = KindReleased
			}
		case kindPaymentExecuting:
			e.claimed = true
		}
	}
	return entries, jobOrg, rows.Err()
}

// expiryFromUnix converts the wire's unix-seconds expiry back to a time. A missing or
// zero expiry stays the ZERO time, which Sweep treats as "not provably dead" rather than
// as 1970: a malformed reserve event must not become a licence to release a live hold.
func expiryFromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// Reserve holds h.ReserveMicros against h.JobID for h.RequestID, durably.
//
// Idempotent by RequestID: a second Reserve for a request that already holds appends
// nothing and returns nil, so a retried intake cannot double-hold. A Reserve for a
// request whose hold already CLOSED is refused with ErrHoldClosed, because a set-based
// fold cannot reopen it and returning nil would report a hold that does not exist.
//
// There is no cap check here. The cap check stays in policy.Evaluate, which is a
// documented pure function; this records the hold that the engine's NEXT evaluation
// sees. Reserve can only make the engine deny more, never less.
func (l *Ledger) Reserve(ctx context.Context, h Hold) error {
	if h.RequestID == "" || h.JobID == "" || h.ReserveMicros <= 0 || h.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: request %q job %q micros %d expiry %v",
			ErrInvalidHold, h.RequestID, h.JobID, h.ReserveMicros, h.ExpiresAt)
	}

	// The check and the append are ONE critical section. A raced duplicate would fold to
	// a single hold anyway (set semantics), but the log would then claim two
	// reservations, and the log is the audit record.
	l.mu.Lock()
	defer l.mu.Unlock()

	if e := l.entries[h.RequestID]; e != nil {
		if e.closedBy != "" {
			return fmt.Errorf("%w: request %s was closed by %s", ErrHoldClosed, h.RequestID, e.closedBy)
		}
		if e.reserved {
			return nil // already held: a recorded no-op, not an error
		}
	}

	// The event ts and the in-memory ReservedAt must be the SAME instant, or a folded
	// hold would differ from a live one and Recover would not be state-preserving. The
	// log stores unix millis, so the in-memory copy is truncated to millis too.
	ts := l.clock().UTC()
	h.ReservedAt = time.UnixMilli(ts.UnixMilli()).UTC()
	// Second precision, matching the wire and the persisted expires_at.
	h.ExpiresAt = time.Unix(h.ExpiresAt.Unix(), 0).UTC()

	// Durable BEFORE visible, the same posture as the lifecycle's approval.requested
	// append: the fold is what recovery rebuilds from, so the hold must land in the log
	// before anything can read it out of memory.
	if _, err := l.st.Append(ctx, store.Event{
		TS:       ts,
		Kind:     KindReserved,
		EntityID: h.JobID,
		Actor:    actor,
		Payload: map[string]any{
			"request_id":      h.RequestID,
			"intent_id":       h.IntentID,
			"job_id":          h.JobID,
			"org_id":          h.OrgID,
			"reserved_micros": h.ReserveMicros,
			"payment_id":      h.PaymentID,
			"nonce":           h.Nonce,
			"merchant":        h.Merchant,
			"pay_to":          h.PayTo,
			"expires_at":      h.ExpiresAt.Unix(),
		},
	}); err != nil {
		return fmt.Errorf("refusing to hold budget without a durable record: %w", err)
	}

	// h is already a copy (value parameter), so the ledger owns this hold. An entry may
	// already exist WITHOUT a reserve if refreshClaims folded a claim for an unknown
	// request from a corrupt log; mutate it rather than replacing it, so that claim
	// survives and Sweep still refuses to touch the hold.
	if e := l.entries[h.RequestID]; e != nil {
		e.hold, e.reserved = h, true
	} else {
		l.entries[h.RequestID] = &entry{hold: h, reserved: true}
	}
	if h.OrgID != "" {
		if _, known := l.jobOrg[h.JobID]; !known {
			l.jobOrg[h.JobID] = h.OrgID
		}
	}
	return nil
}

// Commit converts an open hold into settled spend at paidMicros and releases the
// remainder, in one event.
//
// Idempotent by requestID: a duplicate commit is a no-op, which is what makes the W4
// crash window (200 DELIVERED observed, budget.committed not yet written) safe to replay
// from the sidecar's durable record. A commit landing on a RELEASED hold is NOT a no-op:
// it means money moved after the ledger declared the hold dead, so it returns
// ErrCommitAfterRelease for an owner to reconcile rather than quietly picking a winner.
//
// paidMicros above the reserved figure is recorded as-is with a zero remainder. The
// sidecar refuses an overpay pre-sign, so this is a defensive branch, and if it ever
// fires the money already moved: the ledger records what happened, never a negative
// release.
func (l *Ledger) Commit(ctx context.Context, requestID string, paidMicros int64, paymentID, receiptHash, state string) error {
	if requestID == "" || paidMicros < 0 {
		return fmt.Errorf("%w: commit needs a request id and a non-negative paid amount (got %q, %d)",
			ErrInvalidHold, requestID, paidMicros)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.entries[requestID]
	if e == nil || !e.reserved {
		return fmt.Errorf("%w: %s", ErrNoSuchHold, requestID)
	}
	switch e.closedBy {
	case KindCommitted:
		return nil // already settled: the replay-safe no-op W4 depends on
	case KindReleased:
		return fmt.Errorf("%w: request %s, %d micros reported paid", ErrCommitAfterRelease, requestID, paidMicros)
	}

	released := e.hold.ReserveMicros - paidMicros
	if released < 0 {
		released = 0
	}

	if _, err := l.st.Append(ctx, store.Event{
		TS:       l.clock().UTC(),
		Kind:     KindCommitted,
		EntityID: e.hold.JobID,
		Actor:    actor,
		Payload: map[string]any{
			"request_id":       requestID,
			"job_id":           e.hold.JobID,
			"committed_micros": paidMicros,
			"released_micros":  released,
			"payment_id":       paymentID,
			"receipt_hash":     receiptHash,
			"state":            state,
		},
	}); err != nil {
		// The hold STANDS on an append failure. Leaving budget held is conservative;
		// dropping the hold on a failed audit write would lose the spend entirely.
		return fmt.Errorf("committing hold %s (hold stands): %w", requestID, err)
	}

	e.closedBy = KindCommitted
	e.committedMicros = paidMicros
	return nil
}

// Release returns an open hold to the budget in full. reason is free text for the owner,
// code is the machine-readable H3 error code (or CodeExpiredUnattempted from Sweep), and
// preSign records that nothing was signed, which is the only condition under which
// releasing is provably safe.
//
// Releasing a hold that is already closed is a recognized no-op, not an error: the fold
// is set membership, so a duplicate release cannot refund the budget twice, and a caller
// racing the sweeper must not be punished for it.
func (l *Ledger) Release(ctx context.Context, requestID, reason, code string, preSign bool) error {
	if requestID == "" {
		return fmt.Errorf("%w: release needs a request id", ErrInvalidHold)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.entries[requestID]
	if e == nil || !e.reserved {
		return fmt.Errorf("%w: %s", ErrNoSuchHold, requestID)
	}
	if e.closedBy != "" {
		return nil
	}

	if _, err := l.st.Append(ctx, store.Event{
		TS:       l.clock().UTC(),
		Kind:     KindReleased,
		EntityID: e.hold.JobID,
		Actor:    actor,
		Payload: map[string]any{
			"request_id":      requestID,
			"job_id":          e.hold.JobID,
			"released_micros": e.hold.ReserveMicros,
			"payment_id":      e.hold.PaymentID,
			"reason":          reason,
			"code":            code,
			"pre_sign":        preSign,
		},
	}); err != nil {
		// The hold stands: an un-recorded release would reappear on the next fold and the
		// budget would disagree with the log.
		return fmt.Errorf("releasing hold %s (hold stands): %w", requestID, err)
	}

	e.closedBy = KindReleased
	return nil
}

// Sweep reclaims every open hold that is PROVABLY dead: its expiry has passed AND no
// payment.executing claim exists for its request. Returns the count released. Call at
// boot after Recover, then on a ticker.
//
// This is FR-PAY-007's missing half, and it deliberately does not wait for an
// approval.expired event: a request that expires with nobody waiting on it (the daemon
// restarted, or the request was recovered from the log) is never marked expired at all
// today, so an event-triggered release would leak the hold forever.
//
// It re-reads the durable claim set FIRST. The ledger never writes payment.executing, so
// its in-memory claim view is only as fresh as the last fold, and a stale view would
// release a hold whose payment may already be signed. That is the one thing this package
// must never do, so the check is made authoritative rather than cheap.
func (l *Ledger) Sweep(ctx context.Context, now time.Time) (int, error) {
	if err := l.refreshClaims(ctx); err != nil {
		return 0, err
	}

	l.mu.Lock()
	var dead []string
	for id, e := range l.entries {
		if !e.open() || e.claimed {
			continue
		}
		// Zero expiry = not provably dead (see expiryFromUnix). The comparison matches
		// the lifecycle's own expiry test: expired means NOT before ExpiresAt.
		if e.hold.ExpiresAt.IsZero() || now.Before(e.hold.ExpiresAt) {
			continue
		}
		dead = append(dead, id)
	}
	l.mu.Unlock()

	// Deterministic release order, so the audit log of a sweep is reproducible.
	sort.Strings(dead)

	released := 0
	for _, id := range dead {
		if err := l.Release(ctx, id, sweepReason, CodeExpiredUnattempted, true); err != nil {
			// Report what did get released before failing: the caller logs the count and
			// the error, and the next sweep retries the rest.
			return released, fmt.Errorf("sweeping expired hold %s: %w", id, err)
		}
		released++
	}
	return released, nil
}

// refreshClaims re-folds the lifecycle's execution claims onto known holds. Claims are
// monotone (a claim is never withdrawn: the lifecycle's write-ahead flag is the whole
// point), so merging them in is idempotent and never clears one.
func (l *Ledger) refreshClaims(ctx context.Context) error {
	rows, err := l.st.DB().QueryContext(ctx,
		`SELECT payload_json FROM events WHERE kind = ? ORDER BY seq`, kindPaymentExecuting)
	if err != nil {
		return fmt.Errorf("reading execution claims: %w", err)
	}
	defer rows.Close()

	var claimed []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var p eventPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return fmt.Errorf("corrupt %s event: %w", kindPaymentExecuting, err)
		}
		if p.RequestID != "" {
			claimed = append(claimed, p.RequestID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range claimed {
		e := l.entries[id]
		if e == nil {
			// A claim for a request the ledger holds nothing for: record it, so a later
			// reserve for that id (a corrupt or reordered log) cannot be swept either.
			e = &entry{}
			l.entries[id] = e
		}
		e.claimed = true
	}
	return nil
}

// Unresolved returns every open hold that HAS a payment.executing claim: exactly the set
// the boot status reconciler must poll, because each one may or may not have been signed.
// Never swept, never auto-released. Oldest hold first.
func (l *Ledger) Unresolved() []Hold {
	return l.claimedOpen(0, time.Time{})
}

// StalledSince returns open claimed holds reserved at least d ago, for owner escalation
// after the reconcile deadline. A non-positive d returns every claimed open hold.
func (l *Ledger) StalledSince(d time.Duration, now time.Time) []Hold {
	return l.claimedOpen(d, now)
}

func (l *Ledger) claimedOpen(olderThan time.Duration, now time.Time) []Hold {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []Hold
	for _, e := range l.entries {
		if !e.open() || !e.claimed {
			continue
		}
		if olderThan > 0 && now.Sub(e.hold.ReservedAt) < olderThan {
			continue
		}
		out = append(out, e.hold)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].ReservedAt.Equal(out[b].ReservedAt) {
			return out[a].RequestID < out[b].RequestID
		}
		return out[a].ReservedAt.Before(out[b].ReservedAt)
	})
	return out
}

// Spend is the policy engine's SpendState hook: wire it to approval.Lifecycle.Spend,
// replacing the constant zero. This is the call that makes rule 1 (job-budget) and rule 3
// (daily-cap) live.
//
//	JobCommittedMicros = sum of open holds on jobID + sum of committed spend on jobID
//	DailySpentMicros   = the same sum, org-wide, restricted to holds RESERVED inside the
//	                     current UTC-day window (policy.DailyWindowStartUTC, the caller
//	                     contract SpendState documents)
//
// The daily figure is scoped to the org that owns jobID, learned from that job's own
// reserve events. A job with no holds yet has no known org, so the figure falls back to
// every org's windowed spend: over-counting can only deny more, never less, and the
// daemon runs a single org today.
//
// Both sums are recomputed by scanning the folded holds. That is O(requests) per intake,
// which for a demo-scale log is far cheaper than maintaining running counters that could
// drift from the fold, and the fold is the truth this package exists to defend.
func (l *Ledger) Spend(jobID string) policy.SpendState {
	windowStart := policy.DailyWindowStartUTC(l.clock()).UnixMilli()

	l.mu.Lock()
	defer l.mu.Unlock()

	orgID := l.jobOrg[jobID]

	var st policy.SpendState
	for _, e := range l.entries {
		if !e.reserved {
			continue
		}
		// An open hold counts at its full reserved figure; a settled one counts at what
		// actually moved; a released one counts for nothing (committedMicros is zero).
		amount := e.committedMicros
		if e.open() {
			amount = e.hold.ReserveMicros
		}
		if amount <= 0 {
			continue
		}
		if e.hold.JobID == jobID {
			st.JobCommittedMicros = satAdd(st.JobCommittedMicros, amount)
		}
		if orgID != "" && e.hold.OrgID != orgID {
			continue
		}
		if e.hold.ReservedAt.UnixMilli() >= windowStart {
			st.DailySpentMicros = satAdd(st.DailySpentMicros, amount)
		}
	}
	return st
}

// satAdd saturates instead of wrapping. A wrapped-negative sum would read as a DISCOUNT
// to policy.Evaluate, which fails closed on negative spend state, so the ledger must
// never hand it one. Both operands here are non-negative by construction.
func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// JobGate serializes intake per job: wire it to approval.Lifecycle.JobGate so the spend
// read, the policy evaluation and the hold are ONE critical section.
//
// Without it, two concurrent intents on one job both evaluate against the same unheld
// state and both pass rule 1: five 4.00 intents against a 6.00 budget would all be
// approved. Holding the gate across the whole decision is the hazard H3's pre-sign hold
// exists to close.
//
// The returned release is idempotent and must be called exactly once (defer it). The
// per-job gate is dropped when the last waiter leaves, so the map does not grow with the
// job count.
func (l *Ledger) JobGate(jobID string) (release func()) {
	l.gmu.Lock()
	g := l.gates[jobID]
	if g == nil {
		g = &jobGate{}
		l.gates[jobID] = g
	}
	// Counted BEFORE blocking, under the map lock, so nobody can delete the gate this
	// caller is about to wait on.
	g.waiters++
	l.gmu.Unlock()

	g.mu.Lock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Unlock()
			l.gmu.Lock()
			g.waiters--
			if g.waiters == 0 {
				delete(l.gates, jobID)
			}
			l.gmu.Unlock()
		})
	}
}

// NoteReconcileStalled records that a post-sign hold has outlived the reconcile deadline
// and needs an owner. It is a RECORD, not a transition: the hold stays open, because the
// signed authorization is a bearer instrument that may still settle and nothing in the
// sidecar ever leaves RECONCILING. Releasing it requires an explicit owner action, which
// is the pending-chain discipline applied to money of unknown outcome.
func (l *Ledger) NoteReconcileStalled(ctx context.Context, h Hold, state string) error {
	if h.RequestID == "" {
		return fmt.Errorf("%w: a stall record needs a request id", ErrInvalidHold)
	}
	_, err := l.st.Append(ctx, store.Event{
		TS:       l.clock().UTC(),
		Kind:     KindReconcileStalled,
		EntityID: h.JobID,
		Actor:    actor,
		Payload: map[string]any{
			"request_id":  h.RequestID,
			"job_id":      h.JobID,
			"held_micros": h.ReserveMicros,
			"payment_id":  h.PaymentID,
			"state":       state,
			"held_since":  h.ReservedAt.Unix(),
		},
	})
	if err != nil {
		return fmt.Errorf("recording stalled reconcile for %s: %w", h.RequestID, err)
	}
	return nil
}

// NoteChainDivergence records that a job's on-chain operating budget disagrees with the
// daemon's policy budget. Also a record, not a transition: the two budgets are
// deliberately left unreconciled (making the daemon budget per-job is a policy-engine
// change), so this converts a silent OverBudget revert at demo time into a loud boot-time
// fact. Same posture as billing's projection divergence: raise the alert, pick no winner.
func (l *Ledger) NoteChainDivergence(ctx context.Context, jobID, vaultJobID string, chainBudgetMicros, daemonBudgetMicros int64) error {
	if jobID == "" {
		return fmt.Errorf("%w: a divergence record needs a job id", ErrInvalidHold)
	}
	_, err := l.st.Append(ctx, store.Event{
		TS:       l.clock().UTC(),
		Kind:     KindChainDivergence,
		EntityID: jobID,
		Actor:    actor,
		Payload: map[string]any{
			"job_id":               jobID,
			"vault_job_id":         vaultJobID,
			"chain_budget_micros":  chainBudgetMicros,
			"daemon_budget_micros": daemonBudgetMicros,
		},
	})
	if err != nil {
		return fmt.Errorf("recording chain budget divergence for %s: %w", jobID, err)
	}
	return nil
}
