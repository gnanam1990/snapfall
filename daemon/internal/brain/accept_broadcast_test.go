package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
)

// trackedStep is what one acceptDelivery submission does: whether it is signed (the
// write-ahead hook runs, and the transaction reaches the node unless unsent is set), and
// what the send or receipt wait then returns.
type trackedStep struct {
	broadcast bool
	// unsent: signed and recorded, but the node never took it (it refused the send).
	unsent bool
	out    funding.ChainOutcome
	err    error
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
	// head is the chain head each Resolve reports; it advances by one per Resolve unless
	// stillHead is set (a chain that has not moved between two looks).
	head      uint64
	stillHead bool
	// rebroadcasts are the broadcasts re-sent, in order; rebroadcastErr fails them.
	rebroadcasts   []funding.Broadcast
	rebroadcastErr error
	// sent are the broadcasts the lane reported to its hook, in order.
	sent []funding.Broadcast
	// replacements are the fee replacements the lane signed, in order; replaceErr refuses
	// them before signing, replaceSendErr fails their send after the hook.
	replacements   []funding.Broadcast
	replaceErr     error
	replaceSendErr error
	// replaceCalls counts every Replace asked of the lane, refused ones included.
	replaceCalls int
}

var _ funding.TrackedSubmitter = (*trackedLane)(nil)

func newTrackedLane(t *testing.T, script ...trackedStep) *trackedLane {
	return &trackedLane{t: t, script: script, txs: map[string]*trackedTx{}}
}

func txHashN(n int) string { return fmt.Sprintf("0x%064x", n) }

// laneSigner is the address the tracked lane signs as.
var laneSigner = common.HexToAddress("0x00000000000000000000000000000000000000a1")

func (l *trackedLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	l.t.Error("a tracked lane was submitted to without its broadcast hook")
	return funding.ChainOutcome{}, errors.New("untracked submission")
}

func (l *trackedLane) SubmitTracked(_ context.Context, _ []byte, onSigned func(funding.Broadcast) error) (funding.ChainOutcome, error) {
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
	bc := funding.Broadcast{TxHash: hash, Nonce: uint64(i), Signer: laneSigner, Raw: []byte{0xf8, byte(i + 1)}, SentHead: l.head}
	l.mu.Unlock()
	if step.broadcast {
		// Write-ahead, as the real lane: the hook runs before anything is sent, and a failed
		// record sends nothing.
		if err := onSigned(bc); err != nil {
			return funding.ChainOutcome{}, fmt.Errorf("not sent: %w", err)
		}
		l.mu.Lock()
		st := funding.TxPending
		if step.unsent {
			st = funding.TxEvicted
		}
		l.txs[hash] = &trackedTx{state: st}
		l.sent = append(l.sent, bc)
		l.mu.Unlock()
	}
	out := step.out
	if out.Submitted {
		out.TxHash = hash
	}
	return out, step.err
}

func (l *trackedLane) Resolve(_ context.Context, b funding.Broadcast) (funding.Resolution, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resolves++
	if !l.stillHead {
		l.head++
	}
	if l.resolveErr != nil {
		return funding.Resolution{State: funding.TxPending}, l.resolveErr
	}
	tx, ok := l.txs[b.TxHash]
	if !ok {
		l.t.Errorf("resolve asked about %s, which this lane never broadcast", b.TxHash)
		return funding.Resolution{State: funding.TxPending, Head: l.head}, nil
	}
	if tx.state != funding.TxMined {
		return funding.Resolution{State: tx.state, Head: l.head}, nil
	}
	return funding.Resolution{State: funding.TxMined, Head: l.head,
		Outcome: funding.ChainOutcome{Submitted: true, TxHash: b.TxHash, Block: 9, GasUsed: 90000, Reverted: tx.reverted}}, nil
}

func (l *trackedLane) Rebroadcast(_ context.Context, b funding.Broadcast) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebroadcastErr != nil {
		return l.rebroadcastErr
	}
	l.rebroadcasts = append(l.rebroadcasts, b)
	if tx, ok := l.txs[b.TxHash]; ok && tx.state == funding.TxEvicted {
		tx.state = funding.TxPending // back in the mempool
	}
	return nil
}

