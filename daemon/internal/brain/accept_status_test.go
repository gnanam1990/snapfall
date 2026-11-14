package brain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
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
	t.Run("settled, recorded by the read, when the chain shows a settlement no record has yet", func(t *testing.T) {
		b, _, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		b.SetSettlementOracle(func(context.Context, string) (bool, error) { return true, nil })
		wantStatus(t, b, jobID, SettlementSettled)
		if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
			t.Fatalf("settlement.executed = %d, want the observation recorded once", n)
		}
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

// The write-ahead record cannot be written: NOTHING is sent (the hook refuses before the
// send), the Accept fails, and no transaction exists that the daemon does not know about. So
// once the store recovers the next Accept simply submits, and that one settles.
func TestAcceptBroadcast_FailedWriteAheadRecordSendsNothing(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout, trackedSettled)
	ctx := context.Background()
	lift := failSubmittedRecords(t, b)
	if s, err := b.AcceptDelivery(ctx, jobID); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("accept: state=%q err=%v, want a not-sent error", s, err)
	}
	lane.mu.Lock()
	out := len(lane.sent)
	lane.mu.Unlock()
	if out != 0 {
		t.Fatalf("%d transactions reached the node although their record failed, want 0", out)
	}
	wantStatus(t, b, jobID, SettlementRetry)

	lift()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("after the store recovered: state=%q err=%v, want accepted-settled", s, err)
	}
	if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 1 {
		t.Fatalf("settlement.submitted = %d, want only the one that was sent", n)
	}
	if _, p := executedRecord(t, b, jobID); p["tx_hash"] != txHashN(2) {
		t.Fatalf("settled by %v, want tx2 (tx1 was never sent)", p["tx_hash"])
	}
}

// oracleFailedAccept is the reviewer's case: the first Accept claims the job, but the
// on-chain settlement read fails, so nothing is recorded and nothing is submitted. Then the
// customer's own wallet settles on chain. Returns the rig and a switch for the oracle.
func oracleFailedAccept(t *testing.T) (*Brain, *trackedLane, string, *atomic.Bool) {
	t.Helper()
	b, lane, jobID := trackedRig(t) // no submission is scripted: any would fail the test
	b.SetBilling(billing.New(b.store, g12Chain, nil))
	var landed atomic.Bool
	var down atomic.Bool
	down.Store(true)
	b.SetSettlementOracle(func(context.Context, string) (bool, error) {
		if down.Load() {
			return false, errors.New("rpc down")
		}
		return landed.Load(), nil
	})
	if _, err := b.AcceptDelivery(context.Background(), jobID); err == nil {
		t.Fatal("accept over an unreadable oracle returned no error")
	}
	b.mu.Lock()
	stage := b.jobs[jobID].Stage
	b.mu.Unlock()
	if stage != StageAccepted {
		t.Fatalf("stage = %s, want the claim to stand", stage)
	}
	down.Store(false)
	landed.Store(true) // the customer's own wallet settled it
	return b, lane, jobID, &landed
}

