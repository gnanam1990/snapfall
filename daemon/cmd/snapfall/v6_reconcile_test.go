package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/budget"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// V6's second done-when, against the code that actually ships.
//
// The clause is "AT-15 passes AND a reserved-then-failed payment releases budget correctly".
// AT-15 is gated in CI on both sides. The second half was not: the certifying test in
// internal/purchasing/pay_test.go performs the reconcile BY HAND -- Sweep, Unresolved, Status,
// Release -- and never calls reconcileUnresolved, the function the daemon actually runs at boot
// (main.go:942) and after a lane restart (main.go:585).
//
// The two had already drifted. That test releases with h3.CodePaymentRejected; the shipped path
// releases with the terminal STATE, because a status poll returns no error code to carry. So the
// single assertion standing behind the clause was checking a value production never produces.
//
// These drive reconcileUnresolved directly. It lives in package main, which is why nothing
// reached it -- and package main is testable from inside, so closing this needs no refactor of
// the daemon's wiring.
//
// The branch table (main.go:1131-1146) is a money-safety contract, and every row errs toward
// KEEPING the hold. A wrong release reopens budget behind an authorization that may still
// settle, which is the bearer-instrument rule the whole reclaim design rests on.

const (
	recJob     = "job_v6_reconcile"
	recRequest = "apr_v6"
	recAmount  = int64(4_000_000) // 4.00 USDC, the escalation figure
)

// stubPoller is the sidecar's durable record, answered from a table.
type stubPoller struct {
	res map[string]h3.StatusResult
	err map[string]error
	// calls records every paymentID asked about, so a test can prove the reconciler polled the
	// PERSISTED id rather than one read back out of a response.
	calls []string
}

func (s *stubPoller) Status(_ context.Context, paymentID string) (h3.StatusResult, error) {
	s.calls = append(s.calls, paymentID)
	if e, ok := s.err[paymentID]; ok {
		return h3.StatusResult{}, e
	}
	if r, ok := s.res[paymentID]; ok {
		return r, nil
	}
	return h3.StatusResult{}, errors.New("stub has no answer for " + paymentID)
}

// recFixture is a ledger holding ONE reserved, claimed, still-open hold: exactly the shape the
// reconciler exists for -- money reserved, an execution claim written, no outcome yet.
type recFixture struct {
	led *budget.Ledger
	st  *store.Store
}