func (l *trackedLane) Replace(_ context.Context, _ []byte, prev funding.Broadcast, onSigned func(funding.Broadcast) error) (funding.Broadcast, error) {
	l.mu.Lock()
	l.replaceCalls++
	if l.replaceErr != nil {
		err := l.replaceErr
		l.mu.Unlock()
		return funding.Broadcast{}, err
	}
	k := len(l.replacements) + 1
	bc := funding.Broadcast{TxHash: replHash(k), Nonce: prev.Nonce, Signer: laneSigner, Raw: []byte{0xf9, byte(k)}, SentHead: l.head}
	sendErr := l.replaceSendErr
	l.mu.Unlock()
	if err := onSigned(bc); err != nil {
		return funding.Broadcast{}, fmt.Errorf("replacement not sent: %w", err)
	}
	l.mu.Lock()
	l.replacements = append(l.replacements, bc)
	l.txs[bc.TxHash] = &trackedTx{state: funding.TxPending}
	l.mu.Unlock()
	return bc, sendErr
}

// replHash is the hash of the lane's k-th fee replacement.
func replHash(k int) string { return fmt.Sprintf("0x%064x", 1000+k) }

func (l *trackedLane) set(n int, st funding.TxState, reverted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.txs[txHashN(n)] = &trackedTx{state: st, reverted: reverted}
}

func (l *trackedLane) setHash(h string, st funding.TxState, reverted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.txs[h] = &trackedTx{state: st, reverted: reverted}
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
// recorded as dropped only after TWO observations agree with the chain head advancing
// between them; the first is persisted as settlement.unseen. The Accept that confirms it
// resubmits.
func TestAcceptBroadcast_DroppedBroadcastIsConfirmedThenResubmitted(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout, trackedSettled)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.set(1, funding.TxDropped, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("accept#2 (first dropped look): state=%q err=%v, want accepted-pending-chain", s, err)
	}
	if n := countEvents(t, b.store, "settlement.dropped", jobID); n != 0 {
		t.Fatalf("settlement.dropped = %d after ONE observation, want 0", n)
	}
	if n := countEvents(t, b.store, "settlement.unseen", jobID); n != 1 {
		t.Fatalf("settlement.unseen = %d, want the first observation persisted", n)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d after one dropped look, want 1", subs)
	}
	if s, err := b.SettlementStatus(ctx, jobID); err != nil || s != SettlementPending {
		t.Fatalf("status = %q err=%v, want pending", s, err)
	}
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("accept#3 (second look, higher head): state=%q err=%v, want accepted-settled", s, err)
	}
	if n := countEvents(t, b.store, "settlement.dropped", jobID); n != 1 {
		t.Fatalf("settlement.dropped = %d, want 1", n)
	}
	if subs, _ := lane.counts(); subs != 2 {
		t.Fatalf("lane submissions = %d, want 2", subs)
	}
}

// The lagging-receipt case end to end: one look says "dropped" (a load-balanced RPC whose
// nonce backend is ahead), and the chain head has NOT advanced by the next look, so nothing
// is recorded as dropped and nothing is resubmitted however often the customer presses
// Accept. When the receipt becomes readable the broadcast resolves as mined, and the first
// observation is survived by a restart rather than repeated.
func TestAcceptBroadcast_LaggingReceiptIsNeverRecordedDropped(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.mu.Lock()
	lane.stillHead = true
	lane.mu.Unlock()
	lane.set(1, funding.TxDropped, false)
	for i := 0; i < 4; i++ {
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("accept with the head still: state=%q err=%v", s, err)
		}
	}
	if n := countEvents(t, b.store, "settlement.dropped", jobID); n != 0 {
		t.Fatalf("settlement.dropped = %d with the chain head unmoved, want 0", n)
	}
	if n := countEvents(t, b.store, "settlement.unseen", jobID); n != 1 {
		t.Fatalf("settlement.unseen = %d, want exactly the first observation", n)
	}
	// A restart keeps that observation (it is in the log), and the receipt is readable now.
	fund := funding.New()
	b2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), b.store, b.memory, fund)
	if err := b2.Recover(); err != nil {
		t.Fatal(err)
	}
	fund.SetChain(nil, lane)
	lane.set(1, funding.TxMined, false)
	if s, err := b2.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("after the receipt appeared: state=%q err=%v, want accepted-settled", s, err)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d, want 1", subs)
	}
}

