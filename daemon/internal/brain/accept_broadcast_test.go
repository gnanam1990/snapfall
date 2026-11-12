package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
)

// trackedStep is what one acceptDelivery submission does: whether it gets out (the
// broadcast hook runs), and what the receipt wait then returns.
type trackedStep struct {
	broadcast bool
	out       funding.ChainOutcome
	err       error
}

type trackedTx struct {
	state    funding.TxState
	reverted bool
}

// trackedLane is a settle lane that reports each broadcast before its receipt wait and
// answers Resolve from a table the test moves along, the way the chain would.
type trackedLane struct {
	t          *testing.T
	mu         sync.Mutex
	script     []trackedStep
	submits    int
	resolves   int
	resolveErr error
	txs        map[string]*trackedTx
}

func newTrackedLane(t *testing.T, script ...trackedStep) *trackedLane {
	return &trackedLane{t: t, script: script, txs: map[string]*trackedTx{}}
}

func txHashN(n int) string { return fmt.Sprintf("0x%064x", n) }

func (l *trackedLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	l.t.Error("a tracked lane was submitted to without its broadcast hook")
	return funding.ChainOutcome{}, errors.New("untracked submission")
}

func (l *trackedLane) SubmitTracked(_ context.Context, _ []byte, onBroadcast func(funding.Broadcast)) (funding.ChainOutcome, error) {
	l.mu.Lock()
	i := l.submits
	l.submits++
	if i >= len(l.script) {
		l.mu.Unlock()
		l.t.Errorf("unexpected acceptDelivery submission #%d", i+1)
		return funding.ChainOutcome{}, errors.New("unscripted submission")
	}
	step := l.script[i]
	hash := txHashN(i + 1)
	if step.broadcast {
		l.txs[hash] = &trackedTx{state: funding.TxPending}
	}
	l.mu.Unlock()
	if step.broadcast {
		onBroadcast(funding.Broadcast{TxHash: hash, Nonce: uint64(i)})
	}
	out := step.out
	if out.Submitted {
		out.TxHash = hash
	}
	return out, step.err
}

func (l *trackedLane) Resolve(_ context.Context, b funding.Broadcast) (funding.TxState, funding.ChainOutcome, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resolves++
	if l.resolveErr != nil {
		return funding.TxPending, funding.ChainOutcome{}, l.resolveErr
	}
	tx, ok := l.txs[b.TxHash]
	if !ok {
		l.t.Errorf("resolve asked about %s, which this lane never broadcast", b.TxHash)
		return funding.TxPending, funding.ChainOutcome{}, nil
	}
	if tx.state != funding.TxMined {
		return tx.state, funding.ChainOutcome{}, nil
	}
	return funding.TxMined, funding.ChainOutcome{Submitted: true, TxHash: b.TxHash, Block: 9, GasUsed: 90000, Reverted: tx.reverted}, nil
}

func (l *trackedLane) set(n int, st funding.TxState, reverted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.txs[txHashN(n)] = &trackedTx{state: st, reverted: reverted}
}

func (l *trackedLane) counts() (submits, resolves int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.submits, l.resolves
}

var (
	receiptTimeout = trackedStep{broadcast: true, err: errors.New("receipt: deadline exceeded")}
	trackedSettled = trackedStep{broadcast: true, out: funding.ChainOutcome{Submitted: true, GasUsed: 90000}}
)

func trackedRig(t *testing.T, script ...trackedStep) (*Brain, *trackedLane, string) {
	t.Helper()
	b, _, jobID := acceptRig(t)
	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("b") }); err != nil {
		t.Fatal(err)
	}
	lane := newTrackedLane(t, script...)
	b.funding.SetChain(nil, lane)
	return b, lane, jobID
}

