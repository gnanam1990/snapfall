package brain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

type fakeSettleLane struct{ out funding.ChainOutcome }

func (f fakeSettleLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	return f.out, nil
}

type transientMilestoneOracle struct {
	advanceCalls int
}

func (o *transientMilestoneOracle) AdvanceLanded(context.Context, string) (bool, error) {
	o.advanceCalls++
	if o.advanceCalls == 1 {
		return false, errors.New("temporary RPC failure")
	}
	return true, nil
}

func (*transientMilestoneOracle) SettlementLanded(context.Context, string) (bool, error) {
	return true, nil
}

func (*transientMilestoneOracle) AdvanceRateBps(context.Context) (uint64, error) {
	return 5_500, nil
}

type concurrentMilestoneOracle struct {
	entered chan struct{}
	release chan struct{}
}

func (o *concurrentMilestoneOracle) AdvanceLanded(context.Context, string) (bool, error) {
	o.entered <- struct{}{}
	<-o.release
	return true, nil
}

func (*concurrentMilestoneOracle) SettlementLanded(context.Context, string) (bool, error) {
	return true, nil
}

func (*concurrentMilestoneOracle) AdvanceRateBps(context.Context) (uint64, error) {
	return 5_500, nil
}

// The fall's chain half: an authenticated, claimed Accept settles through Funding's
// customer lane — success is settlement.executed with the tx; a REVERT is
// settlement.reverted and the state says so (never "settled"); a job with no chain
// identity stays honestly pending.
func TestAcceptChain_OutcomesAreDistinct(t *testing.T) {
	cases := []struct {
		name      string
		vault     string
		out       funding.ChainOutcome
		wantState string
		wantEvent string
	}{
		{"settled", "0x" + repeat64("a"), funding.ChainOutcome{Submitted: true, TxHash: "0xs1", GasUsed: 90000}, "accepted-settled", "settlement.executed"},
		{"reverted", "0x" + repeat64("b"), funding.ChainOutcome{Submitted: true, TxHash: "0xr1", Reverted: true}, "accepted-settlement-reverted", "settlement.reverted"},
		{"no chain identity", "", funding.ChainOutcome{}, "accepted-pending-chain", "settlement.pending_chain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, st, jobID := acceptRig(t)
			if tc.vault != "" {
				if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = tc.vault }); err != nil {
					t.Fatal(err)
				}
			}
			b.funding.SetChain(nil, fakeSettleLane{out: tc.out})

			state, err := b.AcceptDelivery(context.Background(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			if state != tc.wantState {
				t.Fatalf("state %q, want %q", state, tc.wantState)
			}
			if n := countEvents(t, st, tc.wantEvent, jobID); n != 1 {
				t.Fatalf("%s = %d, want 1", tc.wantEvent, n)
			}
			if tc.wantEvent != "settlement.executed" {
				if n := countEvents(t, st, "settlement.executed", jobID); n != 0 {
					t.Fatal("a non-success outcome recorded settlement.executed")
				}
			}
		})
	}
}

func TestAcceptChain_RetriesMilestoneObservationWithoutResettling(t *testing.T) {
	b, st, jobID := acceptRig(t)
	const vaultJobID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := b.memory.Update(jobID, func(jm *JobMemory) {
		jm.VaultJobID = vaultJobID
		jm.StandingInstructionID = "standing-build"
		jm.MilestoneNumber = 1
	}); err != nil {
		t.Fatal(err)
	}
	b.funding.SetChain(nil, fakeSettleLane{out: funding.ChainOutcome{
		Submitted: true,
		TxHash:    "0xsettled",
	}})
	oracle := &transientMilestoneOracle{}
	b.SetMilestoneOracle(oracle)

	if state, err := b.AcceptDelivery(context.Background(), jobID); err != nil || state != "accepted-settled" {
		t.Fatalf("first accept state=%q err=%v", state, err)
	}
	if n := countEvents(t, st, "pipeline.milestone.observation_failed", jobID); n != 1 {
		t.Fatalf("failed observations = %d, want 1", n)
	}
	if n := countEvents(t, st, "pipeline.milestone.completed", jobID); n != 0 {
		t.Fatalf("completed observations after transient failure = %d, want 0", n)
	}

	if _, err := b.AcceptDelivery(context.Background(), jobID); err != nil {
		t.Fatalf("idempotent accept did not retry observation: %v", err)
	}
	if n := countEvents(t, st, "pipeline.milestone.completed", jobID); n != 1 {
		t.Fatalf("completed observations after retry = %d, want 1", n)
	}
	if n := countEvents(t, st, "settlement.executed", jobID); n != 1 {
		t.Fatalf("settlement executions = %d, want exactly 1", n)
	}
}

