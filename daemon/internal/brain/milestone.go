package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// Milestone is one cycle under a standing pipeline instruction. Number is one-based;
// the stable instruction identity plus this number defines the cycle's identities.
type Milestone struct {
	StandingInstructionID string
	Number                uint64
	Repository            string
	QuoteUSDC             string
}

// MilestoneCycle is the identity pair callers use for the normal local and chain flows.
// Opening only scopes and binds the job: owner confirmation, funding, advance approval,
// and customer acceptance remain the existing independent gates.
type MilestoneCycle struct {
	JobID      string
	VaultJobID string
}

// MilestoneOracle is the authoritative post-settlement seam. chain.Oracle implements
// it with FloatPool and JobVault views; tests provide an in-memory chain adapter.
type MilestoneOracle interface {
	AdvanceLanded(ctx context.Context, vaultJobID string) (bool, error)
	SettlementLanded(ctx context.Context, vaultJobID string) (bool, error)
	AdvanceRateBps(ctx context.Context) (uint64, error)
}

// SetMilestoneOracle wires authoritative cycle verification. With no oracle, settlement
// remains valid but the milestone observation is recorded as pending rather than guessed.
func (b *Brain) SetMilestoneOracle(oracle MilestoneOracle) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.milestoneOracle = oracle
}

// OpenMilestone scopes one fresh Build-Monitor job and binds its unique bytes32 chain
// identity. It never confirms, funds, advances, settles, or releases the job.
func (b *Brain) OpenMilestone(ctx context.Context, milestone Milestone) (MilestoneCycle, error) {
	instruction := strings.TrimSpace(milestone.StandingInstructionID)
	repository := strings.TrimSpace(milestone.Repository)
	quote := strings.TrimSpace(milestone.QuoteUSDC)
	switch {
	case instruction == "":
		return MilestoneCycle{}, fmt.Errorf("standing instruction id is required")
	case milestone.Number == 0:
		return MilestoneCycle{}, fmt.Errorf("milestone number must be one-based")
	case repository == "":
		return MilestoneCycle{}, fmt.Errorf("milestone repository is required")
	case quote == "":
		return MilestoneCycle{}, fmt.Errorf("milestone quote is required")
	}
	identity := instruction + "\x00" + strconv.FormatUint(milestone.Number, 10)
	localHash := sha256.Sum256([]byte("snapfall:local-milestone:v1\x00" + identity))
	vaultHash := sha256.Sum256([]byte("snapfall:vault-milestone:v1\x00" + identity))
	cycle := MilestoneCycle{
		JobID:      fmt.Sprintf("milestone_%s_%d", hex.EncodeToString(localHash[:16]), milestone.Number),
		VaultJobID: "0x" + hex.EncodeToString(vaultHash[:]),
	}
	if err := b.frozenErr(cycle.JobID, worker.BuildMonitorKind); err != nil {
		return MilestoneCycle{}, err
	}

	existing, err := b.memory.Get(cycle.JobID)
	if err != nil {
		return MilestoneCycle{}, err
	}
	if existing.Scope != "" || existing.Stage != "" || existing.VaultJobID != "" {
		return MilestoneCycle{}, fmt.Errorf("milestone %s already exists", cycle.JobID)
	}

	state := &jobState{
		JobID: cycle.JobID, Scope: repository, QuoteUSDC: quote,
		Stage: StageScoped, Worker: worker.BuildMonitorKind,
	}
	b.mu.Lock()
	if _, duplicate := b.jobs[cycle.JobID]; duplicate {
		b.mu.Unlock()
		return MilestoneCycle{}, fmt.Errorf("milestone %s already exists", cycle.JobID)
	}
	// Reserve the deterministic identity before I/O so concurrent opens cannot both
	// reach the event log. Roll back this exact reservation on a failed durable write.
	b.jobs[cycle.JobID] = state
	b.mu.Unlock()
	rollback := func() {
		b.mu.Lock()
		if b.jobs[cycle.JobID] == state {
			delete(b.jobs, cycle.JobID)
		}
		b.mu.Unlock()
	}

	if _, err := b.store.Append(ctx, store.Event{
		Kind:     "pipeline.milestone.opened",
		EntityID: cycle.JobID,
		Actor:    "brain",
		Payload: map[string]any{
			"standing_instruction_id": instruction,
			"milestone_number":        milestone.Number,
			"repository":              repository,
			"quote_usdc":              quote,
			"vault_job_id":            cycle.VaultJobID,
		},
	}); err != nil {
		rollback()
		return MilestoneCycle{}, err
	}
	if err := b.memory.Update(cycle.JobID, func(jm *JobMemory) {
		jm.Scope = repository
		jm.QuoteUSDC = quote
		jm.Stage = string(StageScoped)
		jm.EscrowState = "none"
		jm.VaultJobID = cycle.VaultJobID
		jm.AssignedWorker = worker.BuildMonitorKind
		jm.StandingInstructionID = instruction
		jm.MilestoneNumber = milestone.Number
	}); err != nil {
		rollback()
		return MilestoneCycle{}, err
	}

	return cycle, nil
}

// observeMilestoneCompletion verifies the completed cycle and records the rate produced
// by that settlement. It is idempotent because customer acceptance is idempotent and
// this event is checked before append.
func (b *Brain) observeMilestoneCompletion(ctx context.Context, jobID string) error {
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return err
	}
	if jm.StandingInstructionID == "" {
		return nil
	}
	var prior int
	if err := b.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind='pipeline.milestone.completed' AND entity_id=?`,
		jobID).Scan(&prior); err != nil {
		return err
	}
	if prior > 0 {
		return nil
	}

	b.mu.Lock()
	oracle := b.milestoneOracle
	b.mu.Unlock()
	if oracle == nil {
		_, err := b.store.Append(ctx, store.Event{
			Kind: "pipeline.milestone.observation_pending", EntityID: jobID, Actor: "brain",
			Payload: map[string]any{"vault_job_id": jm.VaultJobID, "note": "settled locally; no chain oracle wired"},
		})
		return err
	}
	advanced, err := oracle.AdvanceLanded(ctx, jm.VaultJobID)
	if err != nil {
		return fmt.Errorf("verify milestone advance: %w", err)
	}
	settled, err := oracle.SettlementLanded(ctx, jm.VaultJobID)
	if err != nil {
		return fmt.Errorf("verify milestone settlement: %w", err)
	}
	if !advanced || !settled {
		return fmt.Errorf("milestone chain state incomplete: advanced=%t settled=%t", advanced, settled)
	}
	rateBps, err := oracle.AdvanceRateBps(ctx)
	if err != nil {
		return fmt.Errorf("read milestone advance rate: %w", err)
	}
	_, err = b.store.Append(ctx, store.Event{
		Kind: "pipeline.milestone.completed", EntityID: jobID, Actor: "brain",
		Payload: map[string]any{
			"standing_instruction_id": jm.StandingInstructionID,
			"milestone_number":        jm.MilestoneNumber,
			"vault_job_id":            jm.VaultJobID,
			"advance_rate_bps":        rateBps,
		},
	})
	return err
}