func newRecFixture(t *testing.T, paymentID string) *recFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "v6.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	led := budget.New(st, func() time.Time { return now })

	if err := led.Reserve(ctx, budget.Hold{
		RequestID:     recRequest,
		IntentID:      "pi_v6",
		JobID:         recJob,
		OrgID:         "org_v6",
		ReserveMicros: recAmount,
		PaymentID:     paymentID,
		Nonce:         "0x" + "11",
		Merchant:      "api.research-data.example",
		PayTo:         "0x000000000000000000000000000000000000dEaD",
		// Deliberately in the future: Sweep must not be what resolves this. A claimed hold
		// belongs to the reconciler and to nothing else.
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// The execution claim, written the way the approval lifecycle writes it. Without it the
	// hold is not "claimed" and never enters Unresolved() -- that flag is what separates a hold
	// the sweeper may expire from one that may already be a signed bearer instrument. The
	// ledger never writes this event itself, so it only learns the claim by folding.
	if _, err := st.Append(ctx, store.Event{
		Kind:     "payment.executing",
		EntityID: recJob,
		Payload:  map[string]any{"request_id": recRequest},
	}); err != nil {
		t.Fatalf("append the execution claim: %v", err)
	}
	if err := led.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := led.Unresolved(); len(got) != 1 {
		t.Fatalf("fixture holds %d unresolved holds, want 1 -- the reconciler would see nothing", len(got))
	}
	return &recFixture{led: led, st: st}
}

func (f *recFixture) stillHeld(t *testing.T) bool {
	t.Helper()
	return len(f.led.Unresolved()) == 1
}

func (f *recFixture) committed(t *testing.T) int64 {
	t.Helper()
	return f.led.Spend(recJob).JobCommittedMicros
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// ── the row the done-when names ─────────────────────────────────────────────

func TestV6_ReconcileReleasesAReservedThenFailedPayment(t *testing.T) {
	// The clause, driven through the shipped function. FAILED is one of exactly two states the
	// reconciler treats as a PROVEN terminal failure: the sidecar's own durable record says
	// nothing settled.
	const pid = "pay_failed"
	f := newRecFixture(t, pid)
	poller := &stubPoller{res: map[string]h3.StatusResult{
		pid: {PaymentID: pid, State: h3.StateFailed, Reason: "seller rejected the authorization"},
	}}

	open := reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if len(open) != 0 {
		t.Errorf("the hold is still open after a proven terminal failure: %v", open)
	}
	if f.stillHeld(t) {
		t.Error("a reserved-then-failed payment did not release its budget hold")
	}
	if got := f.committed(t); got != 0 {
		t.Errorf("job committed spend is %d micros after the release, want 0", got)
	}

	// It polled the PERSISTED payment id. A reconciler deriving it from a response could not
	// reach /v1/status at all when the pay response was lost, which is the whole reason the id
	// is precomputed at reserve time.
	if len(poller.calls) != 1 || poller.calls[0] != pid {
		t.Errorf("polled %v, want exactly [%s]", poller.calls, pid)
	}
}

func TestV6_ReconcileReleasesExpiredTheSameWay(t *testing.T) {
	// EXPIRED is the other proven-terminal state. Only these two license a post-sign release.
	const pid = "pay_expired"
	f := newRecFixture(t, pid)
	poller := &stubPoller{res: map[string]h3.StatusResult{pid: {PaymentID: pid, State: h3.StateExpired}}}

	reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if f.stillHeld(t) {
		t.Error("an EXPIRED payment did not release its hold")
	}
}

// ── the rows that must NOT release ──────────────────────────────────────────

func TestV6_AnUnknownOutcomeKeepsTheHold(t *testing.T) {
	// The bearer-instrument rule. Every one of these may still settle, so a release would
	// reopen budget behind money that has already left. A standing hold is the correct,
	// visible failure; a release would be the silent one.
	for _, state := range []string{h3.StateSigned, h3.StateSubmitted, h3.StateReconciling} {
		t.Run(state, func(t *testing.T) {
			pid := "pay_" + state
			f := newRecFixture(t, pid)
			poller := &stubPoller{res: map[string]h3.StatusResult{pid: {PaymentID: pid, State: state}}}

			open := reconcileUnresolved(context.Background(), f.led, poller, quietLog())

			if !f.stillHeld(t) {
				t.Fatalf("%s released the hold; the authorization may still settle", state)
			}
			if open[recRequest] != state {
				t.Errorf("open state %q, want %q so the stall escalation can name it without polling again",
					open[recRequest], state)
			}
		})
	}
}

func TestV6_ATransportFailureKeepsTheHold(t *testing.T) {
	// "We could not ask" is not "nothing happened". A sidecar that is down must never look
	// like a payment that failed.
	const pid = "pay_unreachable"
	f := newRecFixture(t, pid)
	poller := &stubPoller{err: map[string]error{pid: errors.New("connection refused")}}

	open := reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if !f.stillHeld(t) {
		t.Fatal("an unreachable sidecar released a claimed hold")
	}
	if open[recRequest] != "" {
		t.Errorf("open state %q, want empty: nothing could be learned about this hold", open[recRequest])
	}
}

func TestV6_AnUnrecognisedStateKeepsTheHoldRatherThanGuessing(t *testing.T) {
	// If the sidecar grows a state this daemon does not know, the safe reading is "unknown",
	// not "failed". The empty string is the same case, and is what a decode failure produces.
	for _, state := range []string{"PARTIALLY_SETTLED", "", "delivered"} {
		name := state
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			pid := "pay_" + name
			f := newRecFixture(t, pid)
			poller := &stubPoller{res: map[string]h3.StatusResult{pid: {PaymentID: pid, State: state}}}

			reconcileUnresolved(context.Background(), f.led, poller, quietLog())

			if !f.stillHeld(t) {
				t.Fatalf("state %q released the hold; an unknown vocabulary proves nothing about the money", state)
			}
		})
	}
}

