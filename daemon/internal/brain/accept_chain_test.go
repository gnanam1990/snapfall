package brain

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
}

func repeat64(s string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += s
	}
	return out
}