func executedRecord(t *testing.T, b *Brain, jobID string) (actor string, p map[string]any) {
	t.Helper()
	var raw string
	if err := b.store.DB().QueryRow(`SELECT actor, payload_json FROM events WHERE kind='settlement.executed' AND entity_id=?`,
		jobID).Scan(&actor, &raw); err != nil {
		t.Fatalf("reading settlement.executed: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return actor, p
}

// The receipt wait failing after the broadcast must not read as "never sent". While that
// transaction is still pending the job is still Delivered on chain, so without a record the
// retry would send a second acceptDelivery that reverts once the first mines, tell the
// customer accepted-settlement-reverted, write a false settlement.reverted, and later
// self-heal the settlement under the customer's name although the daemon's lane signed it.
func TestAcceptBroadcast_ReceiptFailureIsResolvedNotResubmitted(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	var onChainAccepted atomic.Bool
	b.SetSettlementOracle(func(context.Context, string) (bool, error) { return onChainAccepted.Load(), nil })
	b.SetDeliveryOracle(func(context.Context, string) (bool, error) { return !onChainAccepted.Load(), nil })
	ctx := context.Background()

	s1, err := b.AcceptDelivery(ctx, jobID)
	if err != nil || s1 != "accepted-pending-chain" {
		t.Fatalf("accept#1: state=%q err=%v, want accepted-pending-chain: the transaction is out", s1, err)
	}
	if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 1 {
		t.Fatalf("settlement.submitted = %d, want 1 written before the receipt wait", n)
	}

	// tx1 is still pending; the chain still says Delivered.
	for i := 0; i < 3; i++ {
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("accept while tx1 pending: state=%q err=%v", s, err)
		}
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d while the first acceptDelivery was pending, want 1", subs)
	}
	if n := countEvents(t, b.store, "settlement.reverted", jobID); n != 0 {
		t.Fatalf("settlement.reverted = %d, want 0", n)
	}

	// tx1 mines and settles the job.
	lane.set(1, funding.TxMined, false)
	onChainAccepted.Store(true)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("accept after tx1 mined: state=%q err=%v, want accepted-settled", s, err)
	}
	actor, p := executedRecord(t, b, jobID)
	if actor != "funding" || p["signer"] != signerDaemonLane || p["tx_hash"] != txHashN(1) {
		t.Fatalf("settlement.executed actor=%q payload=%v, want the daemon lane's tx1", actor, p)
	}

	// Settled is final: no further resolve, submission or record.
	_, resolves := lane.counts()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("post-settlement accept: state=%q err=%v", s, err)
	}
	if subs, r := lane.counts(); subs != 1 || r != resolves {
		t.Fatalf("post-settlement accept touched the lane: submits=%d resolves=%d->%d", subs, resolves, r)
	}
	if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
		t.Fatalf("settlement.executed = %d, want 1", n)
	}
}

// A recorded broadcast that mined and reverted is recorded as reverted (with its own hash)
// and surfaced like a first attempt's revert; the next Accept resubmits through the usual
// gates.
func TestAcceptBroadcast_RevertedBroadcastAllowsResubmit(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout, trackedSettled)
	ctx := context.Background()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("accept#1: state=%q err=%v", s, err)
	}
	lane.set(1, funding.TxMined, true)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settlement-reverted" {
		t.Fatalf("accept#2: state=%q err=%v, want accepted-settlement-reverted", s, err)
	}
	var raw string
	if err := b.store.DB().QueryRow(`SELECT payload_json FROM events WHERE kind='settlement.reverted' AND entity_id=?`,
		jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["tx_hash"] != txHashN(1) {
		t.Fatalf("settlement.reverted names %v, want tx1", p["tx_hash"])
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d, want 1", subs)
	}
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("accept#3: state=%q err=%v, want accepted-settled", s, err)
	}
	if subs, _ := lane.counts(); subs != 2 {
		t.Fatalf("lane submissions = %d, want 2", subs)
	}
	if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 2 {
		t.Fatalf("settlement.submitted = %d, want 2", n)
	}
	if _, p := executedRecord(t, b, jobID); p["tx_hash"] != txHashN(2) || p["signer"] != signerDaemonLane {
		t.Fatalf("settlement.executed payload %v, want tx2 by the daemon lane", p)
	}
}

// A recorded broadcast that can never mine (its nonce went to another transaction) is
// recorded as dropped, and the same Accept resubmits.
func TestAcceptBroadcast_DroppedBroadcastIsResubmitted(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout, trackedSettled)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.set(1, funding.TxDropped, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("accept#2: state=%q err=%v, want accepted-settled", s, err)
	}
	if n := countEvents(t, b.store, "settlement.dropped", jobID); n != 1 {
		t.Fatalf("settlement.dropped = %d, want 1", n)
	}
	if subs, _ := lane.counts(); subs != 2 {
		t.Fatalf("lane submissions = %d, want 2", subs)
	}
}

// A recorded broadcast the lane cannot read fails closed: an error, and no submission.
func TestAcceptBroadcast_UnreadableBroadcastFailsClosed(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.mu.Lock()
	lane.resolveErr = errors.New("rpc down")
	lane.mu.Unlock()
	if s, err := b.AcceptDelivery(ctx, jobID); err == nil {
		t.Fatalf("accept over an unreadable broadcast returned %q with no error", s)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d, want 1", subs)
	}
}