func TestAcceptChain_ConcurrentObservationRetriesCompleteMilestoneOnce(t *testing.T) {
	b, st, jobID := acceptRig(t)
	const vaultJobID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b.mu.Lock()
	b.jobs[jobID].Stage = StageAccepted
	b.mu.Unlock()
	if err := b.memory.Update(jobID, func(jm *JobMemory) {
		jm.Stage = string(StageAccepted)
		jm.VaultJobID = vaultJobID
		jm.StandingInstructionID = "standing-build"
		jm.MilestoneNumber = 1
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(context.Background(), store.Event{
		Kind: "settlement.executed", EntityID: jobID, Actor: "funding",
		Payload: map[string]any{"tx_hash": "0xsettled"},
	}); err != nil {
		t.Fatal(err)
	}
	oracle := &concurrentMilestoneOracle{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	b.SetMilestoneOracle(oracle)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	retry := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.AcceptDelivery(context.Background(), jobID)
			errs <- err
		}()
	}
	retry()
	<-oracle.entered
	retry()
	select {
	case <-oracle.entered:
		// Without serialization both callers pass the prior-count check and reach
		// the oracle before either can append.
	case <-time.After(100 * time.Millisecond):
		// With serialization the second caller waits for the first completion.
	}
	close(oracle.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent observation retry: %v", err)
		}
	}
	if n := countEvents(t, st, "pipeline.milestone.completed", jobID); n != 1 {
		t.Fatalf("completed observations = %d, want exactly 1", n)
	}
	b.mu.Lock()
	retainedGates := len(b.milestoneObservationGates)
	b.mu.Unlock()
	if retainedGates != 0 {
		t.Fatalf("retained milestone observation gates = %d, want 0", retainedGates)
	}
}

func TestMilestoneObservation_CanceledWaiterDoesNotBlockBehindOracle(t *testing.T) {
	b, _, jobID := acceptRig(t)
	if err := b.memory.Update(jobID, func(jm *JobMemory) {
		jm.VaultJobID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		jm.StandingInstructionID = "standing-build"
		jm.MilestoneNumber = 1
	}); err != nil {
		t.Fatal(err)
	}
	oracle := &concurrentMilestoneOracle{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b.SetMilestoneOracle(oracle)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- b.observeMilestoneCompletion(context.Background(), jobID)
	}()
	<-oracle.entered

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(started)
		secondDone <- b.observeMilestoneCompletion(ctx, jobID)
	}()
	<-started
	cancel()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled observation error = %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(oracle.release)
		<-firstDone
		<-secondDone
		t.Fatal("canceled observation remained blocked behind the in-flight oracle")
	}
	close(oracle.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first observation: %v", err)
	}
}

func repeat64(s string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += s
	}
	return out
}

// panicLane fails the test if anything submits through it: a job the customer already
// settled from their own wallet must never get a second acceptDelivery.
type panicLane struct{ t *testing.T }

func (p panicLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	p.t.Fatal("the customer lane was used for a job the chain says is already settled")
	return funding.ChainOutcome{}, nil
}

