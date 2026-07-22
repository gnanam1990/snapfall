package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

const cursorStream = "h1-contract-logs"

// SyncOnce polls every finalized block not covered by the durable cursor and commits H1 in
// bounded chunks. A crash can expose either the old cursor and old projection, or the new cursor
// and new projection; it cannot expose half of one block range.
func (i *Indexer) SyncOnce(ctx context.Context) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.chainVerified {
		chainID, err := i.source.ChainID(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("reading RPC chain ID: %w", err)
		}
		if chainID != i.cfg.ChainID {
			return Result{}, fmt.Errorf("RPC chain ID %d does not match deployment chain ID %d", chainID, i.cfg.ChainID)
		}
		i.chainVerified = true
	}
	head, err := i.source.Head(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reading chain head: %w", err)
	}
	if head < i.cfg.ConfirmationDepth {
		return Result{}, nil
	}
	finalized := head - i.cfg.ConfirmationDepth
	next, err := i.nextBlock(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{FromBlock: next, NextBlock: next}
	if next > finalized {
		return result, nil
	}

	for from := next; from <= finalized; {
		to := from + i.cfg.ChunkSize - 1
		if to < from || to > finalized {
			to = finalized
		}
		logs, err := i.source.Logs(ctx, Filter{FromBlock: from, ToBlock: to, Addresses: append([]string(nil), i.cfg.Addresses...)})
		if err != nil {
			return result, fmt.Errorf("reading logs %d..%d: %w", from, to, err)
		}
		sort.SliceStable(logs, func(a, b int) bool {
			if logs[a].BlockNumber != logs[b].BlockNumber {
				return logs[a].BlockNumber < logs[b].BlockNumber
			}
			return logs[a].LogIndex < logs[b].LogIndex
		})
		for _, log := range logs {
			if log.BlockNumber < from || log.BlockNumber > to {
				return result, fmt.Errorf("RPC returned block %d outside requested range %d..%d", log.BlockNumber, from, to)
			}
			if log.Removed {
				return result, fmt.Errorf("removed log at (%d,%d); refusing to advance finalized cursor", log.BlockNumber, log.LogIndex)
			}
		}
		rawCount, eventCount, err := i.applyBatch(ctx, logs, to+1)
		if err != nil {
			return result, fmt.Errorf("committing logs %d..%d: %w", from, to, err)
		}
		result.RawLogs += rawCount
		result.Events += eventCount
		result.ThroughBlock = to
		result.NextBlock = to + 1
		if to == math.MaxUint64 {
			break
		}
		from = to + 1
	}
	return result, nil
}

func (i *Indexer) nextBlock(ctx context.Context) (uint64, error) {
	var next int64
	err := i.store.DB().QueryRowContext(ctx,
		`SELECT next_block_number FROM chain_cursors WHERE chain_id = ? AND stream = ?`,
		i.cfg.ChainID, cursorStream).Scan(&next)
	if err == sql.ErrNoRows {
		return i.cfg.StartBlock, nil
	}
	if err != nil {
		return 0, fmt.Errorf("loading H1 cursor: %w", err)
	}
	if next < 0 {
		return 0, fmt.Errorf("stored H1 cursor is negative: %d", next)
	}
	return uint64(next), nil
}

func (i *Indexer) applyBatch(ctx context.Context, logs []Log, nextBlock uint64) (int, int, error) {
	if nextBlock > math.MaxInt64 {
		return 0, 0, fmt.Errorf("cursor block %d exceeds SQLite integer range", nextBlock)
	}
	tx, err := i.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	rawCount, eventCount := 0, 0
	for _, log := range logs {
		if log.BlockNumber > math.MaxInt64 || log.LogIndex > math.MaxInt64 {
			return 0, 0, fmt.Errorf("log position (%d,%d) exceeds SQLite integer range", log.BlockNumber, log.LogIndex)
		}
		address, err := normalizeAddress(log.Address)
		if err != nil {
			return 0, 0, err
		}
		if len(log.Topics) == 0 {
			return 0, 0, fmt.Errorf("log at (%d,%d) has no topic0", log.BlockNumber, log.LogIndex)
		}
		transactionHash, err := normalizeHash32(log.TransactionHash)
		if err != nil {
			return 0, 0, fmt.Errorf("transaction hash at (%d,%d): %w", log.BlockNumber, log.LogIndex, err)
		}
		blockHash, err := normalizeHash32(log.BlockHash)
		if err != nil {
			return 0, 0, fmt.Errorf("block hash at (%d,%d): %w", log.BlockNumber, log.LogIndex, err)
		}
		topics := make([]string, len(log.Topics))
		for n, topic := range log.Topics {
			topics[n], err = normalizeHash32(topic)
			if err != nil {
				return 0, 0, fmt.Errorf("topic %d at (%d,%d): %w", n, log.BlockNumber, log.LogIndex, err)
			}
		}
		data, err := normalizeData(log.Data)
		if err != nil {
			return 0, 0, fmt.Errorf("data at (%d,%d): %w", log.BlockNumber, log.LogIndex, err)
		}
		topicsJSON, err := json.Marshal(topics)
		if err != nil {
			return 0, 0, err
		}
		insert, err := tx.ExecContext(ctx, `
			INSERT INTO chain_logs
			  (chain_id, transaction_hash, log_index, block_number, block_hash, contract_address,
			   topic0, topics_json, data, removed, decoded, observed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)
			ON CONFLICT(chain_id, transaction_hash, log_index) DO NOTHING`,
			i.cfg.ChainID, transactionHash, log.LogIndex, log.BlockNumber,
			blockHash, address, topics[0], string(topicsJSON), data, time.Now().UTC().UnixMilli())
		if err != nil {
			return 0, 0, err
		}
		inserted, err := insert.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		if inserted == 0 {
			continue
		}
		rawCount++

		event, supported, err := decode(log)
		if err != nil {
			return 0, 0, fmt.Errorf("decoding (%d,%d): %w", log.BlockNumber, log.LogIndex, err)
		}
		if !supported {
			continue
		}
		payload, err := event.payloadJSON()
		if err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chain_events
			  (chain_id, transaction_hash, log_index, block_number, contract_address,
			   kind, entity_id, actor, payload_json, h1_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '1.0')`,
			i.cfg.ChainID, transactionHash, log.LogIndex, log.BlockNumber,
			address, event.Kind, event.EntityID, nullable(event.Actor), payload); err != nil {
			return 0, 0, err
		}
		if err := project(ctx, tx, i.cfg.ChainID, log, event); err != nil {
			return 0, 0, fmt.Errorf("projecting %s: %w", event.Kind, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chain_logs SET decoded = 1
			WHERE chain_id = ? AND transaction_hash = ? AND log_index = ?`,
			i.cfg.ChainID, transactionHash, log.LogIndex); err != nil {
			return 0, 0, err
		}
		eventCount++
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chain_cursors (chain_id, stream, next_block_number, next_log_index, updated_at)
		VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(chain_id, stream) DO UPDATE SET
		  next_block_number = excluded.next_block_number,
		  next_log_index = excluded.next_log_index,
		  updated_at = excluded.updated_at`,
		i.cfg.ChainID, cursorStream, nextBlock, time.Now().UTC().UnixMilli()); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return rawCount, eventCount, nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
