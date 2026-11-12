package brain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func wantStatus(t *testing.T, b *Brain, jobID, want string) {
	t.Helper()
	got, err := b.SettlementStatus(context.Background(), jobID)
	if err != nil || got != want {
		t.Fatalf("settlement status = %q err=%v, want %q", got, err, want)
	}
}

// Each of the four customer-facing values, derived from the same record and rules the
// retry acts on.
func TestSettlementStatus_FourValues(t *testing.T) {
	ctx := context.Background()

	t.Run("none before the claim", func(t *testing.T) {
		b, _, jobID := acceptRig(t)
		wantStatus(t, b, jobID, SettlementNone)
	})
	t.Run("settled once settlement.executed is recorded", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{settledOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, b, jobID, SettlementSettled)
	})
	t.Run("retry after a revert", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, b, jobID, SettlementRetry)
	})
	t.Run("retry after a submission that failed before broadcast", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{{}}, []error{errors.New("gas estimate: reverts")})
		if _, err := b.AcceptDelivery(ctx, jobID); err == nil {
			t.Fatal("want the failed submission surfaced")
		}
		wantStatus(t, b, jobID, SettlementRetry)
	})
	t.Run("pending while the chain is not at Delivered, retry once it is", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		delivered := false
		b.SetDeliveryOracle(func(context.Context, string) (bool, error) { return delivered, nil })
		wantStatus(t, b, jobID, SettlementPending)
		delivered = true
		wantStatus(t, b, jobID, SettlementRetry)
	})
	t.Run("pending with no chain identity or no lane", func(t *testing.T) {
		b, _, jobID := acceptRig(t) // no vault id, no lane
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("state=%q err=%v", s, err)
		}
		wantStatus(t, b, jobID, SettlementPending)
		if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("b") }); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, b, jobID, SettlementPending) // vault id bound, still no lane
	})
	t.Run("pending while a broadcast is open, retry once it is confirmed dropped", func(t *testing.T) {
		b, lane, jobID := trackedRig(t, receiptTimeout)
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, b, jobID, SettlementPending)
		lane.set(1, funding.TxDropped, false)
		for i := 0; i < 2; i++ {
			if _, err := b.ResolveOpenSettlements(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if n := countEvents(t, b.store, "settlement.dropped", jobID); n != 1 {
			t.Fatalf("settlement.dropped = %d, want 1", n)
		}
		wantStatus(t, b, jobID, SettlementRetry)
	})
	t.Run("retry when the chain shows a settlement no record has yet", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		b.SetSettlementOracle(func(context.Context, string) (bool, error) { return true, nil })
		wantStatus(t, b, jobID, SettlementRetry)
	})
	t.Run("a read error is an error, never a value", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		b.SetDeliveryOracle(func(context.Context, string) (bool, error) { return false, errors.New("rpc down") })
		if s, err := b.SettlementStatus(ctx, jobID); err == nil {
			t.Fatalf("status over an unreadable chain = %q with no error", s)
		}
		b.SetDeliveryOracle(nil)
		if _, err := b.store.Append(ctx, store.Event{Kind: "settlement.submitted", EntityID: jobID, Actor: "funding",
			Payload: map[string]any{"note": "names no transaction"}}); err != nil {
			t.Fatal(err)
		}
		if s, err := b.SettlementStatus(ctx, jobID); err == nil {
			t.Fatalf("status over an unreadable record = %q with no error", s)
		}
	})
}

