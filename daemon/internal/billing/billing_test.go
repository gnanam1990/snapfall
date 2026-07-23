package billing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/indexer"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The fixture constants are Anandan's, verbatim from indexer_test.go — same chain, same
// contracts, same jobs. The ratified spine numbers live in testdata/h1-spine-logs.json.
const (
	testChain  = uint64(5_042_002)
	vaultAddr  = "0x1111111111111111111111111111111111111111"
	poolAddr   = "0x2222222222222222222222222222222222222222"
	anchorAddr = "0x3333333333333333333333333333333333333333"
	jobA       = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	jobB       = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// The fixture's ExpenseRecorded receiptHash and DeliverySubmitted deliveryHash.
	fixtureReceipt  = "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	fixtureDelivery = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var fixedNow = time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newAgent(st *store.Store) *Agent {
	return New(st, testChain, func() time.Time { return fixedNow })
}

// spineSource replays the committed golden fixture through Anandan's OWN pipeline —
// his decode and project code untouched. Synthetic input, his code path, his exact
// schema; NOT a testnet run (A2's done-criterion is unmet on the record — see the
// package doc's ceiling notes).
type spineSource struct {
	head uint64
	logs []indexer.Log
}

func (s spineSource) Head(context.Context) (uint64, error)    { return s.head, nil }
func (s spineSource) ChainID(context.Context) (uint64, error) { return testChain, nil }
func (s spineSource) Logs(_ context.Context, f indexer.Filter) ([]indexer.Log, error) {
	var out []indexer.Log
	for _, l := range s.logs {
		if l.BlockNumber >= f.FromBlock && l.BlockNumber <= f.ToBlock {
			out = append(out, l)
		}
	}
	return out, nil
}

func indexFixture(t *testing.T, st *store.Store) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "indexer", "testdata", "h1-spine-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logs []indexer.Log
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	idx, err := indexer.New(spineSource{head: 200, logs: logs}, st, indexer.Config{
		ChainID: testChain, Addresses: []string{vaultAddr, poolAddr, anchorAddr},
		StartBlock: 100, ConfirmationDepth: 0, ChunkSize: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		res, err := idx.SyncOnce(context.Background())
		if err != nil {
			t.Fatalf("SyncOnce: %v", err)
		}
		if res.NextBlock > 105 {
			return
		}
	}
	t.Fatal("indexer never reached the end of the fixture spine")
}

