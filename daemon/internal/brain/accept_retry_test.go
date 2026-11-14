package brain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/funding"
)

// scriptedLane answers each Submit with the next scripted outcome and counts calls. A
// call beyond the script fails the test: it is an extra acceptDelivery submission.
type scriptedLane struct {
	t     *testing.T
	mu    sync.Mutex
	calls int
	outs  []funding.ChainOutcome
	errs  []error
}

func (s *scriptedLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i >= len(s.outs) {
		s.t.Errorf("unexpected acceptDelivery submission #%d", i+1)
		return funding.ChainOutcome{}, errors.New("unscripted submission")
	}
	return s.outs[i], s.errs[i]
}

func (s *scriptedLane) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

var (
	revertedOut = funding.ChainOutcome{Submitted: true, TxHash: "0xr1", Reverted: true}
	settledOut  = funding.ChainOutcome{Submitted: true, TxHash: "0xok", GasUsed: 90000}
)

func retryRig(t *testing.T, outs []funding.ChainOutcome, errs []error) (*Brain, *scriptedLane, string) {
	t.Helper()
	b, _, jobID := acceptRig(t)
	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("b") }); err != nil {
		t.Fatal(err)
	}
	lane := &scriptedLane{t: t, outs: outs, errs: errs}
	b.funding.SetChain(nil, lane)
	return b, lane, jobID
}

// A failed settlement attempt must not consume the Accept claim: after a mined revert, or a
// submission that failed before anything was signed (gas estimation refused the call, so no
// transaction exists), the next Accept submits again and settles. An error AFTER signing is
// a different case and is never retried this way (see
// TestAcceptRetry_SendTimeoutAfterSigningIsResolvedByHash).
func TestAcceptRetry_FailedAttemptIsRetried(t *testing.T) {
	cases := []struct {
		name       string
		first      funding.ChainOutcome
		firstErr   error
		firstState string
		reverted   int
	}{
		{"mined revert", revertedOut, nil, "accepted-settlement-reverted", 1},
		{"refused before signing", funding.ChainOutcome{}, errors.New("gas estimate (the call likely reverts): execution reverted"), "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, lane, jobID := retryRig(t,
				[]funding.ChainOutcome{tc.first, settledOut}, []error{tc.firstErr, nil})
			ctx := context.Background()

			s1, err := b.AcceptDelivery(ctx, jobID)
			if (err != nil) != (tc.firstErr != nil) || s1 != tc.firstState {
				t.Fatalf("first accept: state=%q err=%v", s1, err)
			}
			if js, _ := b.Job(jobID); js.Stage != StageAccepted {
				t.Fatalf("stage after first attempt %v, want accepted", js.Stage)
			}

			s2, err := b.AcceptDelivery(ctx, jobID)
			if err != nil || s2 != "accepted-settled" {
				t.Fatalf("retry: state=%q err=%v, want accepted-settled", s2, err)
			}
			if lane.n() != 2 {
				t.Fatalf("lane calls = %d, want 2", lane.n())
			}
			if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
				t.Fatalf("settlement.executed = %d, want 1", n)
			}
			if n := countEvents(t, b.store, "settlement.reverted", jobID); n != tc.reverted {
				t.Fatalf("settlement.reverted = %d, want %d", n, tc.reverted)
			}

			// Settled is final: further Accepts never touch the lane again.
			for i := 0; i < 3; i++ {
				if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
					t.Fatalf("post-settlement accept: state=%q err=%v", s, err)
				}
			}
			if lane.n() != 2 || countEvents(t, b.store, "settlement.executed", jobID) != 1 {
				t.Fatal("an Accept after settlement resubmitted or re-recorded")
			}
		})
	}
}

