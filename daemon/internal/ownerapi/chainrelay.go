package ownerapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// The chain half of H2 §2's "ONE stream, TWO sources".
//
// The decision in H2 §2 is that the daemon RELAYS the indexer's chain events rather than the
// dashboard opening a second subscription: the indexer and this daemon share one SQLite store,
// so relaying is a read of `chain_events`. The daemon half of that stream shipped; this half did
// not, and the gap was invisible because both halves compile and the stream stays green either
// way. The indexer writes `chain_events` (internal/indexer/indexer.go) while the stream reads
// `events` (written only by internal/store), so every chain event -- JobFunded, AdvanceIssued,
// ExpenseRecorded, JobSettled, RateChanged -- was being indexed correctly and then never reaching
// the browser. V10's money graph, V11's score ring and the Float page all read from that stream.

// chainCursor is a position in the (block_number, log_index) order the indexer writes and the
// H2 relay contract requires. It is NOT the SSE id: Last-Event-ID carries the daemon `seq` and is
// parsed with strconv.ParseInt(..., 10, 64) (H2 §2.1). A "block:logIndex" cursor is not a valid
// int64, so if a chain frame set the SSE id, the next reconnect would ParseInt it to (0, error),
// discard the error, and resume the daemon stream from seq 0 — replaying every daemon event.
// Chain frames therefore write no id at all.
type chainCursor struct {
	block    int64
	logIndex int64
}

// after reports whether an event at (b, li) is strictly past the cursor. Lexicographic on
// (block, logIndex), the same order chain_events_order_idx materialises.
func (c chainCursor) after(b, li int64) bool {
	return b > c.block || (b == c.block && li > c.logIndex)
}

// chainSeq is the H2 §2.1 chain sequence: the cursor "block:logIndex" as a string. Chain and
// daemon sequences are different types on purpose -- they are different orderings over different
// tables, and giving them one integer namespace would let a client compare them.
func chainSeq(b, li int64) string { return fmt.Sprintf("%d:%d", b, li) }

// latestChainCursor is where a fresh subscriber starts.
//
// It starts at the HEAD rather than at zero, so connecting does not replay the whole indexed
// history into a browser that only wants to watch the run about to happen; the snapshot carries
// the state that history produced. The cost is that events landing while a client is disconnected
// are missed, and H2 §2.1 gives no client-supplied chain resume point (Last-Event-ID is the daemon
// seq). That is a real limit, so it is written down here rather than left to be discovered: a
// client that must not miss chain events should re-read the snapshot on reconnect, which is what
// the dashboard does.
func latestChainCursor(ctx context.Context, db *sql.DB) chainCursor {
	var b, li sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT block_number, log_index FROM chain_events
		 ORDER BY block_number DESC, log_index DESC LIMIT 1`).Scan(&b, &li)
	if err != nil {
		// No rows, or no chain_* tables in this build. Either way there is nothing to relay
		// yet and a zero cursor is correct: the first event the indexer writes is "after" it.
		return chainCursor{}
	}
	return chainCursor{block: b.Int64, logIndex: li.Int64}
}

// chainFrame is one relayed event, already in H2 §2.1 envelope shape.
type chainFrame struct {
	cursor chainCursor
	msg    map[string]any
}

// readChainEvents returns every chain event strictly after the cursor, in relay order.
//
// The timestamp comes from chain_logs.observed_at because chain_events has no time column of its
// own; the join is on the same (chain_id, transaction_hash, log_index) key the foreign key already
// declares. A LEFT JOIN, so a chain_events row whose log has been pruned still relays rather than
// vanishing from the stream -- an event with an unknown time is worth more to the money graph than
// no event at all.
func readChainEvents(ctx context.Context, db *sql.DB, from chainCursor, limit int) ([]chainFrame, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT e.block_number, e.log_index, e.kind, e.entity_id,
		        COALESCE(e.actor,''), e.payload_json, e.transaction_hash,
		        COALESCE(l.observed_at, 0)
		   FROM chain_events e
		   LEFT JOIN chain_logs l
		     ON l.chain_id = e.chain_id
		    AND l.transaction_hash = e.transaction_hash
		    AND l.log_index = e.log_index
		  WHERE e.block_number > ? OR (e.block_number = ? AND e.log_index > ?)
		  ORDER BY e.block_number, e.log_index
		  LIMIT ?`, from.block, from.block, from.logIndex, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []chainFrame
	for rows.Next() {
		var (
			block, logIndex, observedAt int64
			kind, entityID, actor       string
			payloadJSON, txHash         string
		)
		if err := rows.Scan(&block, &logIndex, &kind, &entityID, &actor,
			&payloadJSON, &txHash, &observedAt); err != nil {
			return nil, err
		}

		var payload any
		if payloadJSON != "" {
			_ = json.Unmarshal([]byte(payloadJSON), &payload)
		}

		// The eight H1 kinds are relayed VERBATIM (H2 §2: "each keeping its already-frozen
		// vocabulary -- H2 renames nothing"). The dashboard's display names are presentation
		// and stay out of the wire contract.
		event := map[string]any{
			"kind":     kind,
			"entityId": entityID,
			"actor":    actor,
			"at":       chainTime(observedAt),
			"payload":  payload,
			"txHash":   txHash,
			"block":    block,
		}
		out = append(out, chainFrame{
			cursor: chainCursor{block: block, logIndex: logIndex},
			msg: map[string]any{
				"kind":   "event",
				"source": "chain",
				"seq":    chainSeq(block, logIndex),
				"event":  event,
			},
		})
	}
	return out, rows.Err()
}

// chainTime formats the observed time, or empty when the joined log was absent. Empty rather than
// the Unix epoch: a client can tell "unknown" from a real timestamp, and 1970 on a money graph
// reads as a bug in the chain rather than a missing join.
func chainTime(observedAt int64) string {
	if observedAt <= 0 {
		return ""
	}
	return time.UnixMilli(observedAt).UTC().Format(time.RFC3339)
}
