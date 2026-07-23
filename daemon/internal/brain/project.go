package brain

import (
	"context"
	"fmt"
	"time"
)

// The jobs-table projection: the independent half of the chain gap's fourth face.
//
// Anandan's A4 reconciler joins chain_job_financials against the SQL `jobs` table;
// until now nothing wrote that table, so every comparison had a null local side. Brain
// now projects each job into `jobs` — id, org, stage as status, the quote as the
// human-facing USDC string his usdcAtomic parses, vault_job_id when memory has one
// (NULL until on-chain job creation exists), created_at. customer_ref stays NULL:
// Brain has a SCOPE label, not a customer reference, and occupying a column that
// means something else would be misread by its next consumer — if the label should
// land in SQL, that is a scope column to ask Anandan for, not a column to repurpose.
//
// AUTHORITY, decided: the file-based JobMemory is the single source of truth. This
// projection is derived, for reconciliation — never a second writable store:
//   - it is written from the just-updated JobMemory VALUE only (the MemoryStore
//     AfterUpdate hook — every write projects, no call site can forget);
//   - it is WRITE-ONLY from the daemon's side (the scan test pins zero reads of the
//     jobs table in this package, and exactly one write site);
//   - on any disagreement, memory wins: Recover reprojects every job from the memory
//     files, so a tampered or drifted row heals at the next startup.
//
// Advance columns (advance_principal_usdc, advance_fee_usdc, advance_status) are NOT
// written here — the daemon has no advance path yet; they stay NULL and reconcile as
// "<missing>" against any chain advance, which is the honest state until the
// chain-write path lands.

// projectJob is the AfterUpdate hook target: derive the SQL row from the new memory
// value. A projection failure must not fail the memory write (memory is the truth;
// the projection heals at Recover) — it is logged, loudly.
func (b *Brain) projectJob(jm JobMemory) {
	if err := b.projectJobRow(context.Background(), jm); err != nil {
		b.log.Warn("jobs projection failed; will heal at next Recover", "job", jm.JobID, "err", err)
	}
}

func (b *Brain) projectJobRow(ctx context.Context, jm JobMemory) error {
	b.mu.Lock()
	org := b.orgID
	b.mu.Unlock()
	if org == "" {
		org = "org_unconfigured"
	}
	db := b.store.DB()
	now := time.Now().UnixMilli()
	// The jobs FK requires the organizations row; nothing else writes it yet.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO organizations (id, owner, name, created_at)
		VALUES (?, 'owner', ?, ?) ON CONFLICT(id) DO NOTHING`, org, org, now); err != nil {
		return fmt.Errorf("seeding organization %s: %w", org, err)
	}
	var vault any // NULL until on-chain job creation fills memory's VaultJobID
	if jm.VaultJobID != "" {
		vault = jm.VaultJobID
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO jobs (id, org_id, status, quote_usdc, vault_job_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  org_id = excluded.org_id,
		  status = excluded.status, quote_usdc = excluded.quote_usdc,
		  vault_job_id = excluded.vault_job_id`,
		jm.JobID, org, jm.Stage, jm.QuoteUSDC, vault, now)
	return err
}

// ReprojectJobs rewrites the projection for every job from the memory files — the
// memory-wins rule made operational. Recover calls it at startup; tests call it to
// prove a tampered row heals.
func (b *Brain) ReprojectJobs(ctx context.Context) error {
	ids, err := b.memory.List()
	if err != nil {
		return err
	}
	for _, id := range ids {
		jm, err := b.memory.Get(id)
		if err != nil {
			return err
		}
		if err := b.projectJobRow(ctx, jm); err != nil {
			return fmt.Errorf("reprojecting %s: %w", id, err)
		}
	}
	return nil
}