// An evicted broadcast (unknown to the node, its nonce still open) is NOT left pending
// forever: confirmed by a second look at a higher head, its recorded signed bytes are sent
// again unchanged, which cannot settle twice, and it then mines and settles. A failed
// re-send records nothing; a frozen job re-sends nothing.
func TestAcceptBroadcast_EvictedBroadcastIsRebroadcastUnchanged(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.set(1, funding.TxEvicted, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("first evicted look: state=%q err=%v", s, err)
	}
	if len(lane.rebroadcasts) != 0 {
		t.Fatal("re-sent after a single observation")
	}
	lane.mu.Lock()
	lane.rebroadcastErr = errors.New("node refused")
	lane.mu.Unlock()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("failed re-send: state=%q err=%v", s, err)
	}
	if n := countEvents(t, b.store, "settlement.rebroadcast", jobID); n != 0 {
		t.Fatalf("settlement.rebroadcast = %d after a failed re-send, want 0", n)
	}
	lane.mu.Lock()
	lane.rebroadcastErr = nil
	lane.mu.Unlock()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("re-send: state=%q err=%v", s, err)
	}
	lane.mu.Lock()
	got := append([]funding.Broadcast(nil), lane.rebroadcasts...)
	orig := lane.sent[0]
	lane.mu.Unlock()
	if len(got) != 1 || got[0].TxHash != orig.TxHash || got[0].Nonce != orig.Nonce ||
		got[0].Signer != orig.Signer || string(got[0].Raw) != string(orig.Raw) {
		t.Fatalf("re-sent %+v, want exactly the recorded broadcast %+v", got, orig)
	}
	if n := countEvents(t, b.store, "settlement.rebroadcast", jobID); n != 1 {
		t.Fatalf("settlement.rebroadcast = %d, want 1", n)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("lane submissions = %d, want 1: a re-send is not a new submission", subs)
	}
	lane.set(1, funding.TxMined, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("after the re-sent tx mined: state=%q err=%v", s, err)
	}
	if _, p := executedRecord(t, b, jobID); p["tx_hash"] != txHashN(1) {
		t.Fatalf("settled by %v, want the original tx1", p["tx_hash"])
	}
}

