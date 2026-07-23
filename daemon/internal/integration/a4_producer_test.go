package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/indexer"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// A4's engine, fed for the first time: Brain's jobs projection is a PRODUCER for the
// reconciler Anandan already shipped, which until now joined chain rows against an
// always-empty jobs table — a reconciler that never fires is indistinguishable from
// one that always passes. Against the golden fixture through his own pipeline, one run
// now produces a REAL MATCH (the funded amount) and a REAL MISMATCH (the advance
// fields the daemon cannot know yet) — both, from the same job, in the same run.

const (
	a4Chain = uint64(5_042_002)
	a4JobA  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type a4Source struct{ logs []indexer.Log }

func (a4Source) Head(context.Context) (uint64, error)    { return 200, nil }
func (a4Source) ChainID(context.Context) (uint64, error) { return a4Chain, nil }
func (s a4Source) Logs(_ context.Context, f indexer.Filter) ([]indexer.Log, error) {
	var out []indexer.Log
	for _, l := range s.logs {
		if l.BlockNumber >= f.FromBlock && l.BlockNumber <= f.ToBlock {
			out = append(out, l)
		}
	}
	return out, nil
}

func indexFixtureA4(t *testing.T, st *store.Store) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "indexer", "testdata", "h1-spine-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logs []indexer.Log
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	idx, err := indexer.New(a4Source{logs}, st, indexer.Config{
		ChainID: a4Chain,
		Addresses: []string{
			"0x1111111111111111111111111111111111111111",
			"0x2222222222222222222222222222222222222222",
			"0x3333333333333333333333333333333333333333",
		},
		StartBlock: 100, ConfirmationDepth: 0, ChunkSize: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		res, err := idx.SyncOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.NextBlock > 105 {
			return
		}
	}
	t.Fatal("fixture never fully indexed")
}

func alertFor(rec indexer.Reconciliation, jobID, field string) (indexer.Alert, bool) {
	for _, a := range rec.Alerts {
		if a.JobID == jobID && a.Field == field {
			return a, true
		}
	}
	return indexer.Alert{}, false
}

func TestA4_ReconcilerFiresAgainstBrainsProjection(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a4.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mem, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, mem, nil)
	b.SetScoper(brain.StubScoper{})
	ctx := context.Background()

	// A real job through Brain (quote 25.00 — the fixture's funded amount), bound to
	// the fixture's vault id the way on-chain job creation eventually will.
	if _, err := b.HandleOwnerRequest(ctx, "job_a4", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Update("job_a4", func(jm *brain.JobMemory) { jm.VaultJobID = a4JobA }); err != nil {
		t.Fatal(err)
	}
	indexFixtureA4(t, st)

	r, err := indexer.NewReconciler(st, a4Chain)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// REAL MATCH: quote 25.00 == funded 25000000 — no funded_amount alert for job A,
	// while the same job produces other alerts, so the silence is a verdict, not a skip.
	if a, found := alertFor(rec, a4JobA, "funded_amount"); found {
		t.Fatalf("funded amount must MATCH (25.00 vs 25000000), got alert %+v", a)
	}
	// REAL MISMATCH: the chain has an advance the local ledger cannot know yet (no
	// advance path) — the honest disagreement, surfaced.
	a, found := alertFor(rec, a4JobA, "advance_principal")
	if !found || a.Local != "<missing>" || a.Chain != "12500000" {
		t.Fatalf("advance_principal must mismatch as local <missing> vs chain 12500000: %+v (found=%v)", a, found)
	}
	if !rec.HasMismatch {
		t.Fatal("HasMismatch must be true")
	}

	// The SQL side is a projection, not truth: tamper the row, the reconciler sees a
	// (false) funded mismatch; reprojecting from memory heals it and his RESOLUTION
	// path marks the alert resolved.
	if _, err := st.DB().Exec(`UPDATE jobs SET quote_usdc='999.00' WHERE id='job_a4'`); err != nil {
		t.Fatal(err)
	}
	rec, err = r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a, found := alertFor(rec, a4JobA, "funded_amount"); !found || a.Local != "999000000" {
		t.Fatalf("tampered row must produce a funded mismatch: %+v (found=%v)", a, found)
	}
	if err := b.ReprojectJobs(ctx); err != nil {
		t.Fatal(err)
	}
	rec, err = r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := alertFor(rec, a4JobA, "funded_amount"); found {
		t.Fatal("after reprojection memory must win and the funded alert must resolve")
	}
	var resolved int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM reconciliation_alerts WHERE job_id=? AND field='funded_amount' AND resolved=1`,
		a4JobA).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("his resolution path must have marked the healed alert resolved, got %d", resolved)
	}
}
