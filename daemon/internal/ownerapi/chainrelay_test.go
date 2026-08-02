package ownerapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The regression this package needed and did not have.
//
// The daemon half of H2 §2 shipped and the chain half did not, and NOTHING went red: the indexer
// wrote chain_events, the stream read events, both compiled, every existing test passed, and the
// only symptom was a money graph that never moved. A test that drives the stream end to end would
// have caught it on day one, so these drive the relay directly against a real SQLite store.

// chainStore opens the REAL store schema rather than a hand-copied one. A local copy would let
// this test keep passing after daemon/store/schema.sql changed underneath it, which is the same
// class of drift that produced the bug being tested: two places describing one table.
func chainStore(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

func insertChainEvent(t *testing.T, db *sql.DB, block, logIndex int64, kind, entity, payload string, observedAt int64) {
	t.Helper()
	tx := "0x" + kind + string(rune('a'+int(logIndex%26)))
	if _, err := db.Exec(
		`INSERT INTO chain_logs (chain_id, transaction_hash, log_index, block_number, block_hash,
			contract_address, topic0, topics_json, data, observed_at)
		 VALUES (1, ?, ?, ?, '0xb', '0xc', '0xt', '[]', '0x', ?)`,
		tx, logIndex, block, observedAt); err != nil {
		t.Fatalf("chain_logs: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO chain_events (chain_id, transaction_hash, log_index, block_number,
			contract_address, kind, entity_id, actor, payload_json)
		 VALUES (1, ?, ?, ?, '0xc', ?, ?, 'chain', ?)`,
		tx, logIndex, block, kind, entity, payload); err != nil {
		t.Fatalf("chain_events: %v", err)
	}
}

// The eight H1 kinds must reach the stream, in (block, logIndex) order, with their vocabulary
// untouched. H2 §2 is explicit that it renames nothing.
func TestChainEventsAreRelayedInOrderWithFrozenKinds(t *testing.T) {
	db := chainStore(t)
	// Deliberately inserted out of order, and with two events sharing a block, so passing
	// requires the (block, logIndex) sort rather than insertion order.
	insertChainEvent(t, db, 200, 1, "JobSettled", "0xjob", `{"amountAtomic":"25000000"}`, 1700000003000)
	insertChainEvent(t, db, 100, 2, "AdvanceIssued", "0xjob", `{"principalAtomic":"12500000"}`, 1700000001000)
	insertChainEvent(t, db, 100, 0, "JobFunded", "0xjob", `{"amountAtomic":"25000000"}`, 1700000000000)
	insertChainEvent(t, db, 150, 0, "RateChanged", "0xorg", `{"newRateBps":5500}`, 1700000002000)

	frames, err := readChainEvents(context.Background(), db, chainCursor{}, 256)
	if err != nil {
		t.Fatalf("readChainEvents: %v", err)
	}
	if len(frames) != 4 {
		t.Fatalf("relayed %d events, want 4: the money graph gets only what this returns", len(frames))
	}

	wantKinds := []string{"JobFunded", "AdvanceIssued", "RateChanged", "JobSettled"}
	wantSeq := []string{"100:0", "100:2", "150:0", "200:1"}
	for i, f := range frames {
		if got := f.msg["source"]; got != "chain" {
			t.Errorf("frame %d source %v, want \"chain\": the dashboard routes on this", i, got)
		}
		if got := f.msg["seq"]; got != wantSeq[i] {
			t.Errorf("frame %d seq %v, want %s", i, got, wantSeq[i])
		}
		ev := f.msg["event"].(map[string]any)
		if got := ev["kind"]; got != wantKinds[i] {
			t.Errorf("frame %d kind %v, want %s (H2 §2 renames nothing)", i, got, wantKinds[i])
		}
		if ev["entityId"] == "" {
			t.Errorf("frame %d has no entityId; chain events carry it instead of jobId", i)
		}
	}

	// Payloads must survive as structured JSON, not as a re-encoded string: H1 §2 says amounts
	// are base-10 atomic strings and the graph reads them out of the payload.
	first := frames[0].msg["event"].(map[string]any)["payload"].(map[string]any)
	if first["amountAtomic"] != "25000000" {
		t.Errorf("payload amount %v, want the atomic string verbatim", first["amountAtomic"])
	}
}

// A cursor must not re-deliver what it has already seen, or every poll would replay the run.
func TestChainCursorAdvancesAndDoesNotRepeat(t *testing.T) {
	db := chainStore(t)
	insertChainEvent(t, db, 100, 0, "JobFunded", "0xjob", `{}`, 1700000000000)
	insertChainEvent(t, db, 100, 5, "AdvanceIssued", "0xjob", `{}`, 1700000001000)

	frames, err := readChainEvents(context.Background(), db, chainCursor{}, 256)
	if err != nil || len(frames) != 2 {
		t.Fatalf("first read: %d frames, err %v", len(frames), err)
	}
	at := frames[len(frames)-1].cursor

	again, err := readChainEvents(context.Background(), db, at, 256)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-read returned %d frames from cursor %v; a poll loop would replay the run every tick", len(again), at)
	}

	// A new event in the SAME block must still be delivered: the cursor is (block, logIndex),
	// and comparing on block alone would drop every event after the first in a busy block.
	insertChainEvent(t, db, 100, 9, "JobSettled", "0xjob", `{}`, 1700000002000)
	next, err := readChainEvents(context.Background(), db, at, 256)
	if err != nil || len(next) != 1 {
		t.Fatalf("same-block follow-up: %d frames, err %v; want 1", len(next), err)
	}
	if got := next[0].msg["seq"]; got != "100:9" {
		t.Errorf("seq %v, want 100:9", got)
	}
}

// The LEFT JOIN in readChainEvents is defensive, and this pins WHY it never has to fire: the
// store opens with foreign_keys(1) and chain_events carries an FK onto chain_logs, so an event
// whose log is missing cannot be inserted at all. Written as a test rather than a comment because
// a future schema that drops the FK would make the join's COALESCE load-bearing overnight, and
// this fails the moment that happens.
func TestSchemaForbidsAChainEventWithoutItsLog(t *testing.T) {
	db := chainStore(t)
	_, err := db.Exec(
		`INSERT INTO chain_events (chain_id, transaction_hash, log_index, block_number,
			contract_address, kind, entity_id, actor, payload_json)
		 VALUES (1, '0xorphan', 0, 300, '0xc', 'AdvanceRepaid', '0xjob', 'chain', '{}')`)
	if err == nil {
		t.Fatal("an orphaned chain_events row was accepted; the FK onto chain_logs is no longer " +
			"enforced, so readChainEvents' COALESCE on observed_at is now load-bearing and needs " +
			"its own coverage")
	}

	// And with its log present, the same row relays with a real timestamp.
	insertChainEvent(t, db, 300, 0, "AdvanceRepaid", "0xjob", `{}`, 1700000009000)
	frames, err := readChainEvents(context.Background(), db, chainCursor{}, 256)
	if err != nil || len(frames) != 1 {
		t.Fatalf("%d frames, err %v; want 1", len(frames), err)
	}
	if at := frames[0].msg["event"].(map[string]any)["at"]; at == "" {
		t.Error("at is empty although the joined log exists")
	}
}

// Aggregates: absent means absent, not zero. Sending treasuryUsdc:"0" against an un-indexed store
// would be a claim the daemon cannot support.
func TestAggregatesAreOmittedUntilTheIndexerHasRun(t *testing.T) {
	db := chainStore(t)
	if agg := computeAggregates(context.Background(), db, "0xorg", 0); agg != nil {
		t.Fatalf("empty projections produced %+v, want nil so the field is omitted", agg)
	}
}

func TestAggregatesReadTheIndexerProjections(t *testing.T) {
	db := chainStore(t)
	if _, err := db.Exec(`INSERT INTO chain_org_rates VALUES (1, '0xORG', 5500, 150, 0)`); err != nil {
		t.Fatalf("rates: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO chain_job_financials (chain_id, job_id, funded_amount_atomic,
			advance_principal_atomic, advance_fee_atomic, advance_status, expense_total_atomic,
			last_block_number, last_log_index)
		 VALUES (1, '0xjob', '25000000', '12500000', '250000', 'Issued', '100000', 150, 1)`); err != nil {
		t.Fatalf("financials: %v", err)
	}

	// Lower-cased org on purpose: addresses are written by whatever cased them, and a
	// checksum-casing mismatch would leave the ring blank with every table populated.
	agg := computeAggregates(context.Background(), db, "0xorg", 2)
	if agg == nil {
		t.Fatal("aggregates nil with both projections populated")
	}
	if agg.Pool == nil || agg.Pool.OrgRateBps != 5500 {
		t.Errorf("pool = %+v, want orgRateBps 5500: this is what V11's ring reads", agg.Pool)
	}
	if len(agg.OpenAdvances) != 1 || agg.OpenAdvances[0].PrincipalAtomic != "12500000" {
		t.Errorf("openAdvances = %+v, want one at 12500000", agg.OpenAdvances)
	}
	if agg.PendingApprovals != 2 {
		t.Errorf("pendingApprovals = %d, want 2", agg.PendingApprovals)
	}

	// Amounts must serialize as STRINGS. A float here would round a 6-decimal balance and the
	// dashboard would render a number that does not reconcile against the chain.
	raw, err := json.Marshal(agg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("invalid JSON")
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	adv := back["openAdvances"].([]any)[0].(map[string]any)
	if _, ok := adv["principalAtomic"].(string); !ok {
		t.Errorf("principalAtomic serialized as %T, want string (H1 §2)", adv["principalAtomic"])
	}
}

// A repaid advance is not outstanding money and must not be presented as such.
func TestOnlyIssuedAdvancesCountAsOpen(t *testing.T) {
	db := chainStore(t)
	for i, status := range []string{"Issued", "Repaid", "WrittenOff"} {
		if _, err := db.Exec(
			`INSERT INTO chain_job_financials (chain_id, job_id, funded_amount_atomic,
				advance_principal_atomic, advance_fee_atomic, advance_status,
				expense_total_atomic, last_block_number, last_log_index)
			 VALUES (1, ?, '25000000', '12500000', '250000', ?, '0', ?, 0)`,
			status+"_job", status, 100+i); err != nil {
			t.Fatalf("insert %s: %v", status, err)
		}
	}
	agg := computeAggregates(context.Background(), db, "", 0)
	if agg == nil {
		t.Fatal("aggregates nil")
	}
	if len(agg.OpenAdvances) != 1 {
		t.Fatalf("openAdvances = %d, want 1: Repaid and WrittenOff are terminal, not outstanding", len(agg.OpenAdvances))
	}
	if agg.OpenAdvances[0].JobID != "Issued_job" {
		t.Errorf("open advance is %s, want Issued_job", agg.OpenAdvances[0].JobID)
	}
	if len(agg.ActiveJobs) != 3 {
		t.Errorf("activeJobs = %d, want all 3 funded jobs", len(agg.ActiveJobs))
	}
}