// The record survives a restart. With the lane wired again the recorded transaction is
// resolved; with no lane wired nothing is resubmitted, and a settlement the chain reports
// is attributed by its indexed hash: the lane's own hash is the daemon's lane, any other
// hash is the customer, and with no indexed hash nothing is claimed yet.
func TestAcceptBroadcast_RestartResolvesTheRecordedBroadcast(t *testing.T) {
	restart := func(t *testing.T, b *Brain, lane funding.Submitter) *Brain {
		t.Helper()
		fund := funding.New()
		b2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), b.store, b.memory, fund)
		if err := b2.Recover(); err != nil {
			t.Fatal(err)
		}
		fund.SetChain(nil, lane)
		return b2
	}
	ctx := context.Background()

	t.Run("lane wired", func(t *testing.T) {
		b, lane, jobID := trackedRig(t, receiptTimeout)
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		lane.set(1, funding.TxMined, false)
		b2 := restart(t, b, lane)
		if s, err := b2.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
			t.Fatalf("after restart: state=%q err=%v", s, err)
		}
		if subs, _ := lane.counts(); subs != 1 {
			t.Fatalf("lane submissions = %d, want 1", subs)
		}
	})

	for _, tc := range []struct {
		name      string
		indexed   string // "" = the indexer has not seen JobSettled
		state     string
		signer    string
		wantActor string
	}{
		{"no lane, not settled on chain", "-", "accepted-pending-chain", "", ""},
		{"no lane, settled, not indexed", "", "accepted-pending-chain", "", ""},
		{"no lane, settled by the lane's tx", txHashN(1), "accepted-settled", signerDaemonLane, "funding"},
		{"no lane, settled by another tx", "0x" + repeat64("d"), "accepted-settled", signerCustomer, "customer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, jobID := trackedRig(t, receiptTimeout)
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			b2 := restart(t, b, nil)
			b2.SetBilling(billing.New(b.store, g12Chain, nil))
			b2.SetSettlementOracle(func(context.Context, string) (bool, error) { return tc.indexed != "-", nil })
			if tc.indexed != "" && tc.indexed != "-" {
				seedChainRow(t, b.store, "JobSettled", "0x"+repeat64("b"), tc.indexed, 77,
					map[string]string{"advanceRepaidAtomic": "0", "operatorNetAtomic": "1000000"})
			}
			if s, err := b2.AcceptDelivery(ctx, jobID); err != nil || s != tc.state {
				t.Fatalf("state=%q err=%v, want %q", s, err, tc.state)
			}
			if tc.signer == "" {
				if n := countEvents(t, b.store, "settlement.executed", jobID); n != 0 {
					t.Fatalf("settlement.executed = %d, want none", n)
				}
				return
			}
			actor, p := executedRecord(t, b2, jobID)
			if actor != tc.wantActor || p["signer"] != tc.signer || p["tx_hash"] != tc.indexed {
				t.Fatalf("actor=%q payload=%v, want signer %q", actor, p, tc.signer)
			}
		})
	}
}

// The lane's broadcast reverts because the customer's own wallet settled first: the revert
// is recorded against the lane's transaction, and the settlement is recorded under the
// customer, from the indexed hash, rather than reported to the customer as a failure.
func TestAcceptBroadcast_RevertAfterCustomerSettledRecordsTheCustomer(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	b.SetBilling(billing.New(b.store, g12Chain, nil))
	customerTx := "0x" + repeat64("d")
	seedChainRow(t, b.store, "JobSettled", "0x"+repeat64("b"), customerTx, 77,
		map[string]string{"advanceRepaidAtomic": "0", "operatorNetAtomic": "1000000"})
	b.SetSettlementOracle(func(context.Context, string) (bool, error) { return true, nil })
	lane.set(1, funding.TxMined, true)

	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("state=%q err=%v, want accepted-settled", s, err)
	}
	if n := countEvents(t, b.store, "settlement.reverted", jobID); n != 1 {
		t.Fatalf("settlement.reverted = %d, want 1 for the lane's transaction", n)
	}
	actor, p := executedRecord(t, b, jobID)
	if actor != "customer" || p["signer"] != signerCustomer || p["tx_hash"] != customerTx {
		t.Fatalf("actor=%q payload=%v, want the customer's transaction", actor, p)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d, want 1", subs)
	}
}
