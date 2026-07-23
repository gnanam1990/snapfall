// Package envelope defines the one message shape that exists in the system (G3, PRD §3).
//
// THE LAW: no agent ever talks to another agent directly. Every message is Agent → Brain,
// and Brain decides what happens next. This package holds only the shared vocabulary —
// it imports nothing from the rest of the daemon, so depending on it grants no capability.
package envelope

import (
	"encoding/json"
	"fmt"
	"time"
)

// Role identifies a participant in the loop. Four fixed roles plus the owner (PRD §3).
type Role string

const (
	RoleOwner   Role = "owner"
	RoleBrain   Role = "brain"
	RoleWorker  Role = "worker"
	RoleFunding Role = "funding"
	RoleBilling Role = "billing"
)

// Type is the message kind, namespaced by origin role.
type Type string

const (
	// Owner → Brain
	TypeOwnerRequest Type = "owner.request"
	TypeOwnerConfirm Type = "owner.confirm"
	TypeOwnerReject  Type = "owner.reject"

	// Brain → Owner
	TypeScopeProposal Type = "brain.scope_proposal"
	TypeJobUpdate     Type = "brain.job_update"
	TypeJobReport     Type = "brain.job_report"

	// Brain → Worker
	TypeAssignment Type = "brain.assignment"

	// Worker → Brain
	TypeWorkerReport   Type = "worker.report"
	TypeWorkerProgress Type = "worker.progress"
	TypeWorkerFailure  Type = "worker.failure"

	// QA-worker → Brain (G9). A distinct type from TypeWorkerReport: a draft and a
	// verdict are different speech acts, and the DeliveryReady transition listens
	// only to this one.
	TypeQAVerdict Type = "worker.qa_verdict"
)

// ── G9 shared vocabulary. Lives here so workers and QA can both name these
//    without depending on each other (THE LAW's import shape). ──

// Claim is one assertion in a deliverable, with the sources backing it.
// An empty Sources list is exactly what QA's unsupported-claim check hunts.
type Claim struct {
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
}

// Deliverable is the structured draft a worker submits for QA review. It is consumed by
// the QA worker, rendered in the customer portal, and cross-referenced by Billing. The G8
// additions are all additive (older consumers ignore them).
type Deliverable struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Claims  []Claim  `json:"claims"`
	Sources []string `json:"sources"`

	// ── G8 additions ──
	// Compliance is the FR-CMP-001 screen result — REQUIRED on a finished report so a
	// deliverable can never silently omit the screen. `Stub:true` renders visibly.
	Compliance *ComplianceResult `json:"compliance,omitempty"`
	// Provenance is one entry per source the worker bought — carries the on-chain
	// receiptHash so Billing joins EXACTLY, and `Status` encodes proven-vs-pending honesty.
	Provenance []SourceProvenance `json:"provenance,omitempty"`
	// Disclaimer is the report-level "evidence, not a guarantee" honesty line.
	Disclaimer string `json:"disclaimer,omitempty"`
}

// ComplianceResult is the FR-CMP-001 screen wrapped with confidence and the honesty
// disclaimer. Never a fabricated screen: when no real engine ran, Stub is true and every
// surface renders "STUB — not a real screen".
type ComplianceResult struct {
	Decision   string `json:"decision"`   // "clear" | "review" | "hit"
	Confidence string `json:"confidence"` // "low" | "medium" | "high" — never rendered as certainty
	Provider   string `json:"provider"`   // "circle-compliance-engine" | "stub"
	Stub       bool   `json:"stub"`       // true when no real screen ran — MUST render visibly
	Disclaimer string `json:"disclaimer"` // "evidence, not a guarantee" verbatim
}

// SourceProvenance is what one purchased source leaves behind. ReceiptHash is the
// JobVault.recordExpense(bytes32 receiptHash) value, so Billing joins provenance ->
// chain_events(ExpenseRecorded).receiptHash EXACTLY, not by jobId+amount (fragile the
// moment two purchases share an amount).
type SourceProvenance struct {
	Resource     string `json:"resource"`
	Merchant     string `json:"merchant"`              // payTo (the F4 merchant-identity seam)
	AmountAtomic string `json:"amountAtomic"`          // 6dp atomic USDC
	ReceiptHash  string `json:"receiptHash,omitempty"` // bytes32 0x-hex; the exact Billing join key
	PaymentID    string `json:"paymentId,omitempty"`   // sidecar paymentId once real
	Status       string `json:"status"`                // "proven" | "pending-integration"
}

// QAVerdict is the QA-worker's review result (payload of TypeQAVerdict).
//
// HONESTY CONTRACT (same discipline as the compliance step, PRD §5.1): a verdict is
// EVIDENCE OF REVIEW, NOT A GUARANTEE. QA can produce false negatives — an unsupported
// claim it fails to catch still ships. Disclaimer carries that sentence verbatim and
// every surface that renders a verdict must show it.
type QAVerdict struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
	Checked int      `json:"checked_claims"`
	// Notes are NON-BLOCKING observations QA surfaces without bouncing — e.g. a stubbed
	// compliance screen or provenance still pending payment-path integration. These are
	// honestly-disclosed provisional states, not defects: bouncing on them would block the
	// demo until Circle credentials + the sidecar client land. Surfaced, never silently
	// ignored (G8 QA decision: pass-with-a-visible-note).
	Notes      []string `json:"notes,omitempty"`
	Disclaimer string   `json:"disclaimer"`
}

// Envelope is the message. Everything that moves between roles moves in one of these.
type Envelope struct {
	JobID   string          `json:"job_id"`
	From    Role            `json:"from"`
	Type    Type            `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	SentAt  time.Time       `json:"sent_at"`
}

// New builds an envelope with the payload marshalled and the timestamp stamped.
func New(jobID string, from Role, typ Type, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshalling %s payload: %w", typ, err)
	}
	return Envelope{JobID: jobID, From: from, Type: typ, Payload: raw, SentAt: time.Now().UTC()}, nil
}

// Decode unmarshals the payload into out.
func (e Envelope) Decode(out any) error {
	if err := json.Unmarshal(e.Payload, out); err != nil {
		return fmt.Errorf("decoding %s payload: %w", e.Type, err)
	}
	return nil
}
