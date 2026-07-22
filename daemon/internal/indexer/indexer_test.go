package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

const (
	testChain  = uint64(5_042_002)
	vaultAddr  = "0x1111111111111111111111111111111111111111"
	poolAddr   = "0x2222222222222222222222222222222222222222"
	anchorAddr = "0x3333333333333333333333333333333333333333"
	jobA       = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	jobB       = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeSource struct {
	head  uint64
	logs  []Log
	calls []Filter
}

func (f *fakeSource) Head(context.Context) (uint64, error)    { return f.head, nil }
func (f *fakeSource) ChainID(context.Context) (uint64, error) { return testChain, nil }
func (f *fakeSource) Logs(_ context.Context, filter Filter) ([]Log, error) {
	f.calls = append(f.calls, filter)
	var out []Log
	for _, log := range f.logs {
		if log.BlockNumber >= filter.FromBlock && log.BlockNumber <= filter.ToBlock {
			out = append(out, log)
		}
	}
	return out, nil
}

type wrongChainSource struct{ *fakeSource }

func (wrongChainSource) ChainID(context.Context) (uint64, error) { return 1, nil }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "indexer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fixtureLogs(t *testing.T) []Log {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "h1-spine-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logs []Log
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	return logs
}

func newTestIndexer(t *testing.T, st *store.Store, source Source) *Indexer {
	t.Helper()
	idx, err := New(source, st, Config{
		ChainID: testChain, Addresses: []string{vaultAddr, poolAddr, anchorAddr},
		StartBlock: 100, ConfirmationDepth: 0, ChunkSize: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestSyncOnceOrdersProjectsAndReplaysAllH1Events(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	source := &fakeSource{head: 106, logs: fixtureLogs(t)}
	idx := newTestIndexer(t, st, source)

	result, err := idx.SyncOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.RawLogs != 7 || result.Events != 7 || result.NextBlock != 107 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(source.calls) != 3 {
		t.Fatalf("RPC calls = %d, want 3 bounded chunks: %+v", len(source.calls), source.calls)
	}

	rows, err := st.DB().QueryContext(ctx, `SELECT kind FROM chain_events ORDER BY block_number, log_index`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	wantKinds := []string{"JobFunded", "AdvanceIssued", "ExpenseRecorded", "DeliverySet", "JobSettled", "AdvanceWrittenOff", "RateUpdated"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}

	var funded, principal, fee, expenses, repaid, operatorNet, status string
	if err := st.DB().QueryRowContext(ctx, `
		SELECT funded_amount_atomic, advance_principal_atomic, advance_fee_atomic,
		       expense_total_atomic, settlement_advance_repaid_atomic, operator_net_atomic, advance_status
		FROM chain_job_financials WHERE chain_id = ? AND job_id = ?`, testChain, jobA).
		Scan(&funded, &principal, &fee, &expenses, &repaid, &operatorNet, &status); err != nil {
		t.Fatal(err)
	}
	got := []string{funded, principal, fee, expenses, repaid, operatorNet, status}
	want := []string{"25000000", "12500000", "250000", "40000", "12750000", "12250000", "Repaid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("job projection = %v, want %v", got, want)
	}
	var rate int
	if err := st.DB().QueryRowContext(ctx, `SELECT rate_bps FROM chain_org_rates WHERE chain_id = ?`, testChain).Scan(&rate); err != nil {
		t.Fatal(err)
	}
	if rate != 5500 {
		t.Fatalf("rate = %d", rate)
	}

	// Force an inclusive replay from the original block. The raw receipt key must suppress
	// duplicate events and, critically, must not add the expense a second time.
	if _, err := st.DB().ExecContext(ctx, `UPDATE chain_cursors SET next_block_number = 100 WHERE chain_id = ?`, testChain); err != nil {
		t.Fatal(err)
	}
	replay, err := idx.SyncOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RawLogs != 0 || replay.Events != 0 {
		t.Fatalf("replay produced effects: %+v", replay)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT expense_total_atomic FROM chain_job_financials WHERE chain_id = ? AND job_id = ?`, testChain, jobA).Scan(&expenses); err != nil {
		t.Fatal(err)
	}
	if expenses != "40000" {
		t.Fatalf("replay doubled expense: %s", expenses)
	}
}

func TestSyncOnceAdvancesAcrossEmptyBlocks(t *testing.T) {
	st := openStore(t)
	source := &fakeSource{head: 105}
	idx := newTestIndexer(t, st, source)
	result, err := idx.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.NextBlock != 106 || result.RawLogs != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSyncOnceRejectsWrongRPCNetworkBeforeReadingLogs(t *testing.T) {
	st := openStore(t)
	source := &fakeSource{head: 105}
	idx := newTestIndexer(t, st, wrongChainSource{source})
	if _, err := idx.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected chain ID mismatch")
	}
	if len(source.calls) != 0 {
		t.Fatalf("read logs from wrong network")
	}
}

func TestSyncOnceFailsClosedBeforeOffendingChunk(t *testing.T) {
	st := openStore(t)
	logs := fixtureLogs(t)
	logs[0].Removed = true
	sort.Slice(logs, func(a, b int) bool { return logs[a].BlockNumber < logs[b].BlockNumber })
	source := &fakeSource{head: 106, logs: logs}
	idx := newTestIndexer(t, st, source)
	if _, err := idx.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected removed-log failure")
	}
	var next int
	if err := st.DB().QueryRow(`SELECT next_block_number FROM chain_cursors WHERE chain_id = ?`, testChain).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 106 {
		t.Fatalf("cursor = %d, want 106 after the last fully committed chunk", next)
	}
	var later int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM chain_logs WHERE block_number >= 106`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if later != 0 {
		t.Fatalf("offending or later chunk partially committed %d logs", later)
	}
}

func TestUnknownAuditLogIsRetainedRaw(t *testing.T) {
	st := openStore(t)
	unknown := Log{
		Address: anchorAddr, Topics: []string{"0xc9278fc7b52eb749b0e7d3e72637b99ddb5b9755ea5bdf3b1306296647f2bc48"},
		Data: "0x", BlockNumber: 100,
		BlockHash:       "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		TransactionHash: "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", LogIndex: 0,
	}
	source := &fakeSource{head: 100, logs: []Log{unknown}}
	result, err := newTestIndexer(t, st, source).SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RawLogs != 1 || result.Events != 0 {
		t.Fatalf("result = %+v", result)
	}
	var decoded int
	if err := st.DB().QueryRow(`SELECT decoded FROM chain_logs`).Scan(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != 0 {
		t.Fatalf("unknown audit log marked decoded")
	}
}