// seedChainEvent writes one normalized chain event directly (chain_logs FK row included)
// and applies the SAME projection semantics his project.go applies for the kinds the
// synthetic tests use, so the cross-check starts consistent and tests tamper explicitly.
func seedChainEvent(t *testing.T, st *store.Store, block, li uint64, tx, kind, entity string, payload map[string]string) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chain_logs (chain_id, transaction_hash, log_index, block_number, block_hash,
		  contract_address, topic0, topics_json, data, removed, decoded, observed_at)
		VALUES (?, ?, ?, ?, '0xff', ?, '0x00', '[]', '0x', 0, 1, 0)`,
		testChain, tx, li, block, vaultAddr); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chain_events (chain_id, transaction_hash, log_index, block_number,
		  contract_address, kind, entity_id, actor, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`,
		testChain, tx, li, block, vaultAddr, kind, entity, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO chain_job_financials (chain_id, job_id, last_block_number, last_log_index)
		VALUES (?, ?, ?, ?) ON CONFLICT(chain_id, job_id) DO NOTHING`,
		testChain, entity, block, li); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "JobFunded":
		if _, err := db.ExecContext(ctx,
			`UPDATE chain_job_financials SET funded_amount_atomic = ? WHERE chain_id = ? AND job_id = ?`,
			payload["amountAtomic"], testChain, entity); err != nil {
			t.Fatal(err)
		}
	case "ExpenseRecorded":
		if _, err := db.ExecContext(ctx, `
			UPDATE chain_job_financials
			SET expense_total_atomic = CAST(CAST(expense_total_atomic AS INTEGER) + CAST(? AS INTEGER) AS TEXT)
			WHERE chain_id = ? AND job_id = ?`,
			payload["amountAtomic"], testChain, entity); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("seedChainEvent does not model projection for kind %q", kind)
	}
}

func gapStages(gaps []Gap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.Stage)
	}
	return out
}

func alertsOfKind(alerts []Alert, kind string) []Alert {
	var out []Alert
	for _, a := range alerts {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// The buildable-today proxy for G12's done-criterion ("invoice totals reconcile to
// chain to the cent on a real spine run" — a real run is blocked on the unowned chain
// gap): the golden spine through Anandan's pipeline invoices completely, to the cent,
// with the receiptHash join landing end to end on his projected rows.
func TestG12_CompleteInvoiceFromFixtureThroughAnandansPipeline(t *testing.T) {
	st := openStore(t)
	indexFixture(t, st)

	set, err := newAgent(st).Invoice(context.Background(), Request{
		JobID: "job_demo", VaultJobID: jobA,
		Labels: Labels{Title: "Vendor risk report — Acme Corp", CustomerRef: "acme"},
		Provenance: []envelope.SourceProvenance{{
			Resource: "GET /v1/benchmark", Merchant: "0x9999999999999999999999999999999999999999",
			AmountAtomic: "40000", ReceiptHash: fixtureReceipt, Status: "proven",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := set.Owner
	if owner.Status != StatusComplete {
		t.Fatalf("status %q, want %q (gaps: %v)", owner.Status, StatusComplete, owner.Gaps)
	}
	if len(owner.Gaps) != 0 || len(set.Alerts) != 0 {
		t.Fatalf("full spine must have no gaps and no alerts: gaps=%v alerts=%v", owner.Gaps, set.Alerts)
	}

	wantKinds := []string{"JobFunded", "AdvanceIssued", "ExpenseRecorded", "DeliverySubmitted", "AdvanceRepaid", "JobSettled"}
	if got := lineKinds(owner.Lines); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("lines %v, want chain order %v", got, wantKinds)
	}
	for _, l := range owner.Lines {
		if l.TxHash == "" {
			t.Fatalf("line %s has no tx hash — surfaces cannot explorer-link it", l.Kind)
		}
	}

	// To the cent, every field mirroring chain_job_financials.
	want := Totals{
		FundedAtomic: "25000000", AdvancePrincipalAtomic: "12500000", AdvanceFeeAtomic: "250000",
		ExpenseTotalAtomic: "40000", SettlementAdvanceRepaidAtomic: "12750000", OperatorNetAtomic: "12250000",
	}
	if owner.Totals != want {
		t.Fatalf("totals %+v, want %+v", owner.Totals, want)
	}

	// The join key survives his decode: the expense line carries the fixture receiptHash
	// and the provenance entry matches on it EXACTLY.
	var expense *Line
	for i := range owner.Lines {
		if owner.Lines[i].Kind == "ExpenseRecorded" {
			expense = &owner.Lines[i]
		}
	}
	if expense == nil || expense.ReceiptHash != fixtureReceipt {
		t.Fatalf("expense line receiptHash: %+v, want %s", expense, fixtureReceipt)
	}
	if len(set.Reconciliation.Outcomes) != 1 || set.Reconciliation.Outcomes[0].Outcome != OutcomeMatched {
		t.Fatalf("join outcomes %+v, want exactly one %q", set.Reconciliation.Outcomes, OutcomeMatched)
	}
	if owner.DeliveryHash != fixtureDelivery {
		t.Fatalf("deliveryHash %q, want %q", owner.DeliveryHash, fixtureDelivery)
	}

	// Both copies carry the SAME chain truth — honesty is not addressee-dependent.
	if set.Customer.Copy != CopyCustomer || owner.Copy != CopyOwner {
		t.Fatalf("copies mislabeled: %q / %q", owner.Copy, set.Customer.Copy)
	}
	if !reflect.DeepEqual(set.Customer.Lines, owner.Lines) || set.Customer.Totals != owner.Totals {
		t.Fatal("customer copy diverged from chain truth")
	}
}

// jobB's spine holds ONLY the write-off: the invoice must show the write-off line and
// mark funding/advance/delivery as absent — absent is empty, never a fabricated zero.
func TestG12_WriteOffJobInvoicesPartialWithAbsenceNotZero(t *testing.T) {
	st := openStore(t)
	indexFixture(t, st)

	set, err := newAgent(st).Invoice(context.Background(), Request{JobID: "job_b", VaultJobID: jobB})
	if err != nil {
		t.Fatal(err)
	}
	owner := set.Owner
	if owner.Status != StatusPartial {
		t.Fatalf("status %q, want %q", owner.Status, StatusPartial)
	}
	if got := lineKinds(owner.Lines); !reflect.DeepEqual(got, []string{"AdvanceWrittenOff"}) {
		t.Fatalf("lines %v, want the write-off alone", got)
	}
	if owner.Lines[0].Payload["socializedAtomic"] != "1000000" {
		t.Fatalf("socialized %q, want 1000000", owner.Lines[0].Payload["socializedAtomic"])
	}
	// The write-off IS the terminal state: no settlement gap. The rest are gaps.
	if got := gapStages(owner.Gaps); !reflect.DeepEqual(got, []string{"funding", "advance", "delivery"}) {
		t.Fatalf("gap stages %v, want [funding advance delivery]", got)
	}
	if owner.Totals.FundedAtomic != "" {
		t.Fatalf("funded total %q — an absent record must stay absent, not become a number", owner.Totals.FundedAtomic)
	}
}

// Today's real demo-job state: nothing on chain at all, purchases approved but
// pending F2. Every stage is a marked gap with its cause; nothing is invented; the
// customer copy carries the same gaps in plain language without internal detail.
func TestG12_PartialInvoiceMarksEveryGapAndInventsNothing(t *testing.T) {
	st := openStore(t)

	set, err := newAgent(st).Invoice(context.Background(), Request{
		JobID:      "job_demo",
		VaultJobID: "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Provenance: []envelope.SourceProvenance{{
			Resource: "GET /v1/benchmark", Merchant: "benchmark-data.example",
			AmountAtomic: "60000", Status: "pending-integration",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := set.Owner
	if owner.Status != StatusPartial || len(owner.Lines) != 0 {
		t.Fatalf("empty chain: status %q lines %d, want partial and none", owner.Status, len(owner.Lines))
	}
	if owner.Totals != (Totals{}) {
		t.Fatalf("totals %+v — every field must stay empty when no chain rows exist", owner.Totals)
	}
	want := []string{"funding", "advance", "expenses", "delivery", "settlement"}
	if got := gapStages(owner.Gaps); !reflect.DeepEqual(got, want) {
		t.Fatalf("gap stages %v, want %v", got, want)
	}
	// The daemon-approved purchase that moved no money is a pending join outcome, not a line.
	if len(set.Reconciliation.Outcomes) != 1 || set.Reconciliation.Outcomes[0].Outcome != OutcomePendingSettlement {
		t.Fatalf("outcomes %+v, want one %q", set.Reconciliation.Outcomes, OutcomePendingSettlement)
	}
	// Owner copy names the internal cause; the customer copy gets plain language only.
	for i, g := range owner.Gaps {
		if g.Detail == "" {
			t.Fatalf("owner gap %q lost its internal detail", g.Stage)
		}
		if c := set.Customer.Gaps[i]; c.Detail != "" || c.Cause == "" {
			t.Fatalf("customer gap %q: detail %q cause %q — want plain cause, no internals", c.Stage, c.Detail, c.Cause)
		}
	}
}

// A divergence between the event-derived totals and Anandan's projection is FLAGGED
// carrying both values — never silently resolved toward either source (A4's posture).
func TestG12_ProjectionDivergenceIsFlaggedNotSilentlyResolved(t *testing.T) {
	st := openStore(t)
	jobC := "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc0001"
	seedChainEvent(t, st, 100, 0, "0x01", "JobFunded", jobC, map[string]string{"amountAtomic": "25000000"})
	if _, err := st.DB().Exec(
		`UPDATE chain_job_financials SET funded_amount_atomic = '24999999' WHERE job_id = ?`, jobC); err != nil {
		t.Fatal(err)
	}

	set, err := newAgent(st).Invoice(context.Background(), Request{JobID: "job_c", VaultJobID: jobC})
	if err != nil {
		t.Fatal(err)
	}
	div := alertsOfKind(set.Alerts, AlertProjectionDivergence)
	if len(div) != 1 {
		t.Fatalf("alerts %+v, want exactly one %s", set.Alerts, AlertProjectionDivergence)
	}
	d := div[0].Data
	if d["field"] != "funded" || d["fromEvents"] != "25000000" || d["fromProjection"] != "24999999" {
		t.Fatalf("divergence alert must carry BOTH values: %+v", d)
	}
	// Lines and totals stay event-derived — the alert reports, it does not pick a winner.
	if set.Owner.Totals.FundedAtomic != "25000000" {
		t.Fatalf("funded total %q, want the event-derived 25000000", set.Owner.Totals.FundedAtomic)
	}
}

// The unaccounted case, decided loud: a chain expense the daemon never proposed is an
// OUTSIDE-POLICY alert by default — everything here exists to make money unable to move
// without passing the policy engine, so an unattributable spend is never a quiet enum.
// The KnownReceipt seam (wired to the daemon's durable receipt record once F2 provides
// one) is the cheap distinguisher that downgrades it to bookkeeping.
func TestG12_UnaccountedExpenseIsLoudUnlessDaemonRecordKnowsIt(t *testing.T) {
	st := openStore(t)
	jobD := "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd0001"
	hashA := "0x1111111111111111111111111111111111111111111111111111111111111111"
	hashB := "0x2222222222222222222222222222222222222222222222222222222222222222"
	seedChainEvent(t, st, 100, 0, "0x02", "ExpenseRecorded", jobD, map[string]string{"amountAtomic": "40000", "receiptHash": hashA})
	seedChainEvent(t, st, 101, 0, "0x03", "ExpenseRecorded", jobD, map[string]string{"amountAtomic": "70000", "receiptHash": hashB})

	prov := []envelope.SourceProvenance{
		{Resource: "GET /v1/benchmark", AmountAtomic: "40000", ReceiptHash: hashA, Status: "proven"},
		{Resource: "GET /v1/extra", AmountAtomic: "60000", Status: "pending-integration"},
	}
	req := Request{JobID: "job_d", VaultJobID: jobD, Provenance: prov}

	// Seam unwired (today's state): hashB is loud.
	set, err := newAgent(st).Invoice(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]string{}
	for _, o := range set.Reconciliation.Outcomes {
		byHash[o.ReceiptHash] = o.Outcome
	}
	if byHash[hashA] != OutcomeMatched || byHash[""] != OutcomePendingSettlement || byHash[hashB] != OutcomeOutsidePolicy {
		t.Fatalf("outcomes %+v", set.Reconciliation.Outcomes)
	}
	loud := alertsOfKind(set.Alerts, AlertExpenseOutsidePolicy)
	if len(loud) != 1 || loud[0].Data["receiptHash"] != hashB {
		t.Fatalf("alerts %+v, want one outside-policy alert naming %s", set.Alerts, hashB)
	}

	// Seam wired (F2's durable receipt record): the same row is bookkeeping, not an alarm.
	a := newAgent(st)
	a.KnownReceipt = func(h string) bool { return h == hashB }
	set2, err := a.Invoice(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	byHash2 := map[string]string{}
	for _, o := range set2.Reconciliation.Outcomes {
		byHash2[o.ReceiptHash] = o.Outcome
	}
	if byHash2[hashB] != OutcomeRecordedNotAttributed {
		t.Fatalf("known receipt outcome %q, want %q", byHash2[hashB], OutcomeRecordedNotAttributed)
	}
	if left := alertsOfKind(set2.Alerts, AlertExpenseOutsidePolicy); len(left) != 0 {
		t.Fatalf("known receipt must not alarm: %+v", left)
	}
}

// FR-BRN-005 structurally: per-job memory contributes labels and NEVER amounts — hostile
// label text changes no number. And the formatter is deterministic: same inputs, same
// invoice, byte for byte.
func TestG12_LabelsNeverAmountsAndOutputDeterministic(t *testing.T) {
	st := openStore(t)
	indexFixture(t, st)
	base := Request{JobID: "job_demo", VaultJobID: jobA}

	hostile := base
	hostile.Labels = Labels{Title: "$999,999.00 due immediately", CustomerRef: "acme"}
	s1, err := newAgent(st).Invoice(context.Background(), hostile)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := newAgent(st).Invoice(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1.Owner.Lines, s2.Owner.Lines) || s1.Owner.Totals != s2.Owner.Totals {
		t.Fatal("labels changed a number — FR-BRN-005 violated")
	}

	s3, err := newAgent(st).Invoice(context.Background(), hostile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1, s3) {
		t.Fatal("same inputs produced different invoices — the formatter must be deterministic")
	}
}

func lineKinds(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Kind)
	}
	return out
}
