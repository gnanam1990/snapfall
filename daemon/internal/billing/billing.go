// Package billing is the G12 read-side invoice formatter: deterministic owner and
// customer invoices built exclusively from on-chain records.
//
// FR-BRN-005, held strictly: every amount on an invoice comes from a chain row
// (chain_events, cross-checked against Anandan's chain_job_financials projection) and
// nothing else. Per-job memory contributes display labels ONLY — never a number. A
// lifecycle stage with no on-chain record appears as an explicit Gap naming its cause;
// an absent record stays absent (empty string), never a fabricated zero, and the
// invoice status says "partial" rather than refusing to exist.
//
// Honest ceiling (decided before the build, not discovered at a STOP):
//
//   - G12's done-criterion — "invoice totals reconcile to chain to the cent on a real
//     spine run" — is blocked on the unowned chain gap (no deployment, no advance call,
//     no settlement call). The buildable-today proxy is the golden fixture run through
//     Anandan's own decode+project pipeline: synthetic input, his code path, his exact
//     schema — not a testnet run.
//   - The receiptHash join is designable and tested with synthetic data on both sides,
//     but end to end the provenance side is empty until F2 lands a real payer.
//   - Nothing writes jobs.vault_job_id, so Request.VaultJobID has no producer for real
//     jobs until on-chain job creation exists (a face of the same chain gap).
//
// The unaccounted case, decided LOUD: a chain expense with no daemon-side record is
// OutcomeOutsidePolicy plus an Alert — everything in this architecture exists to make
// money unable to move without passing the policy engine, so an unattributable spend is
// never a quiet enum value. The daemon has no purchase record independent of provenance
// today (the F2 stub's pending_settlement event carries no receipt hash), so the cheap
// distinguisher does not exist yet; the KnownReceipt seam is designed now so that the
// day F2's durable receipt record exists, wiring it downgrades known rows to
// OutcomeRecordedNotAttributed (indexer-timing/bookkeeping) while unknown rows stay loud.
//
// Copy serving is NOT decided here (an H2 seam, flagged for standup): two copies exist,
// but which reader is served which — Vasanth's layer scoping by magic link server-side,
// or a customer-scoped auth path in the daemon — is open, and both sides assuming the
// other handled it is the exact shape of every gap found this week. Until it is decided,
// nothing in the daemon serves the customer copy at all.
package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

const (
	StatusComplete = "complete"
	StatusPartial  = "partial — awaiting chain records"

	CopyOwner    = "owner"
	CopyCustomer = "customer"

	// Join outcomes, one per provenance entry or unmatched chain expense.
	OutcomeMatched               = "matched"                 // provenance receiptHash == chain row
	OutcomePendingSettlement     = "pending-settlement"      // daemon-side record, no chain row (F2 pending)
	OutcomeRecordedNotAttributed = "recorded-not-attributed" // chain row the daemon's durable record knows — bookkeeping
	OutcomeOutsidePolicy         = "outside-policy"          // chain row nobody can vouch for — LOUD

	AlertProjectionDivergence = "projection-divergence"
	AlertExpenseOutsidePolicy = "expense-outside-policy"

	// Disclaimer is the invoice-level honesty line, on every copy.
	Disclaimer = "Amounts are drawn exclusively from on-chain records; stages listed as gaps have no on-chain record yet and are not billed."
)

// Labels is the per-job memory contribution: display context ONLY, never amounts.
type Labels struct {
	Title       string `json:"title,omitempty"`
	CustomerRef string `json:"customerRef,omitempty"`
}

// Request names the job to invoice. VaultJobID is the bytes32 chain entity — the
// identity join with no producer for real jobs yet (see the package doc's ceiling).
// Provenance is the daemon-side purchase record the receiptHash join runs against.
type Request struct {
	JobID      string
	VaultJobID string
	Labels     Labels
	Provenance []envelope.SourceProvenance
}

// Line is one chain event, verbatim: Kind keeps the frozen H1 name (Billing renames
// nothing — the H2 precedent) and Payload is the projected payload untouched.
type Line struct {
	Kind        string            `json:"kind"`
	TxHash      string            `json:"txHash"`
	Block       uint64            `json:"block"`
	LogIndex    uint64            `json:"logIndex"`
	Payload     map[string]string `json:"payload"`
	ReceiptHash string            `json:"receiptHash,omitempty"` // ExpenseRecorded convenience — the join key
}