// The reviewer's case: eth_sendRawTransaction times out (or answers with an error) AFTER the
// transaction was signed. The lane signs locally, so the transaction was recorded before the
// send, and a send error is never proof it was not sent: the node may hold it. The Accept
// reports pending, and every later Accept resolves that exact transaction by hash and submits
// nothing new while it may mine. A second transaction is never sent.
//
// Even when the node DID refuse it (the "nonce too low" case: its nonce was taken), the
// record is resolved by hash too, and only a confirmed drop lets a new submission through.
func TestAcceptRetry_SendTimeoutAfterSigningIsResolvedByHash(t *testing.T) {
	ctx := context.Background()
	t.Run("node accepted, response lost", func(t *testing.T) {
		sendTimeout := trackedStep{broadcast: true, err: errors.New("send (the transaction may still have reached the node; resolve it by hash): context deadline exceeded")}
		b, lane, jobID := trackedRig(t, sendTimeout)
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("accept with a lost send ack: state=%q err=%v, want accepted-pending-chain", s, err)
		}
		if n := countEvents(t, b.store, "settlement.submitted", jobID); n != 1 {
			t.Fatalf("settlement.submitted = %d, want the write-ahead record", n)
		}
		wantStatus(t, b, jobID, SettlementPending)
		for i := 0; i < 3; i++ {
			if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
				t.Fatalf("retry while it may mine: state=%q err=%v", s, err)
			}
		}
		if subs, resolves := lane.counts(); subs != 1 || resolves < 3 {
			t.Fatalf("submits=%d resolves=%d, want 1 submission and the record resolved on every retry", subs, resolves)
		}
		lane.set(1, funding.TxMined, false)
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
			t.Fatalf("after it mined: state=%q err=%v, want accepted-settled", s, err)
		}
		if _, p := executedRecord(t, b, jobID); p["tx_hash"] != txHashN(1) {
			t.Fatalf("settled by %v, want the first (and only) transaction", p["tx_hash"])
		}
		if subs, _ := lane.counts(); subs != 1 {
			t.Fatalf("submits=%d, want 1: no duplicate acceptDelivery", subs)
		}
	})
	t.Run("node refused it", func(t *testing.T) {
		refused := trackedStep{broadcast: true, unsent: true, err: errors.New("send: RPC error -32000: nonce too low")}
		b, lane, jobID := trackedRig(t, refused, trackedSettled)
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("accept with a refused send: state=%q err=%v, want accepted-pending-chain", s, err)
		}
		lane.set(1, funding.TxDropped, false)
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("first dropped look: state=%q err=%v", s, err)
		}
		if subs, _ := lane.counts(); subs != 1 {
			t.Fatalf("submits=%d after one look, want 1", subs)
		}
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
			t.Fatalf("confirmed dropped: state=%q err=%v, want a resubmission that settles", s, err)
		}
		if subs, _ := lane.counts(); subs != 2 || countEvents(t, b.store, "settlement.dropped", jobID) != 1 {
			t.Fatalf("submits=%d dropped=%d, want 2 and 1", subs, countEvents(t, b.store, "settlement.dropped", jobID))
		}
	})
}

// An already-settled job stays idempotent, whether it settled through the lane or the
// record came from somewhere else: no submission, no second record.
func TestAcceptRetry_SettledStaysIdempotent(t *testing.T) {
	b, lane, jobID := retryRig(t, []funding.ChainOutcome{settledOut}, []error{nil})
	ctx := context.Background()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("first accept: state=%q err=%v", s, err)
	}
	for i := 0; i < 3; i++ {
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("repeat accept: state=%q err=%v", s, err)
		}
	}
	if lane.n() != 1 {
		t.Fatalf("lane calls = %d, want 1", lane.n())
	}
	if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
		t.Fatalf("settlement.executed = %d, want 1", n)
	}
}

// blockingLane holds every submission until released, so a test can pile concurrent
// Accepts onto one in-flight attempt.
type blockingLane struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	out     funding.ChainOutcome
}

func (l *blockingLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	l.calls.Add(1)
	l.entered <- struct{}{}
	<-l.release
	return l.out, nil
}

