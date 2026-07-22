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

// Deliverable is the structured draft a worker submits for QA review.
type Deliverable struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Claims  []Claim  `json:"claims"`
	Sources []string `json:"sources"`
}

// QAVerdict is the QA-worker's review result (payload of TypeQAVerdict).
//
// HONESTY CONTRACT (same discipline as the compliance step, PRD §5.1): a verdict is
// EVIDENCE OF REVIEW, NOT A GUARANTEE. QA can produce false negatives — an unsupported
// claim it fails to catch still ships. Disclaimer carries that sentence verbatim and
// every surface that renders a verdict must show it.
type QAVerdict struct {
	Passed     bool     `json:"passed"`
	Reasons    []string `json:"reasons"`
	Checked    int      `json:"checked_claims"`
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