// Freeze stops a re-send like it stops a settlement: the confirmation happens, nothing is sent.
func TestAcceptBroadcast_FrozenJobIsNotRebroadcast(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.set(1, funding.TxEvicted, false)
	for i := 0; i < 2; i++ {
		if _, err := b.ResolveOpenSettlements(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			reg, err := freeze.NewRegistry(ctx, b.store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			b.SetFreeze(reg, "org_demo")
			if _, err := reg.Engage(ctx, freeze.KindJob, jobID, "gnanam", "incident"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(lane.rebroadcasts) != 0 || countEvents(t, b.store, "settlement.rebroadcast", jobID) != 0 {
		t.Fatalf("a frozen job was re-sent (%d)", len(lane.rebroadcasts))
	}
}

// The broadcast record names the signing ADDRESS and the signed bytes, and a resolution
// asks about exactly that signer: a key rotated across a restart cannot redirect it.
func TestAcceptBroadcast_SubmittedRecordNamesSignerAndBytes(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := b.store.DB().QueryRow(`SELECT payload_json FROM events WHERE kind='settlement.submitted' AND entity_id=?`,
		jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["signer_address"] != laneSigner.Hex() || p["raw_tx"] != "0xf801" || p["signer"] != signerDaemonLane {
		t.Fatalf("settlement.submitted payload %v, want signer_address %s and raw_tx 0xf801", p, laneSigner.Hex())
	}
	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil || len(rec.unresolved) != 1 {
		t.Fatalf("record %+v err=%v", rec, err)
	}
	if got := rec.unresolved[0]; got.Signer != laneSigner || string(got.Raw) != "\xf8\x01" {
		t.Fatalf("read back %+v, want the recorded signer and bytes", got)
	}
	_ = lane
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

// submittedPayloads are the job's settlement.submitted payloads, oldest first.
func submittedPayloads(t *testing.T, b *Brain, jobID string) []map[string]any {
	t.Helper()
	rows, err := b.store.DB().Query(`SELECT payload_json FROM events WHERE kind='settlement.submitted' AND entity_id=? ORDER BY seq`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// The reviewer's wedge: a recorded acceptDelivery the node HOLDS but never mines (priced
// under what the chain takes). Its nonce never moves, so it is never dropped, and a re-send
// of the same bytes changes nothing. Once it has sat replaceAfterBlocks since it was signed,
// it is re-signed at the SAME nonce with a higher fee, recorded write-ahead as a
// settlement.submitted that `replaces` it. Replacements are bounded: past the maximum, one
// settlement.stuck is recorded and nothing more is sent. Whichever transaction at the nonce
// mines settles the job and every other one is recorded dropped; no new nonce is ever used.
// The replacement chain survives a restart (it is read back from the log).
func TestAcceptBroadcast_StalledPendingIsReplacedAtTheSameNonce(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	b.mu.Lock()
	b.replaceAfterBlocks, b.maxReplacements = 3, 2
	b.mu.Unlock()
	lane.mu.Lock()
	lane.head = 10
	lane.mu.Unlock()
	ctx := context.Background()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
		t.Fatalf("accept#1: state=%q err=%v", s, err)
	}
	accept := func() {
		t.Helper()
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("accept while stalled: state=%q err=%v", s, err)
		}
	}
	// Heads 11 and 12: younger than 3 blocks since sent_head 10, nothing is replaced.
	accept()
	accept()
	if len(lane.replacements) != 0 {
		t.Fatalf("replaced after %d blocks, want none before 3", 2)
	}
	accept() // head 13: replaced
	lane.mu.Lock()
	r := append([]funding.Broadcast(nil), lane.replacements...)
	lane.mu.Unlock()
	if len(r) != 1 || r[0].Nonce != 0 {
		t.Fatalf("replacements %+v, want one at the original nonce 0", r)
	}
	ps := submittedPayloads(t, b, jobID)
	if len(ps) != 2 || ps[1]["tx_hash"] != replHash(1) || ps[1]["replaces"] != txHashN(1) || ps[1]["nonce"] != float64(0) {
		t.Fatalf("submitted records %v, want the replacement recorded with replaces=tx1 at nonce 0", ps)
	}
	wantStatus(t, b, jobID, SettlementPending)

	// A restart keeps the chain: the replacement is still linked to tx1's nonce.
	fund := funding.New()
	b2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), b.store, b.memory, fund)
	if err := b2.Recover(); err != nil {
		t.Fatal(err)
	}
	fund.SetChain(nil, lane)
	b2.replaceAfterBlocks, b2.maxReplacements = 3, 2
	b = b2

	// Each pass now resolves both (the head moves by two); replacement 1 was signed at 13.
	accept() // heads 14, 15
	accept() // heads 16, 17: replaced again (the maximum)
	accept()
	accept()
	accept()
	if n := len(lane.replacements); n != 2 {
		t.Fatalf("replacements = %d, want exactly the maximum of 2", n)
	}
	if n := countEvents(t, b.store, "settlement.stuck", jobID); n != 1 {
		t.Fatalf("settlement.stuck = %d, want exactly one once the maximum was reached", n)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("SubmitTracked calls = %d, want 1: a replacement is never a new nonce", subs)
	}

	// The first replacement mines: the job settles under its hash; tx1 and replacement 2
	// can never mine now and are recorded dropped, naming what took the nonce.
	lane.setHash(replHash(1), funding.TxMined, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("after a replacement mined: state=%q err=%v, want accepted-settled", s, err)
	}
	if _, p := executedRecord(t, b, jobID); p["tx_hash"] != replHash(1) {
		t.Fatalf("settled by %v, want replacement 1", p["tx_hash"])
	}
	rows, err := b.store.DB().Query(`SELECT payload_json FROM events WHERE kind='settlement.dropped' AND entity_id=? ORDER BY seq`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var dropped []string
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		var p map[string]any
		_ = json.Unmarshal([]byte(raw), &p)
		if p["taken_by"] != replHash(1) {
			t.Errorf("dropped %v does not name the transaction that took the nonce", p)
		}
		dropped = append(dropped, p["tx_hash"].(string))
	}
	rows.Close()
	if len(dropped) != 2 || dropped[0] != txHashN(1) || dropped[1] != replHash(2) {
		t.Fatalf("dropped %v, want tx1 and replacement 2", dropped)
	}
	if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
		t.Fatalf("settlement.executed = %d, want 1", n)
	}
}

// A replacement is gated like every settlement write: frozen, nothing is replaced; a
// replacement whose write-ahead record fails is NOT sent; one whose send then fails is
// recorded and resolved by hash like any other.
func TestAcceptBroadcast_ReplacementIsFrozenGatedAndWriteAhead(t *testing.T) {
	ctx := context.Background()
	rig := func(t *testing.T) (*Brain, *trackedLane, string) {
		b, lane, jobID := trackedRig(t, receiptTimeout)
		b.replaceAfterBlocks, b.maxReplacements = 1, 3
		lane.head = 10
		if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
			t.Fatal(err)
		}
		return b, lane, jobID
	}
	t.Run("frozen", func(t *testing.T) {
		b, lane, jobID := rig(t)
		reg, err := freeze.NewRegistry(ctx, b.store, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		b.SetFreeze(reg, "org_demo")
		if _, err := reg.Engage(ctx, freeze.KindJob, jobID, "gnanam", "incident"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, err := b.ResolveOpenSettlements(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if len(lane.replacements) != 0 || countEvents(t, b.store, "settlement.submitted", jobID) != 1 {
			t.Fatalf("a frozen job was replaced (%d)", len(lane.replacements))
		}
	})
	t.Run("record fails", func(t *testing.T) {
		b, lane, _ := rig(t)
		lift := failSubmittedRecords(t, b)
		for i := 0; i < 3; i++ {
			if _, err := b.ResolveOpenSettlements(ctx); err != nil {
				t.Fatal(err)
			}
		}
		lift()
		if len(lane.replacements) != 0 {
			t.Fatalf("%d replacements reached the node although their record failed", len(lane.replacements))
		}
	})
	t.Run("send fails after the record", func(t *testing.T) {
		b, lane, jobID := rig(t)
		lane.mu.Lock()
		lane.replaceSendErr = errors.New("send: context deadline exceeded")
		lane.mu.Unlock()
		if _, err := b.ResolveOpenSettlements(ctx); err != nil {
			t.Fatal(err)
		}
		if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 2 {
			t.Fatalf("settlement.submitted = %d, want the replacement recorded", n)
		}
		lane.setHash(replHash(1), funding.TxMined, false)
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
			t.Fatalf("the unacknowledged replacement mined: state=%q err=%v", s, err)
		}
	})
}

// An evicted transaction whose re-send the node refuses (priced under the base fee, say)
// is not left open forever either: it is replaced at its nonce.
func TestAcceptBroadcast_EvictedAndRefusedIsReplaced(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	b.replaceAfterBlocks = 2
	lane.head = 10
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	lane.set(1, funding.TxEvicted, false)
	lane.mu.Lock()
	lane.rebroadcastErr = errors.New("rebroadcast: RPC error -32000: transaction underpriced")
	lane.mu.Unlock()
	for i := 0; i < 3 && len(lane.replacements) == 0; i++ {
		if _, err := b.ResolveOpenSettlements(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(lane.replacements) != 1 || lane.replacements[0].Nonce != 0 {
		t.Fatalf("replacements %+v, want one at nonce 0", lane.replacements)
	}
	lane.setHash(replHash(1), funding.TxMined, false)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("state=%q err=%v, want accepted-settled", s, err)
	}
}

// stuckPayloads are the job's settlement.stuck payloads, oldest first.
func stuckPayloads(t *testing.T, b *Brain, jobID string) []map[string]any {
	t.Helper()
	rows, err := b.store.DB().Query(`SELECT payload_json FROM events WHERE kind='settlement.stuck' AND entity_id=? ORDER BY seq`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// syncBuffer is a log sink the race detector accepts.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// A stalled broadcast the lane refuses to replace FOR GOOD (another key signed it after a
// rotation, outbidding it would pass the fee cap, its recorded bytes are not its
// transaction, or it names no signer) is recorded settlement.stuck the first time, with the
// reason, logged at ERROR, exactly once; the lane is not asked again. A refusal that may
// pass (an RPC error) records nothing and is asked again next pass.
func TestAcceptBroadcast_PermanentReplaceRefusalIsRecordedStuckOnce(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("replace 0x01: %w", funding.ErrSignerMismatch), "signer_mismatch"},
		{fmt.Errorf("replace 0x01: %w: over the cap", funding.ErrFeeCapExceeded), "fee_cap"},
		{fmt.Errorf("replace 0x01: %w: swapped bytes", funding.ErrBroadcastMismatch), "record_mismatch"},
		{fmt.Errorf("replace 0x01: %w", funding.ErrSignerUnknown), "signer_unknown"},
		{errors.New("gas price: dial tcp: connection refused"), ""},
	} {
		name := tc.reason
		if name == "" {
			name = "transient"
		}
		t.Run(name, func(t *testing.T) {
			b, lane, jobID := trackedRig(t, receiptTimeout)
			logs := &syncBuffer{}
			b.log = slog.New(slog.NewTextHandler(logs, nil))
			b.replaceAfterBlocks = 1
			lane.head = 10
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			lane.mu.Lock()
			lane.replaceErr = tc.err
			lane.mu.Unlock()
			for i := 0; i < 4; i++ {
				if _, err := b.ResolveOpenSettlements(ctx); err != nil {
					t.Fatal(err)
				}
			}
			lane.mu.Lock()
			calls := lane.replaceCalls
			lane.mu.Unlock()
			stuck := stuckPayloads(t, b, jobID)
			if tc.reason == "" {
				if len(stuck) != 0 || calls < 2 {
					t.Fatalf("transient refusal: stuck=%v replace calls=%d, want none and asked again", stuck, calls)
				}
				return
			}
			if len(stuck) != 1 || stuck[0]["reason"] != tc.reason || stuck[0]["tx_hash"] != txHashN(1) || stuck[0]["nonce"] != float64(0) {
				t.Fatalf("settlement.stuck %v, want exactly one for tx1 with reason %q", stuck, tc.reason)
			}
			if calls != 1 {
				t.Fatalf("replace calls = %d, want 1: a stuck transaction is not asked about again", calls)
			}
			if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "reason="+tc.reason) {
				t.Fatalf("no ERROR log naming the reason: %s", out)
			}
			if len(lane.replacements) != 0 {
				t.Fatalf("%d replacements were sent", len(lane.replacements))
			}
			wantStatus(t, b, jobID, SettlementPending)
		})
	}
}

// A recorded broadcast the wired lane can never resolve (no signer and no bytes, so with no
// receipt and unknown to the node it has no nonce to compare; or bytes that are not its
// transaction) is recorded settlement.stuck once, at ERROR, and nothing is sent: not a
// resubmission, a re-send or a replacement. A lane that merely cannot answer (none wired)
// stays pending with no stuck record.
func TestAcceptBroadcast_UnresolvableBroadcastIsRecordedStuckOnce(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"signer_unknown", fmt.Errorf("%w: resolve 0x01: %w", funding.ErrSettlementUnresolvable, funding.ErrSignerUnknown), "signer_unknown"},
		{"record_mismatch", fmt.Errorf("%w: %w: swapped bytes", funding.ErrSettlementUnresolvable, funding.ErrBroadcastMismatch), "record_mismatch"},
		{"no lane answers", funding.ErrSettlementUnresolvable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, lane, jobID := trackedRig(t, receiptTimeout)
			logs := &syncBuffer{}
			b.log = slog.New(slog.NewTextHandler(logs, nil))
			b.replaceAfterBlocks = 1
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			lane.mu.Lock()
			lane.resolveErr = tc.err
			lane.mu.Unlock()
			for i := 0; i < 3; i++ {
				if _, err := b.ResolveOpenSettlements(ctx); err != nil {
					t.Fatal(err)
				}
				if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
					t.Fatalf("accept over an unresolvable broadcast: state=%q err=%v", s, err)
				}
			}
			stuck := stuckPayloads(t, b, jobID)
			if tc.reason == "" {
				if len(stuck) != 0 {
					t.Fatalf("settlement.stuck %v with no lane able to answer, want none", stuck)
				}
			} else {
				if len(stuck) != 1 || stuck[0]["reason"] != tc.reason || stuck[0]["tx_hash"] != txHashN(1) || stuck[0]["nonce"] != float64(0) {
					t.Fatalf("settlement.stuck %v, want exactly one for tx1 with reason %q", stuck, tc.reason)
				}
				if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "reason="+tc.reason) {
					t.Fatalf("no ERROR log naming the reason: %s", out)
				}
			}
			lane.mu.Lock()
			subs, calls, resent := lane.submits, lane.replaceCalls, len(lane.rebroadcasts)
			lane.mu.Unlock()
			if subs != 1 || calls != 0 || resent != 0 {
				t.Fatalf("submissions=%d replace calls=%d rebroadcasts=%d, want 1/0/0: nothing is sent", subs, calls, resent)
			}
			wantStatus(t, b, jobID, SettlementPending)
		})
	}
}