// Self-custody customer: their wallet signed acceptDelivery, then the portal sends the
// credential-gated Accept. The chain is the oracle, so the settlement is recorded as
// executed by the customer, with the indexed transaction when there is one, and the
// custodial lane is never touched. Without the check the lane would submit, mine, revert,
// and report a failed settlement of a job that settled fine.
func TestAcceptChain_CustomerWalletSettlementIsObservedNotResubmitted(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		b, st, jobID := acceptRig(t)
		vault := "0x" + repeat64("c")
		if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = vault }); err != nil {
			t.Fatal(err)
		}
		b.SetBilling(billing.New(st, g12Chain, nil))
		if indexed {
			seedChainRow(t, st, "JobSettled", vault, "0x"+repeat64("d"), 77,
				map[string]string{"advanceRepaidAtomic": "0", "operatorNetAtomic": "1000000"})
		}
		b.funding.SetChain(nil, panicLane{t})
		var asked []string
		b.SetSettlementOracle(func(_ context.Context, v string) (bool, error) {
			asked = append(asked, v)
			return true, nil
		})

		state, err := b.AcceptDelivery(context.Background(), jobID)
		if err != nil || state != "accepted-settled" {
			t.Fatalf("indexed=%v: state=%q err=%v", indexed, state, err)
		}
		if len(asked) != 1 || asked[0] != vault {
			t.Fatalf("oracle asked %v, want the bound vault id", asked)
		}
		var actor, payload string
		if err := st.DB().QueryRow(`SELECT actor, payload_json FROM events WHERE kind='settlement.executed' AND entity_id=?`,
			jobID).Scan(&actor, &payload); err != nil {
			t.Fatalf("indexed=%v: %v", indexed, err)
		}
		if actor != "customer" {
			t.Fatalf("actor %q, want customer", actor)
		}
		hasHash := strings.Contains(payload, repeat64("d"))
		if hasHash != indexed || strings.Contains(payload, "tx_hash_pending") == indexed {
			t.Fatalf("indexed=%v: payload %s", indexed, payload)
		}
	}
}

// Not settled on chain: the Accept takes the customer lane exactly as before this check
// existed.
func TestAcceptChain_UnsettledChainUsesTheLane(t *testing.T) {
	b, st, jobID := acceptRig(t)
	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("e") }); err != nil {
		t.Fatal(err)
	}
	b.funding.SetChain(nil, fakeSettleLane{out: funding.ChainOutcome{Submitted: true, TxHash: "0xlane"}})
	b.SetSettlementOracle(func(context.Context, string) (bool, error) { return false, nil })
	if state, err := b.AcceptDelivery(context.Background(), jobID); err != nil || state != "accepted-settled" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	var actor string
	if err := st.DB().QueryRow(`SELECT actor FROM events WHERE kind='settlement.executed' AND entity_id=?`,
		jobID).Scan(&actor); err != nil || actor != "funding" {
		t.Fatalf("actor=%q err=%v, want the funding lane's record", actor, err)
	}
}

// countingLane settles and counts its submissions.
type countingLane struct{ n *atomic.Int32 }

func (c countingLane) Submit(context.Context, []byte) (funding.ChainOutcome, error) {
	c.n.Add(1)
	return funding.ChainOutcome{Submitted: true, TxHash: "0xlane"}, nil
}

// An unreadable chain FAILS CLOSED on the first Accept as on a retry: the chain may already
// show the job settled, so nothing is submitted blind. The claim stands, the customer's
// status says retry once the chain reads again, and that Accept settles.
func TestAcceptChain_UnreadableChainFailsClosedThenRetries(t *testing.T) {
	b, st, jobID := acceptRig(t)
	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = "0x" + repeat64("e") }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	b.funding.SetChain(nil, countingLane{&calls})
	var down atomic.Bool
	down.Store(true)
	b.SetSettlementOracle(func(context.Context, string) (bool, error) {
		if down.Load() {
			return false, errors.New("rpc down")
		}
		return false, nil
	})
	ctx := context.Background()
	if state, err := b.AcceptDelivery(ctx, jobID); err == nil {
		t.Fatalf("accept over an unreadable chain returned %q with no error", state)
	}
	if calls.Load() != 0 {
		t.Fatalf("lane submissions = %d, want 0: nothing is submitted blind", calls.Load())
	}
	if js, _ := b.Job(jobID); js.Stage != StageAccepted {
		t.Fatalf("stage %s, want the claim to stand", js.Stage)
	}
	if _, err := b.SettlementStatus(ctx, jobID); err == nil {
		t.Fatal("status over an unreadable chain returned no error")
	}
	down.Store(false)
	if s, err := b.SettlementStatus(ctx, jobID); err != nil || s != SettlementRetry {
		t.Fatalf("status = %q err=%v, want retry", s, err)
	}
	if state, err := b.AcceptDelivery(ctx, jobID); err != nil || state != "accepted-settled" {
		t.Fatalf("retry: state=%q err=%v", state, err)
	}
	if calls.Load() != 1 || countEvents(t, st, "settlement.executed", jobID) != 1 {
		t.Fatalf("lane calls=%d, want 1 and one settlement.executed", calls.Load())
	}
}