func TestV6_NoSidecarWiredKeepsEveryHold(t *testing.T) {
	// A nil poller is a daemon with no H3 lane. Being unable to ask does not license returning
	// the money to the budget.
	f := newRecFixture(t, "pay_nolane")

	open := reconcileUnresolved(context.Background(), f.led, nil, quietLog())

	if !f.stillHeld(t) {
		t.Fatal("a daemon with no sidecar released a claimed hold")
	}
	if open[recRequest] != "" {
		t.Errorf("open state %q, want empty", open[recRequest])
	}
}

// ── the rows that resolve the other way ─────────────────────────────────────

func TestV6_PaymentNotFoundReleasesAsPreSign(t *testing.T) {
	// The one case where pre-sign is provable: the sidecar's pre-sign checks run BEFORE its
	// write-ahead record, so no record means no signature. It keys on the CODE, never on a bare
	// 404, which could equally mean a wrong URL.
	const pid = "pay_absent"
	f := newRecFixture(t, pid)
	poller := &stubPoller{err: map[string]error{
		pid: &h3.Error{Code: h3.CodePaymentNotFound, Message: "no such payment"},
	}}

	reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if f.stillHeld(t) {
		t.Fatal("a payment the sidecar never recorded did not release")
	}
}

func TestV6_ADeliveredPaymentCommitsAtTheSidecarsFigure(t *testing.T) {
	// Committing at OUR reserved figure would overstate the spend whenever the seller charged
	// less. The sidecar's amountPaid is the authority, and the remainder releases in the same
	// event.
	const pid = "pay_delivered"
	f := newRecFixture(t, pid)
	poller := &stubPoller{res: map[string]h3.StatusResult{
		pid: {PaymentID: pid, State: h3.StateDelivered, AmountPaid: "40000"},
	}}

	reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if f.stillHeld(t) {
		t.Fatal("a delivered payment left its hold open")
	}
	if got := f.committed(t); got != 40_000 {
		t.Errorf("committed %d micros, want the sidecar's 40000 rather than the reserved %d", got, recAmount)
	}
}

func TestV6_AnUnreadableAmountKeepsTheHoldRatherThanGuessing(t *testing.T) {
	// A committed state whose amount will not parse is not a licence to commit a number we
	// chose. The hold stands and the next reconcile retries.
	const pid = "pay_badamount"
	f := newRecFixture(t, pid)
	poller := &stubPoller{res: map[string]h3.StatusResult{
		pid: {PaymentID: pid, State: h3.StateDelivered, AmountPaid: "four dollars"},
	}}

	reconcileUnresolved(context.Background(), f.led, poller, quietLog())

	if !f.stillHeld(t) {
		t.Fatal("an unparseable amountPaid still resolved the hold at some figure")
	}
	// Still the RESERVED figure, not the bogus one. An open hold counts toward committed spend
	// by design -- that is what a reservation is -- so the thing to prove is that nothing
	// CLOSED the hold at a number nobody could read.
	if got := f.committed(t); got != recAmount {
		t.Errorf("committed %d micros, want the untouched reservation %d", got, recAmount)
	}
	if n := countEvents(t, f, budget.KindCommitted); n != 0 {
		t.Errorf("%d commit event(s) written for an amount that does not parse; the figure would be a guess", n)
	}
}

// countEvents counts durable budget events of one kind, so a test can prove the reconciler
// wrote nothing rather than merely that the in-memory numbers look unchanged.
func countEvents(t *testing.T, f *recFixture, kind string) int {
	t.Helper()
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", kind, err)
	}
	return n
}