// Concurrent Accepts submit once, both on the first claim and on a retry: everyone who
// arrives while an attempt is in flight returns without reaching the lane.
func TestAcceptRetry_ConcurrentAcceptsSubmitOnce(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "first claim"
		if retry {
			name = "retry after revert"
		}
		t.Run(name, func(t *testing.T) {
			b, _, jobID := acceptRig(t)
			if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("b") }); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if retry {
				b.funding.SetChain(nil, fakeSettleLane{out: revertedOut})
				if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settlement-reverted" {
					t.Fatalf("seeding revert: state=%q err=%v", s, err)
				}
			}
			lane := &blockingLane{entered: make(chan struct{}, 16), release: make(chan struct{}), out: settledOut}
			b.funding.SetChain(nil, lane)

			first := make(chan string, 1)
			go func() {
				s, err := b.AcceptDelivery(ctx, jobID)
				if err != nil {
					t.Errorf("in-flight accept: %v", err)
				}
				first <- s
			}()
			select {
			case <-lane.entered:
			case s := <-first:
				t.Fatalf("the Accept returned %q without reaching the lane", s)
			case <-time.After(5 * time.Second):
				t.Fatal("the Accept never reached the lane")
			}

			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s, err := b.AcceptDelivery(ctx, jobID)
					if err != nil || s != "accepted-pending-chain" {
						t.Errorf("concurrent accept: state=%q err=%v", s, err)
					}
				}()
			}
			waited := make(chan struct{})
			go func() { wg.Wait(); close(waited) }()
			select {
			case <-waited: // none of them blocked on the lane
			case <-time.After(5 * time.Second):
				close(lane.release)
				t.Fatalf("concurrent Accepts blocked behind the in-flight attempt (lane calls=%d)", lane.calls.Load())
			}
			close(lane.release)
			if s := <-first; s != "accepted-settled" {
				t.Fatalf("in-flight accept state %q, want accepted-settled", s)
			}
			if n := lane.calls.Load(); n != 1 {
				t.Fatalf("lane calls = %d, want 1", n)
			}
			if n := countEvents(t, b.store, "settlement.executed", jobID); n != 1 {
				t.Fatalf("settlement.executed = %d, want 1", n)
			}
			b.mu.Lock()
			left := len(b.settling)
			b.mu.Unlock()
			if left != 0 {
				t.Fatalf("settling markers left behind: %d", left)
			}
		})
	}
}

// With the delivery oracle wired, a retry submits only when the job is Delivered on chain:
// a customer repeating Accept before the operator anchors delivery costs no gas and writes
// no records.
func TestAcceptRetry_WaitsForDeliveryOnChain(t *testing.T) {
	b, lane, jobID := retryRig(t, []funding.ChainOutcome{revertedOut, settledOut}, []error{nil, nil})
	ctx := context.Background()
	var delivered atomic.Bool
	b.SetDeliveryOracle(func(context.Context, string) (bool, error) { return delivered.Load(), nil })
	b.SetSettlementOracle(func(context.Context, string) (bool, error) { return false, nil })

	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settlement-reverted" {
		t.Fatalf("first accept: state=%q err=%v", s, err)
	}
	for i := 0; i < 5; i++ {
		if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
			t.Fatalf("early retry: state=%q err=%v", s, err)
		}
	}
	if lane.n() != 1 || countEvents(t, b.store, "settlement.reverted", jobID) != 1 {
		t.Fatalf("early retries submitted: lane calls=%d", lane.n())
	}

	delivered.Store(true)
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("retry after delivery: state=%q err=%v", s, err)
	}
	if lane.n() != 2 {
		t.Fatalf("lane calls = %d, want 2", lane.n())
	}
}

// A retry fails closed: if the chain cannot be read, nothing is submitted and the caller
// gets an error, not a state label that looks final.
func TestAcceptRetry_UnreadableChainFailsClosed(t *testing.T) {
	rpcDown := func(context.Context, string) (bool, error) { return false, errors.New("rpc down") }
	cases := []struct {
		name             string
		landed, anchored func(context.Context, string) (bool, error)
	}{
		{"settlement read fails", rpcDown, func(context.Context, string) (bool, error) { return true, nil }},
		{"delivery read fails", func(context.Context, string) (bool, error) { return false, nil }, rpcDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, lane, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
			ctx := context.Background()
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			b.SetSettlementOracle(tc.landed)
			b.SetDeliveryOracle(tc.anchored)
			if s, err := b.AcceptDelivery(ctx, jobID); err == nil {
				t.Fatalf("retry over an unreadable chain returned %q with no error", s)
			}
			if lane.n() != 1 {
				t.Fatalf("lane calls = %d, want 1 (no resubmission)", lane.n())
			}
		})
	}
}

// A retry whose chain read shows the customer already settled records the settlement and
// never resubmits.
func TestAcceptRetry_SettledOnChainIsObservedNotResubmitted(t *testing.T) {
	b, lane, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	b.SetSettlementOracle(func(context.Context, string) (bool, error) { return true, nil })
	b.SetDeliveryOracle(func(context.Context, string) (bool, error) {
		t.Error("delivery read after the chain already reported settlement")
		return false, nil
	})
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
		t.Fatalf("retry: state=%q err=%v", s, err)
	}
	if lane.n() != 1 {
		t.Fatalf("lane calls = %d, want 1", lane.n())
	}
}