// Startup resolution: a broadcast the previous run left open is resolved once at boot with
// the same code the retry uses, recording executed / reverted / the first unseen look, and
// never submitting a new acceptDelivery.
func TestResolveOpenSettlements_RecordsOutcomesAndNeverSubmits(t *testing.T) {
	ctx := context.Background()
	restart := func(t *testing.T, b *Brain, lane funding.Submitter) *Brain {
		t.Helper()
		fund := funding.New()
		fund.SetChain(nil, lane)
		b2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), b.store, b.memory, fund)
		if err := b2.Recover(); err != nil {
			t.Fatal(err)
		}
		return b2
	}
	for _, tc := range []struct {
		name     string
		state    funding.TxState
		reverted bool
		kind     string
		status   string
	}{
		{"mined", funding.TxMined, false, "settlement.executed", SettlementSettled},
		{"mined and reverted", funding.TxMined, true, "settlement.reverted", SettlementRetry},
		{"dropped (first look)", funding.TxDropped, false, "settlement.unseen", SettlementPending},
		{"still pending", funding.TxPending, false, "", SettlementPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, lane, jobID := trackedRig(t, receiptTimeout)
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			lane.set(1, tc.state, tc.reverted)
			b2 := restart(t, b, lane)
			n, err := b2.ResolveOpenSettlements(ctx)
			if err != nil || n != 1 {
				t.Fatalf("resolved %d err=%v, want 1", n, err)
			}
			if tc.kind != "" && countEvents(t, b.store, tc.kind, jobID) != 1 {
				t.Fatalf("%s not recorded", tc.kind)
			}
			if subs, resolves := lane.counts(); subs != 1 || resolves != 1 {
				t.Fatalf("submits=%d resolves=%d, want 1 and 1: startup resolves, it never submits", subs, resolves)
			}
			wantStatus(t, b2, jobID, tc.status)
			// A second pass over a resolved job does nothing.
			if tc.state == funding.TxMined {
				if n, err := b2.ResolveOpenSettlements(ctx); err != nil || n != 0 {
					t.Fatalf("second pass resolved %d err=%v, want 0", n, err)
				}
			}
		})
	}

	t.Run("an in-flight attempt is skipped, an unreadable lane is reported", func(t *testing.T) {
		b, lane, jobID := trackedRig(t, receiptTimeout)
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		b.settling[jobID] = true
		b.mu.Unlock()
		if n, err := b.ResolveOpenSettlements(ctx); err != nil || n != 0 {
			t.Fatalf("in flight: resolved %d err=%v", n, err)
		}
		b.clearSettling(jobID)
		lane.mu.Lock()
		lane.resolveErr = errors.New("rpc down")
		lane.mu.Unlock()
		if _, err := b.ResolveOpenSettlements(ctx); err == nil || !strings.Contains(err.Error(), jobID) {
			t.Fatalf("an unreadable lane: err=%v, want it reported against the job", err)
		}
		if subs, _ := lane.counts(); subs != 1 {
			t.Fatalf("submits=%d, want 1", subs)
		}
	})
}

// failSubmittedRecords makes every settlement.submitted insert fail, as a full disk or a
// locked database would; the returned func lifts it.
func failSubmittedRecords(t *testing.T, b *Brain) func() {
	t.Helper()
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_submitted BEFORE INSERT ON events
		WHEN NEW.kind = 'settlement.submitted' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := b.store.DB().Exec(`DROP TRIGGER fail_submitted`); err != nil {
			t.Fatal(err)
		}
	}
}

// The broadcast is out, its record cannot be written, and the receipt wait fails: the Accept
// FAILS with an error naming the transaction, the status says pending, and the next Accept
// records the broadcast FIRST and resolves it instead of resubmitting. While the record
// still cannot be written, nothing at all is submitted.
func TestAcceptBroadcast_UnrecordedBroadcastFailsExplicitlyAndIsRecordedFirst(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout, trackedSettled)
	ctx := context.Background()
	lift := failSubmittedRecords(t, b)
	_, err := b.AcceptDelivery(ctx, jobID)
	if err == nil || !strings.Contains(err.Error(), txHashN(1)) || !strings.Contains(err.Error(), "unrecorded") {
		t.Fatalf("accept err=%v, want an explicit error naming %s as out and unrecorded", err, txHashN(1))
	}
	wantStatus(t, b, jobID, SettlementPending)
	if _, err := b.AcceptDelivery(ctx, jobID); err == nil {
		t.Fatal("a retry while the broadcast still cannot be recorded returned no error")
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("submissions = %d while a broadcast is out unrecorded, want 1", subs)
	}

	lift()
	lane.set(1, funding.TxMined, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("after the store recovered: state=%q err=%v, want accepted-settled via tx1", s, err)
	}
	if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 1 {
		t.Fatalf("settlement.submitted = %d, want the late record", n)
	}
	if _, p := executedRecord(t, b, jobID); p["tx_hash"] != txHashN(1) {
		t.Fatalf("settled by %v, want tx1", p["tx_hash"])
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("submissions = %d, want 1", subs)
	}
}
