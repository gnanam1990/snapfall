// Package funding is the Funding agent boundary (G3, PRD §3, FR-BRN-004).
//
// The only component that can move money, acting only on Brain-relayed, owner-approved
// instructions. Phase 1 ships the boundary, not the money: Execute records the instruction
// and returns; wrapping the real treasury signer is Phase 2 (post-H3).
//
// Capability placement, layered (the Step-6 closure of the Grant question):
//
//  1. Workers cannot reach this package at all — no import path exists (AT-16).
//  2. The SOLE mutating entry point, Execute, demands an approval.Grant — the
//     capability the G7 lifecycle mints only after every gate (hash, state, expiry,
//     policy version, exactly-once) has passed. A Grant forged outside the approval
//     package is empty and refused here. The wiring layer cannot invoke funding
//     "without any Grant existing" because there is nothing else Execute accepts,
//     and it cannot lie about amounts or approver: every value is READ FROM the
//     grant, never supplied alongside it.
//
// Note the deliberate trade, made at Step-6 review: funding now imports the approval
// package (type vocabulary for the credential), which transitively reaches policy. The
// old import-graph proof "funding cannot name a policy.Decision" is superseded by the
// stronger type-level property: nameable or not, a Decision has no door — the only
// door demands the unforgeable Grant. boundary tests pin the method set and the
// empty-grant refusal.
package funding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
)

// Instruction is the RECORD of an executed movement — derived inside Execute from the
// grant, never accepted from a caller. Billing and tests read these.
type Instruction struct {
	JobID        string
	Kind         string
	AmountMicros int64
	Merchant     string
	RequestID    string
	ApprovedAt   time.Time
}

// Agent is the funding boundary. Phase 1: records instructions for inspection.
type Agent struct {
	mu       sync.Mutex
	executed []Instruction
	// seen dedups by RequestID: a callback retaining a valid Grant must not be able to
	// replay it into multiple movements (review-batch fix — belt-and-suspenders atop the
	// lifecycle's exactly-once; a real signer would make this durable).
	seen map[string]bool
}

// New returns a funding agent. Hand the pointer to Brain and to nothing else.
func New() *Agent { return &Agent{seen: make(map[string]bool)} }

// Execute performs one approved movement. Phase 2 records it; the real signer wrap
// lands with H3. The ONLY input is the approval-minted Grant: a forged (empty) grant
// is refused, and every recorded value derives from the grant itself.
func (a *Agent) Execute(ctx context.Context, g approval.Grant) error {
	if g.Empty() {
		return fmt.Errorf("funding: refused — grant is empty (forged outside the approval lifecycle); FR-BRN-004")
	}
	in := g.Intent()
	a.mu.Lock()
	defer a.mu.Unlock()
	// One movement per approval, even if a caller replays the same Grant.
	if a.seen[g.RequestID()] {
		return fmt.Errorf("funding: refused — grant for request %s already executed (replay)", g.RequestID())
	}
	a.seen[g.RequestID()] = true
	a.executed = append(a.executed, Instruction{
		JobID:        in.JobID,
		Kind:         "pay_intent",
		AmountMicros: in.AmountMicros,
		Merchant:     in.Merchant,
		RequestID:    g.RequestID(),
		ApprovedAt:   g.GrantedAt(),
	})
	return nil
}

// Executed returns a copy of everything this agent has performed, for tests and Billing.
func (a *Agent) Executed() []Instruction {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Instruction{}, a.executed...)
}
