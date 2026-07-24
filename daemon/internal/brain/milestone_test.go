package brain

import (
	"context"
	"testing"
)

func TestOpenMilestoneCreatesFreshLocalAndVaultJobs(t *testing.T) {
	b, _, _ := newTestBrain(t)
	ctx := context.Background()
	instruction := "acme-standing-build"

	first, err := b.OpenMilestone(ctx, Milestone{
		StandingInstructionID: instruction,
		Number:                1,
		Repository:            "/work/acme",
		QuoteUSDC:             "25.00",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.OpenMilestone(ctx, Milestone{
		StandingInstructionID: instruction,
		Number:                2,
		Repository:            "/work/acme",
		QuoteUSDC:             "27.50",
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.JobID == second.JobID {
		t.Fatalf("milestones reused local job %s", first.JobID)
	}
	if first.VaultJobID == second.VaultJobID {
		t.Fatalf("milestones reused vault job %s", first.VaultJobID)
	}
	for _, cycle := range []MilestoneCycle{first, second} {
		if len(cycle.VaultJobID) != 66 || cycle.VaultJobID[:2] != "0x" {
			t.Fatalf("vault job id %q is not bytes32 hex", cycle.VaultJobID)
		}
		jm, err := b.memory.Get(cycle.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if jm.VaultJobID != cycle.VaultJobID || jm.Stage != string(StageScoped) ||
			jm.AssignedWorker != "build-monitor" {
			t.Fatalf("memory for %s = %+v", cycle.JobID, jm)
		}
	}

	if _, err := b.OpenMilestone(ctx, Milestone{
		StandingInstructionID: instruction,
		Number:                2,
		Repository:            "/work/acme",
		QuoteUSDC:             "27.50",
	}); err == nil {
		t.Fatal("opening the same milestone twice must be refused")
	}
}