// An evicted transaction the node keeps evicting is re-sent at most maxRebroadcasts times
// (each re-send starts its observations over, so without a bound it would stay open
// forever); past that it falls through to fee replacement at its nonce.
func TestAcceptBroadcast_RebroadcastsAreBoundedThenReplaced(t *testing.T) {
	b, lane, jobID := trackedRig(t, receiptTimeout)
	b.replaceAfterBlocks = 1
	lane.head = 10
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	evictedTwoLooks := func() {
		t.Helper()
		lane.set(1, funding.TxEvicted, false)
		for i := 0; i < 2; i++ {
			if _, err := b.ResolveOpenSettlements(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for round := 1; round <= defaultSettlementMaxRebroadcasts; round++ {
		evictedTwoLooks()
		lane.mu.Lock()
		n, r := len(lane.rebroadcasts), len(lane.replacements)
		lane.mu.Unlock()
		if n != round || r != 0 {
			t.Fatalf("round %d: rebroadcasts=%d replacements=%d, want %d and 0", round, n, r, round)
		}
	}
	evictedTwoLooks()
	lane.mu.Lock()
	n, r := len(lane.rebroadcasts), append([]funding.Broadcast(nil), lane.replacements...)
	lane.mu.Unlock()
	if n != defaultSettlementMaxRebroadcasts {
		t.Fatalf("rebroadcasts = %d, want the bound %d", n, defaultSettlementMaxRebroadcasts)
	}
	if len(r) != 1 || r[0].Nonce != 0 {
		t.Fatalf("replacements %+v, want one at nonce 0 once the re-sends ran out", r)
	}
	if c := countEvents(t, b.store, "settlement.rebroadcast", jobID); c != defaultSettlementMaxRebroadcasts {
		t.Fatalf("settlement.rebroadcast = %d, want %d", c, defaultSettlementMaxRebroadcasts)
	}
	if subs, _ := lane.counts(); subs != 1 {
		t.Fatalf("submits=%d, want 1", subs)
	}
}
