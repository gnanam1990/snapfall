package brain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The customer accept credential — the daemon-side half of the settlement
// authorization (the fall). The SETTLEMENT PRINCIPAL IS THE CUSTOMER, not the owner:
// their Accept is what releases their escrow, so the credential that authenticates it
// is per-job, minted for the magic link, and entirely separate from the owner token —
// an owner bearer must not open customer routes, nor the reverse.
//
// Lifecycle: the owner mints a credential for a delivery-ready job (H2, owner-gated)
// and hands it to the customer as the magic link. The daemon stores ONLY the SHA-256
// of the token — the plaintext exists once, in the mint response, and can never be
// recovered from the daemon (a test pins its absence from memory files and events).
// Re-minting rotates: the old credential dies with its hash.
//
// Accept authorization chain, in order: credential (per request, constant-time) →
// delivery-ready state → freeze gate (AT-09 discipline: the kill switch stops
// settlements like it stops payments) → exactly-once claim → the chain call. The
// chain call is an HONEST STOP today: no deployment exists, so acceptance is recorded
// durably as settlement.pending_chain — the same shape as purchase.pending_settlement
// — and the on-chain acceptDelivery becomes one call away when the write path lands.

// ErrNotDeliveryReady refuses minting or accepting for a job not in delivery_ready.
var ErrNotDeliveryReady = errors.New("job is not delivery-ready")

// ErrNoCredential means no accept credential was ever minted for the job.
var ErrNoCredential = errors.New("no accept credential minted for this job")

// ErrFrozen wraps the kill switch's refusal so surfaces can map it without matching
// on message text.
var ErrFrozen = errors.New("frozen")

// MintAcceptCredential creates (or ROTATES) the per-job customer credential. Only a
// delivery-ready or already-accepted job can be minted for — there is nothing to
// accept earlier. Returns the plaintext token exactly once; only its hash is stored.
func (b *Brain) MintAcceptCredential(ctx context.Context, jobID string) (string, error) {
	b.mu.Lock()
	js, ok := b.jobs[jobID]
	stage := JobStage("")
	if ok {
		stage = js.Stage
	}
	b.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown job %s", jobID)
	}
	if stage != StageDeliveryReady && stage != StageAccepted {
		return "", fmt.Errorf("%w: stage %s", ErrNotDeliveryReady, stage)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "act_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	if err := b.memory.Update(jobID, func(jm *JobMemory) {
		jm.AcceptTokenHash = hex.EncodeToString(sum[:])
	}); err != nil {
		return "", err
	}
	// The record says a credential exists — never what it is.
	if _, err := b.store.Append(ctx, store.Event{
		Kind: "delivery.accept_credential_minted", EntityID: jobID, Actor: "owner",
		Payload: map[string]any{"note": "customer accept credential minted (rotates any prior)"},
	}); err != nil {
		return "", err
	}
	return token, nil
}

// VerifyAcceptCredential reports whether token is the job's current credential —
// constant-time over the stored hash. No credential minted = nothing verifies.
func (b *Brain) VerifyAcceptCredential(jobID, token string) bool {
	jm, err := b.memory.Get(jobID)
	if err != nil || jm.AcceptTokenHash == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(jm.AcceptTokenHash)) == 1
}

// AcceptDelivery is the customer's Accept, AFTER authentication (the endpoint verifies
// the credential; this method owns state, freeze, exactly-once, and the honest chain
// stop). Idempotent: accepting an accepted job returns its state without a second
// record (G7's same-decision precedent). Returns the resulting state label.
func (b *Brain) AcceptDelivery(ctx context.Context, jobID string) (string, error) {
	// Freeze gate first: the kill switch stops settlements like it stops payments.
	if err := b.frozenErr(jobID, ""); err != nil {
		return "", fmt.Errorf("%w: %s", ErrFrozen, err)
	}

	// Exactly-once claim under the lock: delivery_ready -> accepted is the transition;
	// a concurrent second Accept sees StageAccepted and takes the idempotent path.
	b.mu.Lock()
	js, ok := b.jobs[jobID]
	if !ok {
		b.mu.Unlock()
		return "", fmt.Errorf("unknown job %s", jobID)
	}
	switch js.Stage {
	case StageAccepted:
		b.mu.Unlock()
		return "accepted-pending-chain", nil
	case StageDeliveryReady:
		js.Stage = StageAccepted // the claim
	default:
		stage := js.Stage
		b.mu.Unlock()
		return "", fmt.Errorf("%w: stage %s", ErrNotDeliveryReady, stage)
	}
	b.mu.Unlock()

	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.Stage = string(StageAccepted) }); err != nil {
		return "", err
	}
	state, err := b.settleOnChain(ctx, jobID)
	if err != nil {
		return state, err
	}
	if state == "accepted-settled" {
		if err := b.observeMilestoneCompletion(ctx, jobID); err != nil {
			// Settlement already committed on chain. Observation failure is an alert,
			// never grounds to misreport the actual settlement as failed.
			_, _ = b.store.Append(context.WithoutCancel(ctx), store.Event{
				Kind: "pipeline.milestone.observation_failed", EntityID: jobID, Actor: "brain",
				Payload: map[string]any{"error": err.Error()},
			})
			b.log.Warn("milestone settled but chain observation failed", "job", jobID, "err", err)
		}
	}
	return state, nil
}

// settleOnChain is the chain half of an authenticated, claimed Accept: submit
// JobVault.acceptDelivery through Funding's CUSTOMER lane (SC-JV-005 — only the
// customer settles; the demo lane is daemon-custodial, stated openly) and record the
// outcome distinctly. No lane or no chain identity = the honest pending stop; a
// REVERT is mined-and-failed — surfaced to the owner, never recorded as settled.
func (b *Brain) settleOnChain(ctx context.Context, jobID string) (string, error) {
	pending := func(note string) (string, error) {
		if _, err := b.store.Append(ctx, store.Event{
			Kind: "settlement.pending_chain", EntityID: jobID, Actor: "customer",
			Payload: map[string]any{"note": note},
		}); err != nil {
			return "", err
		}
		b.log.Info("delivery accepted", "job", jobID, "settlement", "pending-chain")
		return "accepted-pending-chain", nil
	}
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return "", err
	}
	if b.funding == nil {
		return pending("delivery accepted; no funding agent wired — settlement pending")
	}
	if jm.VaultJobID == "" {
		return pending("delivery accepted; no on-chain job identity yet (vault_job_id unset) — settlement pending")
	}
	out, err := b.funding.SettleOnChain(context.WithoutCancel(ctx), jm.VaultJobID)
	if err != nil {
		return "", fmt.Errorf("acceptDelivery submission: %w", err)
	}
	if !out.Submitted {
		return pending("delivery accepted; no customer chain lane wired — settlement pending")
	}
	if out.Reverted {
		if _, aerr := b.store.Append(ctx, store.Event{
			Kind: "settlement.reverted", EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": out.TxHash, "block": out.Block, "gas_used": out.GasUsed,
				"note": "acceptDelivery MINED AND REVERTED — not settled; owner attention required",
			},
		}); aerr != nil {
			return "", aerr
		}
		b.log.Warn("settlement reverted on chain", "job", jobID, "tx", out.TxHash)
		return "accepted-settlement-reverted", nil
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: "settlement.executed", EntityID: jobID, Actor: "funding",
		Payload: map[string]any{"tx_hash": out.TxHash, "block": out.Block, "gas_used": out.GasUsed},
	}); err != nil {
		return "", err
	}
	b.log.Info("settlement executed on chain", "job", jobID, "tx", out.TxHash, "gas", out.GasUsed)
	return "accepted-settled", nil
}
