package brain

import (
	"context"
	"fmt"

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
)

// The advance triggers — Brain's half of the snap. The advance itself is
// human-authorized through the approval lifecycle (internal/advancing); Brain only
// decides WHEN to propose: on the owner's explicit request (the exercisable path
// today), or when funding is observed on chain (written and seeded-row tested, but it
// has NEVER fired for real — no deployment, no JobFunded rows, no vault ids).

// SetAdvanceFlow hands Brain the advance flow — held by Brain alone, invoked from the
// single ProposeAdvance site (scan-pinned).
func (b *Brain) SetAdvanceFlow(f *advancing.Flow) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advanceFlow = f
}

// ProposeAdvance opens the human-authorized advance request for a job — the SOLE
// advancing invocation site in the daemon. The owner's approval in the H2 inbox is the
// 0:30 beat; Brain never approves what it proposes.
func (b *Brain) ProposeAdvance(ctx context.Context, jobID string) (approval.Request, error) {
	b.mu.Lock()
	flow := b.advanceFlow
	b.mu.Unlock()
	if flow == nil {
		return approval.Request{}, fmt.Errorf("advance flow is not wired")
	}
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return approval.Request{}, err
	}
	if jm.Scope == "" && jm.Stage == "" {
		return approval.Request{}, billing.ErrUnknownJob
	}
	if jm.QuoteUSDC == "" {
		return approval.Request{}, fmt.Errorf("job %s has no quote to advance against", jobID)
	}
	return flow.Propose(ctx, jobID, jm.VaultJobID, jm.QuoteUSDC)
}

// ObserveFundingOnce is the automatic trigger: a JobFunded row in the shared store for
// a tracked job's vault id proposes the advance — ONCE per job, ever (a rejected
// advance is the owner's answer; only the owner re-proposes).
//
// HONESTY: this path has never run against a real chain — no deployment exists, so no
// JobFunded row has ever been produced, and nothing writes vault ids (the chain gap).
// It is written and tested against seeded rows so funding day needs no daemon change.
func (b *Brain) ObserveFundingOnce(ctx context.Context) (int, error) {
	b.mu.Lock()
	flow, agent := b.advanceFlow, b.billingAgent
	b.mu.Unlock()
	if flow == nil || agent == nil {
		return 0, nil // not wired for chain observation in this build
	}
	ids, err := b.memory.List()
	if err != nil {
		return 0, err
	}
	proposed := 0
	for _, jobID := range ids {
		jm, err := b.memory.Get(jobID)
		if err != nil || jm.VaultJobID == "" {
			continue
		}
		var funded int
		if err := b.store.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM chain_events WHERE chain_id=? AND kind='JobFunded' AND entity_id=?`,
			agent.ChainID(), jm.VaultJobID).Scan(&funded); err != nil {
			return proposed, err
		}
		if funded == 0 {
			continue
		}
		// Once per job: any prior advance request (any outcome) means Brain stays quiet.
		var prior int
		if err := b.store.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM events WHERE kind='approval.requested' AND entity_id=?
			 AND payload_json LIKE '%"Kind":"advance"%'`, jobID).Scan(&prior); err != nil {
			return proposed, err
		}
		if prior > 0 {
			continue
		}
		if _, err := b.ProposeAdvance(ctx, jobID); err != nil {
			return proposed, err
		}
		proposed++
	}
	return proposed, nil
}

// BindVaultJob records the job's on-chain identity (the bytes32 JobVault job id) in
// memory — the owner-side producer for vault_job_id, used by the advance observer,
// the settlement path, Billing's join, and Anandan's reconciler (via the jobs
// projection, which fires automatically on this write).
func (b *Brain) BindVaultJob(ctx context.Context, jobID, vaultJobID string) error {
	if len(vaultJobID) != 66 || vaultJobID[:2] != "0x" {
		return fmt.Errorf("vault job id must be 0x-prefixed bytes32 hex, got %q", vaultJobID)
	}
	_ = ctx
	return b.memory.Update(jobID, func(jm *JobMemory) { jm.VaultJobID = vaultJobID })
}
