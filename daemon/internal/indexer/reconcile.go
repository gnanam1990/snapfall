package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Alert is one local-ledger/chain disagreement. Values are normalized atomic USDC strings or
// canonical status labels, so the dashboard can explain the mismatch without reimplementing
// accounting rules.
type Alert struct {
	JobID      string
	Field      string
	Local      string
	Chain      string
	DetectedAt time.Time
}

// Reconciliation is the dashboard-ready result of one full comparison.
type Reconciliation struct {
	HasMismatch bool
	Alerts      []Alert
}

// Reconciler compares the existing jobs ledger to deterministic H1 projections.
type Reconciler struct {
	store   *store.Store
	chainID uint64
}

// NewReconciler creates the A4 reconciliation module.
func NewReconciler(st *store.Store, chainID uint64) (*Reconciler, error) {
	if st == nil || chainID == 0 {
		return nil, fmt.Errorf("reconciler store and chain ID are required")
	}
	return &Reconciler{store: st, chainID: chainID}, nil
}

type ledgerPair struct {
	jobID  string
	field  string
	local  sql.NullString
	chain  sql.NullString
	status bool
}

// Run compares every local job bound to a vault job ID, persists structured alerts, resolves
// alerts whose values now agree and returns the current dashboard flag.
func (r *Reconciler) Run(ctx context.Context) (Reconciliation, error) {
	rows, err := r.store.DB().QueryContext(ctx, `
		SELECT c.job_id, j.id,
		       j.quote_usdc, c.funded_amount_atomic,
		       j.advance_principal_usdc, c.advance_principal_atomic,
		       j.advance_fee_usdc, c.advance_fee_atomic,
		       j.advance_status, c.advance_status
		FROM chain_job_financials c
		LEFT JOIN jobs j ON j.vault_job_id = c.job_id
		WHERE c.chain_id = ?
		ORDER BY c.job_id`, r.chainID)
	if err != nil {
		return Reconciliation{}, err
	}
	defer rows.Close()

	var pairs []ledgerPair
	for rows.Next() {
		var jobID string
		var localJobID sql.NullString
		var quote, funded, principal, chainPrincipal, fee, chainFee, status, chainStatus sql.NullString
		if err := rows.Scan(&jobID, &localJobID, &quote, &funded, &principal, &chainPrincipal, &fee, &chainFee, &status, &chainStatus); err != nil {
			return Reconciliation{}, err
		}
		if !localJobID.Valid {
			pairs = append(pairs, ledgerPair{jobID, "local_job", sql.NullString{}, sql.NullString{String: "present-on-chain", Valid: true}, true})
			continue
		}
		pairs = append(pairs,
			ledgerPair{jobID, "funded_amount", quote, funded, false},
			ledgerPair{jobID, "advance_principal", principal, chainPrincipal, false},
			ledgerPair{jobID, "advance_fee", fee, chainFee, false},
			ledgerPair{jobID, "advance_status", status, chainStatus, true},
		)
	}
	if err := rows.Err(); err != nil {
		return Reconciliation{}, err
	}

	now := time.Now().UTC()
	current := make(map[string]Alert)
	for _, pair := range pairs {
		if !pair.chain.Valid || strings.TrimSpace(pair.chain.String) == "" {
			continue // the corresponding chain lifecycle step has not happened yet
		}
		chainValue := pair.chain.String
		localValue := "<missing>"
		matches := false
		if pair.local.Valid && strings.TrimSpace(pair.local.String) != "" {
			if pair.status {
				localValue = canonicalStatus(pair.local.String)
				chainValue = canonicalStatus(pair.chain.String)
				matches = localValue == chainValue
			} else {
				atomic, err := usdcAtomic(pair.local.String)
				if err != nil {
					localValue = "<invalid:" + strings.TrimSpace(pair.local.String) + ">"
				} else {
					localValue = atomic
					matches = atomic == chainValue
				}
			}
		}
		if !matches {
			alert := Alert{JobID: pair.jobID, Field: pair.field, Local: localValue, Chain: chainValue, DetectedAt: now}
			current[alert.JobID+"\x00"+alert.Field] = alert
		}
	}

	tx, err := r.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return Reconciliation{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	existing, err := loadOpenAlerts(ctx, tx, r.chainID)
	if err != nil {
		return Reconciliation{}, err
	}
	for key, alert := range current {
		if id, ok := existing[key]; ok {
			if _, err := tx.ExecContext(ctx, `
				UPDATE reconciliation_alerts
				SET local_value = ?, chain_value = ?, detected_at = ?
				WHERE id = ?`, alert.Local, alert.Chain, now.UnixMilli(), id); err != nil {
				return Reconciliation{}, err
			}
			delete(existing, key)
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO reconciliation_alerts
			  (chain_id, job_id, field, local_value, chain_value, detected_at, resolved)
			VALUES (?, ?, ?, ?, ?, ?, 0)`,
			r.chainID, alert.JobID, alert.Field, alert.Local, alert.Chain, now.UnixMilli()); err != nil {
			return Reconciliation{}, err
		}
	}
	for _, id := range existing {
		if _, err := tx.ExecContext(ctx, `UPDATE reconciliation_alerts SET resolved = 1 WHERE id = ?`, id); err != nil {
			return Reconciliation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Reconciliation{}, err
	}

	result := Reconciliation{HasMismatch: len(current) > 0, Alerts: make([]Alert, 0, len(current))}
	for _, alert := range current {
		result.Alerts = append(result.Alerts, alert)
	}
	sort.Slice(result.Alerts, func(a, b int) bool {
		if result.Alerts[a].JobID != result.Alerts[b].JobID {
			return result.Alerts[a].JobID < result.Alerts[b].JobID
		}
		return result.Alerts[a].Field < result.Alerts[b].Field
	})
	return result, nil
}

func loadOpenAlerts(ctx context.Context, tx *sql.Tx, chainID uint64) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, job_id, field FROM reconciliation_alerts WHERE chain_id = ? AND resolved = 0`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var id int64
		var jobID, field string
		if err := rows.Scan(&id, &jobID, &field); err != nil {
			return nil, err
		}
		out[jobID+"\x00"+field] = id
	}
	return out, rows.Err()
}

func canonicalStatus(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// usdcAtomic parses a local human-facing USDC amount without floats. More than six fractional
// digits is rejected rather than rounded, so reconciliation is exact to the token's atomic unit.
func usdcAtomic(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+") {
		return "", fmt.Errorf("invalid USDC amount %q", v)
	}
	parts := strings.Split(v, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && (parts[1] == "" || len(parts[1]) > 6)) {
		return "", fmt.Errorf("invalid USDC amount %q", v)
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return "", fmt.Errorf("invalid USDC amount %q", v)
			}
		}
	}
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok {
		return "", fmt.Errorf("invalid USDC amount %q", v)
	}
	whole.Mul(whole, big.NewInt(1_000_000))
	if len(parts) == 2 && parts[1] != "" {
		fracText := parts[1] + strings.Repeat("0", 6-len(parts[1]))
		frac, ok := new(big.Int).SetString(fracText, 10)
		if !ok {
			return "", fmt.Errorf("invalid USDC amount %q", v)
		}
		whole.Add(whole, frac)
	}
	return whole.String(), nil
}
