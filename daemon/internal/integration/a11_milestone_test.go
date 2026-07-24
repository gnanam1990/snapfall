package integration

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

type completedBuild struct{}

func (completedBuild) Snapshot(context.Context, string) (worker.BuildSnapshot, error) {
	return worker.BuildSnapshot{
		CompletionPct: 100,
		Revision:      "0123456789abcdef0123456789abcdef01234567",
		Completed:     []string{"contract", "integration", "release"},
	}, nil
}

type milestoneRail struct {
	mu       sync.Mutex
	advanced map[string]bool
	settled  map[string]bool
	accepted uint64
	advance  chan string
}

type milestoneLane struct {
	rail *milestoneRail
	kind string
}

func (l milestoneLane) Submit(_ context.Context, calldata []byte) (funding.ChainOutcome, error) {
	if len(calldata) != 36 {
		return funding.ChainOutcome{}, fmt.Errorf("%s calldata length %d", l.kind, len(calldata))
	}
	id := "0x" + hex.EncodeToString(calldata[4:36])
	l.rail.mu.Lock()
	defer l.rail.mu.Unlock()
	switch l.kind {
	case "advance":
		if l.rail.advanced[id] {
			return funding.ChainOutcome{Submitted: true, TxHash: "0xduplicate", Reverted: true}, nil
		}
		l.rail.advanced[id] = true
		l.rail.advance <- id
	case "settle":
		if !l.rail.advanced[id] {
			return funding.ChainOutcome{}, fmt.Errorf("settlement before advance for %s", id)
		}
		if l.rail.settled[id] {
			return funding.ChainOutcome{Submitted: true, TxHash: "0xduplicate", Reverted: true}, nil
		}
		l.rail.settled[id] = true
		l.rail.accepted++
	default:
		return funding.ChainOutcome{}, fmt.Errorf("unknown lane %q", l.kind)
	}
	return funding.ChainOutcome{
		Submitted: true,
		TxHash:    fmt.Sprintf("0x%s-%d", l.kind, l.rail.accepted),
		Block:     100 + l.rail.accepted,
	}, nil
}

func (r *milestoneRail) AdvanceLanded(_ context.Context, vaultJobID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.advanced[vaultJobID], nil
}

func (r *milestoneRail) SettlementLanded(_ context.Context, vaultJobID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settled[vaultJobID], nil
}

func (r *milestoneRail) AdvanceRateBps(context.Context) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return 5_000 + r.accepted*500, nil
}

func TestA11_AT17SecondMilestoneCyclesFreshAdvanceSettlementAndRate(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "a11.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mem, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	fund := funding.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := brain.New(log, st, mem, fund)
	if err := b.RegisterWorker(worker.NewBuildMonitor(completedBuild{})); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterQAWorker(qa.Worker{}); err != nil {
		t.Fatal(err)
	}

	rail := &milestoneRail{
		advanced: make(map[string]bool),
		settled:  make(map[string]bool),
		advance:  make(chan string, 2),
	}
	fund.SetChain(milestoneLane{rail, "advance"}, milestoneLane{rail, "settle"})
	b.SetMilestoneOracle(rail)

	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	b.SetAdvanceFlow(advancing.New(life, st, fund, log, "org_demo", time.Minute))

	var cycles []brain.MilestoneCycle
	for number, quote := range []string{"25.00", "27.50"} {
		cycle, err := b.OpenMilestone(ctx, brain.Milestone{
			StandingInstructionID: "acme-standing-build",
			Number:                uint64(number + 1),
			Repository:            "/work/acme",
			QuoteUSDC:             quote,
		})
		if err != nil {
			t.Fatal(err)
		}
		cycles = append(cycles, cycle)
		if err := b.Confirm(ctx, cycle.JobID, "anandan"); err != nil {
			t.Fatal(err)
		}
		if err := b.AwaitTask(cycle.JobID); err != nil {
			t.Fatal(err)
		}
		jm, err := mem.Get(cycle.JobID)
		if err != nil || jm.Stage != string(brain.StageDeliveryReady) || jm.CompletionPct != 100 {
			t.Fatalf("monitored milestone not delivery-ready: %+v err=%v", jm, err)
		}

		req, err := b.ProposeAdvance(ctx, cycle.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "anandan", "milestone funded"); err != nil {
			t.Fatal(err)
		}
		select {
		case advancedID := <-rail.advance:
			if advancedID != cycle.VaultJobID {
				t.Fatalf("advanced %s, want fresh %s", advancedID, cycle.VaultJobID)
			}
		case <-time.After(time.Second):
			t.Fatal("approved milestone advance did not reach the chain seam")
		}

		state, err := b.AcceptDelivery(ctx, cycle.JobID)
		if err != nil || state != "accepted-settled" {
			t.Fatalf("settlement state=%q err=%v", state, err)
		}
	}

	if cycles[0].JobID == cycles[1].JobID || cycles[0].VaultJobID == cycles[1].VaultJobID {
		t.Fatalf("milestone 2 reused milestone 1 identity: %+v", cycles)
	}
	if rate, _ := rail.AdvanceRateBps(ctx); rate != 6_000 {
		t.Fatalf("rate after two accepted milestones = %d, want 6000", rate)
	}
	var completed int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind='pipeline.milestone.completed'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 2 {
		t.Fatalf("completed milestone observations = %d, want 2", completed)
	}
	for _, cycle := range cycles {
		var progressSeq, settlementSeq int64
		if err := st.DB().QueryRowContext(ctx,
			`SELECT MIN(seq) FROM events WHERE entity_id=? AND kind='brain.msg.worker.progress'`,
			cycle.JobID).Scan(&progressSeq); err != nil {
			t.Fatal(err)
		}
		if err := st.DB().QueryRowContext(ctx,
			`SELECT MIN(seq) FROM events WHERE entity_id=? AND kind='settlement.executed'`,
			cycle.JobID).Scan(&settlementSeq); err != nil {
			t.Fatal(err)
		}
		if progressSeq >= settlementSeq {
			t.Fatalf("completion was not reported before release for %s: progress=%d settlement=%d",
				cycle.JobID, progressSeq, settlementSeq)
		}
	}
}