// A settlement the chain shows and no record has (the oracle read failed during the first
// Accept, then the customer settled from their own wallet) is recorded by the status read
// as an OBSERVATION, attributed from the indexed JobSettled hash, and reported settled. A
// self-custody customer has no Accept left to press; nothing is ever submitted for it.
func TestSettlementStatus_RecordsASettlementTheChainShows(t *testing.T) {
	ctx := context.Background()
	t.Run("indexed: attributed to the hash's signer", func(t *testing.T) {
		b, lane, jobID, _ := oracleFailedAccept(t)
		customerTx := "0x" + repeat64("d")
		seedChainRow(t, b.store, "JobSettled", "0x"+repeat64("b"), customerTx, 77,
			map[string]string{"advanceRepaidAtomic": "0", "operatorNetAtomic": "1000000"})
		wantStatus(t, b, jobID, SettlementSettled)
		actor, p := executedRecord(t, b, jobID)
		if actor != "customer" || p["signer"] != signerCustomer || p["tx_hash"] != customerTx || p["block"] != float64(77) {
			t.Fatalf("actor=%q payload=%v, want the customer's indexed transaction", actor, p)
		}
		wantStatus(t, b, jobID, SettlementSettled)
		if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
			t.Fatalf("settlement.executed = %d after two reads, want 1", n)
		}
		if subs, resolves := lane.counts(); subs != 0 || resolves != 0 {
			t.Fatalf("submits=%d resolves=%d, want 0 and 0: an observation is never a transaction", subs, resolves)
		}
	})
	t.Run("not indexed yet: the customer, with the hash marked pending", func(t *testing.T) {
		b, lane, jobID, _ := oracleFailedAccept(t)
		wantStatus(t, b, jobID, SettlementSettled)
		actor, p := executedRecord(t, b, jobID)
		if actor != "customer" || p["signer"] != signerCustomer || p["tx_hash_pending"] == nil || p["tx_hash"] != nil {
			t.Fatalf("actor=%q payload=%v, want the customer with tx_hash_pending", actor, p)
		}
		if subs, _ := lane.counts(); subs != 0 {
			t.Fatalf("submits=%d, want 0", subs)
		}
	})
	t.Run("an attempt in flight: pending, nothing recorded", func(t *testing.T) {
		b, _, jobID, _ := oracleFailedAccept(t)
		b.mu.Lock()
		b.settling[jobID] = true
		b.mu.Unlock()
		wantStatus(t, b, jobID, SettlementPending)
		b.clearSettling(jobID)
		if n := countEvents(t, b.store, "settlement.executed", jobID); n != 0 {
			t.Fatalf("settlement.executed = %d while an attempt held the marker, want 0", n)
		}
	})
	t.Run("frozen: still recorded and read accurately", func(t *testing.T) {
		b, lane, jobID, _ := oracleFailedAccept(t)
		reg, err := freeze.NewRegistry(ctx, b.store, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		b.SetFreeze(reg, "org_demo")
		if _, err := reg.Engage(ctx, freeze.KindJob, jobID, "gnanam", "incident"); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, b, jobID, SettlementSettled)
		if subs, _ := lane.counts(); subs != 0 {
			t.Fatalf("submits=%d, want 0", subs)
		}
	})
	t.Run("the chain says not settled: retry, nothing recorded", func(t *testing.T) {
		b, _, jobID, landed := oracleFailedAccept(t)
		landed.Store(false)
		wantStatus(t, b, jobID, SettlementRetry)
		if n := countEvents(t, b.store, "settlement.executed", jobID); n != 0 {
			t.Fatalf("settlement.executed = %d, want 0", n)
		}
	})
}

// The settlement-resolver records the same observation without anyone reading the status:
// an accepted, unsettled job with no open broadcast whose chain shows it settled gets its
// settlement.executed from the per-minute pass, and never a transaction.
func TestResolveOpenSettlements_RecordsASettlementTheChainShows(t *testing.T) {
	ctx := context.Background()
	b, lane, jobID, landed := oracleFailedAccept(t)
	landed.Store(false)
	if n, err := b.ResolveOpenSettlements(ctx); err != nil || n != 0 {
		t.Fatalf("not settled on chain: resolved %d err=%v, want 0", n, err)
	}
	landed.Store(true)
	customerTx := "0x" + repeat64("d")
	seedChainRow(t, b.store, "JobSettled", "0x"+repeat64("b"), customerTx, 77,
		map[string]string{"advanceRepaidAtomic": "0", "operatorNetAtomic": "1000000"})
	if n, err := b.ResolveOpenSettlements(ctx); err != nil || n != 1 {
		t.Fatalf("settled on chain: resolved %d err=%v, want 1", n, err)
	}
	if actor, p := executedRecord(t, b, jobID); actor != "customer" || p["tx_hash"] != customerTx {
		t.Fatalf("actor=%q payload=%v, want the customer's indexed transaction", actor, p)
	}
	if n, err := b.ResolveOpenSettlements(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: resolved %d err=%v, want 0", n, err)
	}
	if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
		t.Fatalf("settlement.executed = %d, want 1", n)
	}
	if subs, resolves := lane.counts(); subs != 0 || resolves != 0 {
		t.Fatalf("submits=%d resolves=%d, want 0 and 0", subs, resolves)
	}
	wantStatus(t, b, jobID, SettlementSettled)

	// An unreadable oracle is reported against the job, and records nothing.
	b2, _, job2, _ := oracleFailedAccept(t)
	b2.SetSettlementOracle(func(context.Context, string) (bool, error) { return false, errors.New("rpc down") })
	if _, err := b2.ResolveOpenSettlements(ctx); err == nil || !strings.Contains(err.Error(), job2) {
		t.Fatalf("unreadable oracle: err=%v, want it reported against the job", err)
	}
	if n := countEvents(t, b2.store, "settlement.executed", job2); n != 0 {
		t.Fatalf("settlement.executed = %d over an unreadable oracle, want 0", n)
	}
}

// A done context stops the pass before the next job: the bounded boot pass leaves the rest
// to the per-minute resolver instead of holding up serving.
func TestResolveOpenSettlements_StopsWhenTheContextIsDone(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	if _, err := b.AcceptDelivery(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := b.ResolveOpenSettlements(ctx)
	if n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("resolved %d err=%v, want 0 and context.Canceled", n, err)
	}
	if _, resolves := lane.counts(); resolves != 0 {
		t.Fatalf("resolves=%d after the context was done, want 0", resolves)
	}
}