// A pending-chain stop is not final. Accept before the job had a vault id, or before a
// customer lane was wired, records settlement.pending_chain once; repeats while nothing has
// changed add no record and submit nothing. Once the vault id and the lane exist, a later
// Accept takes the same gated retry as after a revert: nothing is sent until the job is
// Delivered on chain, and then it settles.
func TestAcceptRetry_PendingChainRetriesOnceTheChainCanTakeIt(t *testing.T) {
	for _, missing := range []string{"vault id", "lane"} {
		t.Run("no "+missing, func(t *testing.T) {
			b, _, jobID := acceptRig(t)
			lane := &scriptedLane{t: t, outs: []funding.ChainOutcome{settledOut}, errs: []error{nil}}
			vault := "0x" + repeat64("b")
			if missing == "vault id" {
				b.funding.SetChain(nil, lane)
			} else if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = vault }); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
					t.Fatalf("accept #%d: state=%q err=%v", i+1, s, err)
				}
			}
			if n := countEvents(t, b.store, "settlement.pending_chain", jobID); n != 1 {
				t.Fatalf("settlement.pending_chain = %d, want 1", n)
			}
			if lane.n() != 0 {
				t.Fatalf("lane calls = %d, want 0", lane.n())
			}

			// The missing piece arrives; the job is not yet Delivered on chain.
			if missing == "vault id" {
				if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = vault }); err != nil {
					t.Fatal(err)
				}
			} else {
				b.funding.SetChain(nil, lane)
			}
			var delivered atomic.Bool
			b.SetSettlementOracle(func(context.Context, string) (bool, error) { return false, nil })
			b.SetDeliveryOracle(func(context.Context, string) (bool, error) { return delivered.Load(), nil })
			if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" {
				t.Fatalf("accept before delivery: state=%q err=%v", s, err)
			}
			if lane.n() != 0 || countEvents(t, b.store, "settlement.pending_chain", jobID) != 1 {
				t.Fatalf("accept before delivery submitted or re-recorded: lane calls=%d", lane.n())
			}

			delivered.Store(true)
			if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
				t.Fatalf("accept once delivered: state=%q err=%v, want accepted-settled", s, err)
			}
			if lane.n() != 1 || countEvents(t, b.store, "settlement.executed", jobID) != 1 {
				t.Fatalf("lane calls=%d executed=%d, want 1 and 1", lane.n(),
					countEvents(t, b.store, "settlement.executed", jobID))
			}
			if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-pending-chain" || lane.n() != 1 {
				t.Fatalf("post-settlement accept: state=%q err=%v lane calls=%d", s, err, lane.n())
			}
		})
	}
}

// An unreadable settlement record fails closed: no submission, an error to the caller.
func TestAcceptRetry_UnreadableRecordFailsClosed(t *testing.T) {
	b, lane, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
	ctx := context.Background()
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if s, err := b.AcceptDelivery(cctx, jobID); err == nil {
		t.Fatalf("retry with an unreadable record returned %q with no error", s)
	}
	if lane.n() != 1 {
		t.Fatalf("lane calls = %d, want 1", lane.n())
	}
}

// The in-flight marker is process memory and is not persisted. After a restart the job is
// recovered as accepted from its memory file, nothing is in flight, and the retry runs.
func TestAcceptRetry_SurvivesRestart(t *testing.T) {
	b, lane, jobID := retryRig(t, []funding.ChainOutcome{revertedOut}, []error{nil})
	ctx := context.Background()
	if s, err := b.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settlement-reverted" {
		t.Fatalf("first accept: state=%q err=%v", s, err)
	}

	fund := funding.New()
	b2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), b.store, b.memory, fund)
	if err := b2.Recover(); err != nil {
		t.Fatal(err)
	}
	if js, ok := b2.Job(jobID); !ok || js.Stage != StageAccepted {
		t.Fatalf("recovered stage %v ok=%v, want accepted", js.Stage, ok)
	}
	lane2 := &scriptedLane{t: t, outs: []funding.ChainOutcome{settledOut}, errs: []error{nil}}
	fund.SetChain(nil, lane2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s, err := b2.AcceptDelivery(ctx, jobID); err != nil || s != "accepted-settled" {
			t.Errorf("retry after restart: state=%q err=%v", s, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry after restart hung")
	}
	if lane.n() != 1 || lane2.n() != 1 {
		t.Fatalf("lane calls before=%d after=%d, want 1 and 1", lane.n(), lane2.n())
	}
}
