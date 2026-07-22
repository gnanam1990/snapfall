package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strconv"
)

func project(ctx context.Context, tx *sql.Tx, chainID uint64, log Log, event decodedEvent) error {
	if event.Kind == "RateUpdated" {
		rate, err := strconv.ParseUint(event.Payload["rateBps"], 10, 16)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO chain_org_rates (chain_id, org_address, rate_bps, last_block_number, last_log_index)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(chain_id, org_address) DO UPDATE SET
			  rate_bps = excluded.rate_bps,
			  last_block_number = excluded.last_block_number,
			  last_log_index = excluded.last_log_index`,
			chainID, event.EntityID, rate, log.BlockNumber, log.LogIndex)
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chain_job_financials (chain_id, job_id, last_block_number, last_log_index)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chain_id, job_id) DO NOTHING`,
		chainID, event.EntityID, log.BlockNumber, log.LogIndex); err != nil {
		return err
	}

	var query string
	var args []any
	switch event.Kind {
	case "JobFunded":
		query = `UPDATE chain_job_financials SET funded_amount_atomic = ?, last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{event.Payload["amountAtomic"], log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	case "AdvanceIssued":
		query = `UPDATE chain_job_financials SET advance_principal_atomic = ?, advance_fee_atomic = ?, advance_status = 'Issued', last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{event.Payload["principalAtomic"], event.Payload["feeAtomic"], log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	case "ExpenseRecorded":
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT expense_total_atomic FROM chain_job_financials WHERE chain_id = ? AND job_id = ?`, chainID, event.EntityID).Scan(&current); err != nil {
			return err
		}
		total, err := addAtomic(current, event.Payload["amountAtomic"])
		if err != nil {
			return err
		}
		query = `UPDATE chain_job_financials SET expense_total_atomic = ?, last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{total, log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	case "DeliverySet":
		query = `UPDATE chain_job_financials SET delivery_hash = ?, last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{event.Payload["deliveryHash"], log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	case "JobSettled":
		query = `UPDATE chain_job_financials SET settlement_advance_repaid_atomic = ?, operator_net_atomic = ?, advance_status = CASE WHEN ? = '0' THEN advance_status ELSE 'Repaid' END, last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{event.Payload["advanceRepaidAtomic"], event.Payload["operatorNetAtomic"], event.Payload["advanceRepaidAtomic"], log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	case "AdvanceWrittenOff":
		query = `UPDATE chain_job_financials SET advance_status = 'WrittenOff', bond_slashed_atomic = ?, reserve_used_atomic = ?, socialized_atomic = ?, last_block_number = ?, last_log_index = ? WHERE chain_id = ? AND job_id = ?`
		args = []any{event.Payload["bondSlashedAtomic"], event.Payload["reserveUsedAtomic"], event.Payload["socializedAtomic"], log.BlockNumber, log.LogIndex, chainID, event.EntityID}
	default:
		return fmt.Errorf("unsupported H1 kind %q", event.Kind)
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func addAtomic(a, b string) (string, error) {
	left, ok := new(big.Int).SetString(a, 10)
	if !ok || left.Sign() < 0 {
		return "", fmt.Errorf("invalid atomic amount %q", a)
	}
	right, ok := new(big.Int).SetString(b, 10)
	if !ok || right.Sign() < 0 {
		return "", fmt.Errorf("invalid atomic amount %q", b)
	}
	return left.Add(left, right).String(), nil
}