// Gap marks a lifecycle stage with no on-chain record. Cause is plain language and
// appears on every copy; Detail carries internal references and is owner-only.
type Gap struct {
	Stage  string `json:"stage"`
	Cause  string `json:"cause"`
	Detail string `json:"detail,omitempty"`
}

// Totals mirror chain_job_financials field for field so the cross-check is 1:1.
// An empty field means NO RECORD — absence is never rendered as zero.
type Totals struct {
	FundedAtomic                  string `json:"fundedAtomic,omitempty"`
	AdvancePrincipalAtomic        string `json:"advancePrincipalAtomic,omitempty"`
	AdvanceFeeAtomic              string `json:"advanceFeeAtomic,omitempty"`
	ExpenseTotalAtomic            string `json:"expenseTotalAtomic,omitempty"`
	SettlementAdvanceRepaidAtomic string `json:"settlementAdvanceRepaidAtomic,omitempty"`
	OperatorNetAtomic             string `json:"operatorNetAtomic,omitempty"`
}

type Invoice struct {
	Copy         string    `json:"copy"`
	JobID        string    `json:"jobId"`
	VaultJobID   string    `json:"vaultJobId"`
	Status       string    `json:"status"`
	Labels       Labels    `json:"labels"`
	Lines        []Line    `json:"lines"`
	Gaps         []Gap     `json:"gaps"`
	Totals       Totals    `json:"totals"`
	DeliveryHash string    `json:"deliveryHash,omitempty"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Disclaimer   string    `json:"disclaimer"`
}

// JoinOutcome is one row of the provenance<->chain receiptHash reconciliation.
type JoinOutcome struct {
	ReceiptHash string `json:"receiptHash,omitempty"`
	Resource    string `json:"resource,omitempty"` // provenance-side context, "" for chain-only rows
	Outcome     string `json:"outcome"`
}

type Reconciliation struct {
	Outcomes []JoinOutcome `json:"outcomes"`
}

// Alert is a finding that must not be ignorable: projection divergence or an
// outside-policy expense. Alerts live on the Set (the owner surface), never on the
// customer copy.
type Alert struct {
	Kind    string            `json:"kind"`
	Message string            `json:"message"`
	Data    map[string]string `json:"data"`
}

// Set is one invoicing run: both copies sharing the same chain truth, the receiptHash
// reconciliation, and any alerts.
type Set struct {
	Owner          Invoice        `json:"owner"`
	Customer       Invoice        `json:"customer"`
	Reconciliation Reconciliation `json:"reconciliation"`
	Alerts         []Alert        `json:"alerts,omitempty"`
}

// Agent formats invoices. It is constructed at wiring and handed to Brain alone;
// the import-graph law tests in boundary_test.go pin what it can and cannot reach.
type Agent struct {
	st      *store.Store
	chainID uint64
	now     func() time.Time

	// KnownReceipt reports whether the daemon's own durable purchase record contains a
	// receipt hash. Nil today — no such record exists until F2's real payer — so every
	// unmatched chain expense stays OutcomeOutsidePolicy (loud by design).
	KnownReceipt func(receiptHash string) bool
}

func New(st *store.Store, chainID uint64, now func() time.Time) *Agent {
	if now == nil {
		now = time.Now
	}
	return &Agent{st: st, chainID: chainID, now: now}
}

// Invoice builds both copies for one job from its chain record.
func (a *Agent) Invoice(ctx context.Context, req Request) (Set, error) {
	lines, err := a.chainLines(ctx, req.VaultJobID)
	if err != nil {
		return Set{}, err
	}
	totals := computeTotals(lines)
	alerts, err := a.crossCheck(ctx, req.VaultJobID, totals)
	if err != nil {
		return Set{}, err
	}
	rec, joinAlerts := a.reconcile(lines, req.Provenance)
	alerts = append(alerts, joinAlerts...)
	gaps := gapsFor(lines, rec)

	status := StatusComplete
	if len(gaps) > 0 {
		status = StatusPartial
	}
	owner := Invoice{
		Copy: CopyOwner, JobID: req.JobID, VaultJobID: req.VaultJobID, Status: status,
		Labels: req.Labels, Lines: lines, Gaps: gaps, Totals: totals,
		DeliveryHash: deliveryHash(lines), GeneratedAt: a.now(), Disclaimer: Disclaimer,
	}
	customer := owner
	customer.Copy = CopyCustomer
	customer.Gaps = make([]Gap, len(gaps))
	for i, g := range gaps {
		customer.Gaps[i] = Gap{Stage: g.Stage, Cause: g.Cause} // plain language, no internals
	}
	return Set{Owner: owner, Customer: customer, Reconciliation: rec, Alerts: alerts}, nil
}

func (a *Agent) chainLines(ctx context.Context, vaultJobID string) ([]Line, error) {
	rows, err := a.st.DB().QueryContext(ctx, `
		SELECT transaction_hash, log_index, block_number, kind, payload_json
		FROM chain_events
		WHERE chain_id = ? AND entity_id = ?
		ORDER BY block_number, log_index`, a.chainID, vaultJobID)
	if err != nil {
		return nil, fmt.Errorf("chain_events: %w", err)
	}
	defer rows.Close()

	var lines []Line
	for rows.Next() {
		var l Line
		var payloadJSON string
		if err := rows.Scan(&l.TxHash, &l.LogIndex, &l.Block, &l.Kind, &payloadJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payloadJSON), &l.Payload); err != nil {
			return nil, fmt.Errorf("payload of %s/%d: %w", l.TxHash, l.LogIndex, err)
		}
		if l.Kind == "ExpenseRecorded" {
			l.ReceiptHash = l.Payload["receiptHash"]
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

func computeTotals(lines []Line) Totals {
	var t Totals
	expenses := new(big.Int)
	sawExpense := false
	for _, l := range lines {
		switch l.Kind {
		case "JobFunded":
			t.FundedAtomic = l.Payload["amountAtomic"]
		case "AdvanceIssued":
			t.AdvancePrincipalAtomic = l.Payload["principalAtomic"]
			t.AdvanceFeeAtomic = l.Payload["feeAtomic"]
		case "ExpenseRecorded":
			if v, ok := new(big.Int).SetString(l.Payload["amountAtomic"], 10); ok {
				expenses.Add(expenses, v)
				sawExpense = true
			}
		case "JobSettled":
			t.SettlementAdvanceRepaidAtomic = l.Payload["advanceRepaidAtomic"]
			t.OperatorNetAtomic = l.Payload["operatorNetAtomic"]
		}
	}
	if sawExpense {
		t.ExpenseTotalAtomic = expenses.String()
	}
	return t
}

// crossCheck compares the event-derived totals against Anandan's chain_job_financials
// projection field by field. A divergence is FLAGGED carrying both values — the invoice
// stays event-derived and the alert picks no winner (A4's posture, applied here).
func (a *Agent) crossCheck(ctx context.Context, vaultJobID string, t Totals) ([]Alert, error) {
	var funded, principal, fee, settled, net sql.NullString
	var expenseTotal string
	err := a.st.DB().QueryRowContext(ctx, `
		SELECT funded_amount_atomic, advance_principal_atomic, advance_fee_atomic,
		       expense_total_atomic, settlement_advance_repaid_atomic, operator_net_atomic
		FROM chain_job_financials WHERE chain_id = ? AND job_id = ?`,
		a.chainID, vaultJobID).Scan(&funded, &principal, &fee, &expenseTotal, &settled, &net)
	if err == sql.ErrNoRows {
		return nil, nil // no chain presence at all: the gaps say so; nothing to cross-check
	}
	if err != nil {
		return nil, fmt.Errorf("chain_job_financials: %w", err)
	}

	var alerts []Alert
	check := func(field, fromEvents, fromProjection string) {
		// The projection's zero-default and an absent event total agree: no record, no expense.
		if field == "expenses" && fromEvents == "" && fromProjection == "0" {
			return
		}
		if fromEvents != fromProjection {
			alerts = append(alerts, Alert{
				Kind: AlertProjectionDivergence,
				Message: fmt.Sprintf("%s: chain_events say %q, chain_job_financials say %q — reporting both, resolving neither",
					field, fromEvents, fromProjection),
				Data: map[string]string{"field": field, "fromEvents": fromEvents, "fromProjection": fromProjection},
			})
		}
	}
	check("funded", t.FundedAtomic, funded.String)
	check("advance-principal", t.AdvancePrincipalAtomic, principal.String)
	check("advance-fee", t.AdvanceFeeAtomic, fee.String)
	check("expenses", t.ExpenseTotalAtomic, expenseTotal)
	check("settlement-advance-repaid", t.SettlementAdvanceRepaidAtomic, settled.String)
	check("operator-net", t.OperatorNetAtomic, net.String)
	return alerts, nil
}

// reconcile joins provenance to chain expenses by receiptHash EXACTLY (never
// jobId+amount). Unmatched chain expenses are loud unless the KnownReceipt seam vouches.
func (a *Agent) reconcile(lines []Line, prov []envelope.SourceProvenance) (Reconciliation, []Alert) {
	matched := map[string]bool{}
	onChain := map[string]bool{}
	for _, l := range lines {
		if l.Kind == "ExpenseRecorded" {
			onChain[l.ReceiptHash] = true
		}
	}

	var outcomes []JoinOutcome
	for _, p := range prov {
		if p.ReceiptHash != "" && onChain[p.ReceiptHash] {
			matched[p.ReceiptHash] = true
			outcomes = append(outcomes, JoinOutcome{ReceiptHash: p.ReceiptHash, Resource: p.Resource, Outcome: OutcomeMatched})
			continue
		}
		outcomes = append(outcomes, JoinOutcome{ReceiptHash: p.ReceiptHash, Resource: p.Resource, Outcome: OutcomePendingSettlement})
	}

	var alerts []Alert
	for _, l := range lines {
		if l.Kind != "ExpenseRecorded" || matched[l.ReceiptHash] {
			continue
		}
		if a.KnownReceipt != nil && a.KnownReceipt(l.ReceiptHash) {
			outcomes = append(outcomes, JoinOutcome{ReceiptHash: l.ReceiptHash, Outcome: OutcomeRecordedNotAttributed})
			continue
		}
		outcomes = append(outcomes, JoinOutcome{ReceiptHash: l.ReceiptHash, Outcome: OutcomeOutsidePolicy})
		alerts = append(alerts, Alert{
			Kind: AlertExpenseOutsidePolicy,
			Message: fmt.Sprintf("on-chain expense %s (%s atomic) has no daemon-side purchase record — money may have moved outside the policy gate",
				l.ReceiptHash, l.Payload["amountAtomic"]),
			Data: map[string]string{"receiptHash": l.ReceiptHash, "amountAtomic": l.Payload["amountAtomic"], "txHash": l.TxHash},
		})
	}
	return Reconciliation{Outcomes: outcomes}, alerts
}

// gapsFor marks every lifecycle stage with no on-chain record, in fixed stage order.
// The expense gap fires only when the daemon-side record shows purchases that never
// reached chain — a job with no purchases at all has no expense gap to mark.
func gapsFor(lines []Line, rec Reconciliation) []Gap {
	has := map[string]bool{}
	for _, l := range lines {
		has[l.Kind] = true
	}
	pendingPurchases := false
	for _, o := range rec.Outcomes {
		if o.Outcome == OutcomePendingSettlement {
			pendingPurchases = true
		}
	}

	var gaps []Gap
	if !has["JobFunded"] {
		gaps = append(gaps, Gap{Stage: "funding", Cause: "no on-chain funding record",
			Detail: "JobVault funding never executed — chain deployment + write path unassigned (standup item, 23 Jul)"})
	}
	if !has["AdvanceIssued"] {
		gaps = append(gaps, Gap{Stage: "advance", Cause: "no on-chain advance record",
			Detail: "FloatPool.requestAdvance never called — the snap depends on the unowned chain-write path"})
	}
	if pendingPurchases {
		gaps = append(gaps, Gap{Stage: "expenses", Cause: "purchases approved but not yet settled on chain",
			Detail: "daemon-approved purchases move no money until the F2 sidecar client lands (purchase.pending_settlement)"})
	}
	if !has["DeliverySubmitted"] {
		gaps = append(gaps, Gap{Stage: "delivery", Cause: "no on-chain delivery anchor",
			Detail: "delivery anchor never submitted — chain-write path unassigned"})
	}
	if !has["JobSettled"] && !has["AdvanceWrittenOff"] {
		gaps = append(gaps, Gap{Stage: "settlement", Cause: "no on-chain settlement",
			Detail: "settlement tx never produced — the fall (V9 Accept) depends on the unowned chain-write path"})
	}
	return gaps
}

func deliveryHash(lines []Line) string {
	for _, l := range lines {
		if l.Kind == "DeliverySubmitted" {
			return l.Payload["deliveryHash"]
		}
	}
	return ""
}
