package brain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

const (
	g12Chain = uint64(5_042_002)
	g12Vault = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func newBillingBrain(t *testing.T) (*Brain, *store.Store, *MemoryStore) {
	t.Helper()
	b, st, _ := newTestBrain(t)
	mem, err := NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	b.memory = mem
	b.SetBilling(billing.New(st, g12Chain, nil))
	return b, st, mem
}

// seedSettledJob writes a JobSettled chain event (with its chain_logs FK row and a
// consistent financials projection) the way the indexer would have.
func seedChainRow(t *testing.T, st *store.Store, kind, entity, tx string, block uint64, payload map[string]string) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO chain_logs (chain_id, transaction_hash, log_index, block_number, block_hash,
		contract_address, topic0, topics_json, data, removed, decoded, observed_at)
		VALUES (?, ?, 0, ?, '0xff', '0x11', '0x00', '[]', '0x', 0, 1, 0)`, g12Chain, tx, block)
	mustExec(`INSERT INTO chain_events (chain_id, transaction_hash, log_index, block_number,
		contract_address, kind, entity_id, actor, payload_json)
		VALUES (?, ?, 0, ?, '0x11', ?, ?, '', ?)`, g12Chain, tx, block, kind, entity, string(raw))
	mustExec(`INSERT INTO chain_job_financials (chain_id, job_id, last_block_number, last_log_index)
		VALUES (?, ?, ?, 0) ON CONFLICT(chain_id, job_id) DO NOTHING`, g12Chain, entity, block)
	switch kind {
	case "JobFunded":
		mustExec(`UPDATE chain_job_financials SET funded_amount_atomic=? WHERE chain_id=? AND job_id=?`,
			payload["amountAtomic"], g12Chain, entity)
	case "JobSettled":
		mustExec(`UPDATE chain_job_financials SET settlement_advance_repaid_atomic=?, operator_net_atomic=?
			WHERE chain_id=? AND job_id=?`,
			payload["advanceRepaidAtomic"], payload["operatorNetAtomic"], g12Chain, entity)
	default:
		t.Fatalf("seedChainRow does not model projection for %q", kind)
	}
}

func billingEvents(t *testing.T, st *store.Store, jobID string) []map[string]any {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT payload_json FROM events WHERE kind='billing.invoice' AND entity_id=? ORDER BY seq`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(p), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// Regeneration semantics, decided append-only: each generation appends a durable
// billing.invoice event with a monotonic version; a later version reflects chain rows
// the earlier one predated. Two generations never leave two records claiming to be THE
// invoice — the versions ARE the history.
func TestG12_GenerateInvoiceVersionsAppendOnly(t *testing.T) {
	b, st, mem := newBillingBrain(t)
	ctx := context.Background()

	draft, _ := json.Marshal(envelope.Deliverable{
		Title: "Vendor risk report",
		Provenance: []envelope.SourceProvenance{
			{Resource: "GET /v1/benchmark", AmountAtomic: "60000", Status: "pending-integration"},
		},
	})
	if err := mem.Update("job_g12", func(jm *JobMemory) {
		jm.Scope, jm.Stage, jm.VaultJobID, jm.Draft = "Vendor risk — Acme Corp", "delivery_ready", g12Vault, string(draft)
	}); err != nil {
		t.Fatal(err)
	}

	// v1: nothing on chain — a partial, all-gaps invoice with the Draft's provenance
	// surfacing as a pending-settlement outcome (memory contributes labels + provenance
	// input, never an amount).
	rec1, err := b.GenerateInvoice(ctx, "job_g12", "owner-request")
	if err != nil {
		t.Fatal(err)
	}
	if rec1.Version != 1 || rec1.Owner.Status != billing.StatusPartial || len(rec1.Owner.Lines) != 0 {
		t.Fatalf("v1: %+v", rec1)
	}
	if rec1.Owner.Labels.Title != "Vendor risk — Acme Corp" {
		t.Fatalf("labels lost: %+v", rec1.Owner.Labels)
	}
	if len(rec1.Reconciliation.Outcomes) != 1 || rec1.Reconciliation.Outcomes[0].Outcome != billing.OutcomePendingSettlement {
		t.Fatalf("draft provenance did not reach the join: %+v", rec1.Reconciliation.Outcomes)
	}

	// The chain record fills; regenerating picks it up as v2 — v1 stays durable.
	seedChainRow(t, st, "JobFunded", g12Vault, "0x01", 100, map[string]string{"amountAtomic": "25000000"})
	rec2, err := b.GenerateInvoice(ctx, "job_g12", "owner-request")
	if err != nil {
		t.Fatal(err)
	}
	if rec2.Version != 2 || len(rec2.Owner.Lines) != 1 || rec2.Owner.Totals.FundedAtomic != "25000000" {
		t.Fatalf("v2: %+v", rec2)
	}

	evs := billingEvents(t, st, "job_g12")
	if len(evs) != 2 || evs[0]["version"].(float64) != 1 || evs[1]["version"].(float64) != 2 {
		t.Fatalf("durable record must hold BOTH versions in order: %+v", evs)
	}

	// A job the daemon never touched cannot be invoiced.
	if _, err := b.GenerateInvoice(ctx, "job_nope", "owner-request"); !errors.Is(err, billing.ErrUnknownJob) {
		t.Fatalf("unknown job: %v, want ErrUnknownJob", err)
	}
}

// The settlement-observed trigger: fires once per job when a JobSettled row for its
// vault id reaches the shared store, and never for jobs with no vault mapping. Honesty:
// this path has never run against a real chain (none exists — the chain gap); this test
// is seeded rows through the same store the indexer writes.
func TestG12_SettlementObservedGeneratesOnce(t *testing.T) {
	b, st, mem := newBillingBrain(t)
	ctx := context.Background()

	// One job with the vault mapping, one without.
	if err := mem.Update("job_mapped", func(jm *JobMemory) {
		jm.Scope, jm.Stage, jm.VaultJobID = "scope", "delivery_ready", g12Vault
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.Update("job_unmapped", func(jm *JobMemory) {
		jm.Scope, jm.Stage = "scope", "delivery_ready"
	}); err != nil {
		t.Fatal(err)
	}

	// No settlement on chain yet: nothing to observe.
	if n, err := b.ObserveSettlementsOnce(ctx); err != nil || n != 0 {
		t.Fatalf("before settlement: n=%d err=%v", n, err)
	}

	seedChainRow(t, st, "JobSettled", g12Vault, "0x02", 104,
		map[string]string{"advanceRepaidAtomic": "12750000", "operatorNetAtomic": "12250000"})

	// Observed: exactly one generation, trigger recorded, for the mapped job only.
	if n, err := b.ObserveSettlementsOnce(ctx); err != nil || n != 1 {
		t.Fatalf("observe: n=%d err=%v", n, err)
	}
	evs := billingEvents(t, st, "job_mapped")
	if len(evs) != 1 || evs[0]["trigger"] != "settlement-observed" {
		t.Fatalf("mapped job events: %+v", evs)
	}
	if evs := billingEvents(t, st, "job_unmapped"); len(evs) != 0 {
		t.Fatalf("unmapped job must not be invoiced: %+v", evs)
	}

	// Observing again is idempotent — no version spam from the watcher loop.
	if n, err := b.ObserveSettlementsOnce(ctx); err != nil || n != 0 {
		t.Fatalf("re-observe: n=%d err=%v", n, err)
	}
	if evs := billingEvents(t, st, "job_mapped"); len(evs) != 1 {
		t.Fatalf("watcher appended a duplicate: %+v", evs)
	}

	// An owner request AFTER the settlement version still appends (append-only, v2) —
	// the dedupe is per-trigger, not a freeze.
	if _, err := b.GenerateInvoice(ctx, "job_mapped", "owner-request"); err != nil {
		t.Fatal(err)
	}
	if evs := billingEvents(t, st, "job_mapped"); len(evs) != 2 {
		t.Fatalf("owner request after settlement: %+v", evs)
	}
}

// The wiring law: the brain package invokes the billing agent from EXACTLY ONE site
// (GenerateInvoice), the same technique as the dispatch chokepoint. The scan counts the
// agent-invocation token in non-test sources; doc comments must not carry it.
func TestG12_BillingInvocationSiteIsSingle(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	token := ".Invoice("
	count := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		count += strings.Count(string(src), token)
	}
	if count != 1 {
		t.Fatalf("billing invocation sites in brain: %d, want exactly 1 (GenerateInvoice)", count)
	}
}
