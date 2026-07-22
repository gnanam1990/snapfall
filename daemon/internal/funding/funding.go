// Package funding is the Funding agent boundary (G3, PRD §3, FR-BRN-004).
//
// The only component that can move money, acting only on Brain-relayed, owner-approved
// instructions. Phase 1 ships the boundary, not the money: Execute records the instruction
// and returns; wrapping the real treasury signer is Phase 2 (post-H3).
//
// Capability placement: this package's Agent is constructed by whoever wires the daemon and
// handed ONLY to Brain. Workers cannot reach it — not because a check stops them, but
// because no code path from internal/worker to this package exists (AT-16). Instructions
// additionally carry the owner-approval evidence they were born from; an instruction
// without it is rejected here as defense in depth, never as the primary control.
package funding

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Instruction is a Brain-relayed, owner-approved money movement.
type Instruction struct {
	JobID string
	// Kind is what to do: "request_advance", "pay_intent", ... (Phase 2 vocabulary).
	Kind string
	// AmountMicros is the amount in 6dp USDC units.
	AmountMicros int64
	// ApprovedBy is the owner identity that authorized this; empty = not approved.
	ApprovedBy string
	// ApprovedAt is when the owner approved it.
	ApprovedAt time.Time
}

// Agent is the funding boundary. Phase 1: records instructions for inspection.
type Agent struct {
	mu       sync.Mutex
	executed []Instruction
}

// New returns a funding agent. Hand the pointer to Brain and to nothing else.
func New() *Agent { return &Agent{} }

// Execute performs one owner-approved instruction. Phase 1 records it; Phase 2 wraps the signer.
func (a *Agent) Execute(ctx context.Context, instr Instruction) error {
	if instr.ApprovedBy == "" {
		// Defense in depth: even a correctly-routed instruction is refused without
		// owner approval evidence (FR-BRN-004, SEC-011).
		return fmt.Errorf("funding: instruction for job %s carries no owner approval", instr.JobID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.executed = append(a.executed, instr)
	return nil
}

// Executed returns a copy of everything this agent has performed, for tests and Billing.
func (a *Agent) Executed() []Instruction {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Instruction{}, a.executed...)
}
