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
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
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
	// The chain lanes (nil until wired): Funding holds the ONLY signer references in
	// the daemon — workers, Billing, and discovery cannot even name them.
	advanceLane Submitter
	settleLane  Submitter
}

// New returns a funding agent. Hand the pointer to Brain and to nothing else.
func New() *Agent { return &Agent{seen: make(map[string]bool)} }

// ChainOutcome is one submitted transaction's result, as Funding reports it upward.
// Reverted is a THIRD state — mined and failed — never conflated with success or with
// a submission error; callers surface it to the owner, never record it as done.
type ChainOutcome struct {
	Submitted bool // false = no chain writer wired (the honest pending_chain stop)
	TxHash    string
	Block     uint64
	GasUsed   uint64
	Reverted  bool
}

// Submitter is one signed submission lane (internal/chain.Client behind an interface
// so funding stays fake-testable). Each Submitter serializes its own key's
// submissions; Funding holds the ONLY references — no other package can sign.
type Submitter interface {
	Submit(ctx context.Context, calldata []byte) (ChainOutcome, error)
}

// SetChain wires the two submission lanes: the TREASURY lane signs requestAdvance
// (the operator org draws the advance) and the CUSTOMER lane signs acceptDelivery
// (SC-JV-005: only the customer settles — for the demo the customer wallet is
// DAEMON-CUSTODIAL, stated openly; production replaces this lane with the customer's
// own wallet). Either nil = that action stops honestly at *.pending_chain.
func (a *Agent) SetChain(treasuryAdvance, customerSettle Submitter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLane, a.settleLane = treasuryAdvance, customerSettle
}

// ExecuteAdvance is the ADVANCE door: it takes the approval-minted GRANT — never raw
// calldata, so no caller can aim this lane at an arbitrary contract call — performs
// the same dedupe-and-record as Execute, DERIVES the requestAdvance calldata from the
// grant's own ChainRef, and submits through the treasury lane. A grant with no chain
// ref or no wired lane returns Submitted=false: the caller records the honest pending
// stop.
func (a *Agent) ExecuteAdvance(ctx context.Context, g approval.Grant) (ChainOutcome, error) {
	if err := a.Execute(ctx, g); err != nil {
		return ChainOutcome{}, err
	}
	a.mu.Lock()
	lane := a.advanceLane
	a.mu.Unlock()
	ref := g.Intent().ChainRef
	if lane == nil || ref == "" {
		return ChainOutcome{Submitted: false}, nil
	}
	id, err := chain.JobID32(ref)
	if err != nil {
		return ChainOutcome{}, fmt.Errorf("advance chain ref: %w", err)
	}
	return lane.Submit(ctx, chain.CalldataRequestAdvance(id))
}

// SettleOnChain is the SETTLEMENT door: it takes only the vault job id and derives the
// acceptDelivery calldata itself. Its authorization is upstream (the customer's
// per-job credential + delivery-ready state + freeze + exactly-once claim in Brain)
// and ON-CHAIN (SC-JV-005: the contract itself refuses unless msg.sender is the
// job's customer and status is Delivered — a misdirected call reverts, it cannot
// misappropriate). Called from Brain's AcceptDelivery only.
func (a *Agent) SettleOnChain(ctx context.Context, vaultJobID string) (ChainOutcome, error) {
	a.mu.Lock()
	lane := a.settleLane
	a.mu.Unlock()
	if lane == nil || vaultJobID == "" {
		return ChainOutcome{Submitted: false}, nil
	}
	id, err := chain.JobID32(vaultJobID)
	if err != nil {
		return ChainOutcome{}, fmt.Errorf("settlement chain ref: %w", err)
	}
	return lane.Submit(ctx, chain.CalldataAcceptDelivery(id))
}

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
	kind := "pay_intent"
	if in.Kind != "" && in.Kind != "payment" {
		// The Phase-2 vocabulary arrives: an advance-kind Grant records the advance
		// instruction (request_advance) rather than a payment.
		kind = "request_" + in.Kind
	}
	a.executed = append(a.executed, Instruction{
		JobID:        in.JobID,
		Kind:         kind,
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
