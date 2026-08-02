package ownerapi

import (
	"context"
	"database/sql"
)

// H2 §2.1's `aggregates`, and the Snapshot fields that share its shape.
//
// The field is marked OPTIONAL in the contract -- "present only when computable from the shared
// store's chain projections" -- and the daemon took that as permission to never send it. It was
// consumed by five dashboard files and produced by none: `grep aggregates daemon/` returned
// nothing. The visible effect is that the treasury hero and the V11 score ring render "-" against
// a real daemon while looking correct against the V5 mock, which always sends them.
//
// OPTIONAL means "absent when the indexer has not run", not "absent always". Everything below is
// derived from the projections the indexer already maintains (chain_job_financials,
// chain_org_rates); nothing here reads a chain node.

// aggregates is the H2 §2.1 optional block. Every money field is a base-10 atomic-USDC STRING,
// per the H1 §2 rule that amounts never cross the wire as numbers -- a float would round a
// 6-decimal balance and the dashboard would render a number nobody can reconcile.
// Treasury is intentionally not a field here. The operator balance needs a chain read (6dp
// ERC-20 balanceOf); the dashboard reads it from /api/float so it is one verifiable source and
// works on the daemon-less public deploy. This block carries only what the indexer projects.
type aggregates struct {
	Pool             *poolAgg      `json:"pool,omitempty"`
	OpenAdvances     []openAdvance `json:"openAdvances"`
	ActiveJobs       []activeJob   `json:"activeJobs"`
	PendingApprovals int           `json:"pendingApprovals"`
}

type poolAgg struct {
	OrgRateBps int64 `json:"orgRateBps"`
}

// openAdvance matches the dashboard's OpenAdvance shape (lib/types.ts). The money fields carry
// atomic-USDC strings; the field names use the dashboard's `...Usdc` spelling so the wire keys
// match what it reads (the earlier `principalAtomic`/`feeAtomic` keys never matched and rendered
// blank). rateBps is intentionally absent: chain_job_financials records no per-advance draw rate,
// and the current org rate is not the rate this advance was drawn at, so emitting it would be a
// wrong number. The dashboard renders an absent rateBps as a dash.
type openAdvance struct {
	JobID         string `json:"jobId"`
	Org           string `json:"org"`
	PrincipalUsdc string `json:"principalUsdc"`
	FeeUsdc       string `json:"feeUsdc"`
	Status        string `json:"status"`
}

// activeJob carries only what the chain projection can source: the job id and its price (the
// funded escrow amount). The dashboard's JobSummary also shows customer, title and lifecycle
// state — none of which live in chain_job_financials. customer and state are in the daemon's own
// `jobs` table (customer_ref/status); title lives only in Brain's per-job memory. Sourcing those
// would mean reading a different, daemon-asserted projection, which is a deliberate decision, not
// a widening — so they are omitted here and the dashboard renders their absence as a dash rather
// than a fabricated value.
type activeJob struct {
	JobID     string `json:"jobId"`
	PriceUsdc string `json:"priceUsdc"`
}

// computeAggregates reads the indexer's projections. A missing table or an empty projection is
// not an error: it is the documented "before the indexer has run" case, and the contract says the
// dashboard keeps its last known values when the block is absent. So this returns a nil pointer
// rather than a zeroed struct, and the caller omits the block entirely -- sending an org rate of
// 0 against an un-indexed store would be a claim the daemon cannot support.
func computeAggregates(ctx context.Context, db *sql.DB, orgAddress string, pendingApprovals int) *aggregates {
	agg := &aggregates{
		OpenAdvances:     []openAdvance{},
		ActiveJobs:       []activeJob{},
		PendingApprovals: pendingApprovals,
	}
	any := false

	if orgAddress != "" {
		var rateBps int64
		// COLLATE NOCASE: addresses are written by whatever cased them, and an org whose rate
		// silently failed to match because of checksum casing would leave the score ring at
		// "-" with every table populated -- the hardest kind of empty to debug.
		err := db.QueryRowContext(ctx,
			`SELECT rate_bps FROM chain_org_rates WHERE org_address = ? COLLATE NOCASE
			 ORDER BY last_block_number DESC, last_log_index DESC LIMIT 1`,
			orgAddress).Scan(&rateBps)
		if err == nil {
			agg.Pool = &poolAgg{OrgRateBps: rateBps}
			any = true
		}
	}

	rows, err := db.QueryContext(ctx,
		`SELECT job_id,
		        COALESCE(funded_amount_atomic,''), COALESCE(expense_total_atomic,'0'),
		        COALESCE(advance_principal_atomic,''), COALESCE(advance_fee_atomic,''),
		        COALESCE(advance_status,'')
		   FROM chain_job_financials
		  ORDER BY last_block_number DESC, last_log_index DESC
		  LIMIT 256`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var jobID, funded, expense, principal, fee, status string
			if err := rows.Scan(&jobID, &funded, &expense, &principal, &fee, &status); err != nil {
				break
			}
			any = true
			// "Open" is the contract's word for an advance that is drawn and not yet repaid.
			// Issued is the only status FloatPool leaves in that state (Repaid and WrittenOff
			// are both terminal), so anything else must not be presented as outstanding money.
			if status == "Issued" && principal != "" {
				agg.OpenAdvances = append(agg.OpenAdvances, openAdvance{
					JobID: jobID, Org: orgAddress, PrincipalUsdc: principal, FeeUsdc: fee, Status: status,
				})
			}
			if funded != "" {
				// funded escrow == the job's price (SC-JV-001: the customer funds the quoted amount).
				agg.ActiveJobs = append(agg.ActiveJobs, activeJob{JobID: jobID, PriceUsdc: funded})
			}
		}
	}

	if !any {
		return nil
	}
	return agg
}
