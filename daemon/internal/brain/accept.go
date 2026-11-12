package brain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/funding"
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
//
// The claim (delivery_ready -> accepted) is exactly-once, but it is not allowed to consume
// the settlement: an attempt that reverted on chain, that failed before anything was
// broadcast, or that stopped at pending-chain, leaves the job accepted and UNSETTLED, so a
// later Accept retries it (retryAcceptedSettlement). Without that, one early or unlucky
// Accept would wedge the job at accepted-pending-chain for good and the operator's payout
// would never settle.
func (b *Brain) AcceptDelivery(ctx context.Context, jobID string) (string, error) {
	// Freeze gate first: the kill switch stops settlements like it stops payments.
	if err := b.frozenErr(jobID, ""); err != nil {
		return "", fmt.Errorf("%w: %s", ErrFrozen, err)
	}

	// Exactly-once claim under the lock: delivery_ready -> accepted is the transition.
	// Every attempt, first or retry, also takes the per-job settling marker under the same
	// lock, so at most one settlement attempt per job is ever in flight; a concurrent
	// Accept sees the marker and returns without touching the chain.
	b.mu.Lock()
	js, ok := b.jobs[jobID]
	if !ok {
		b.mu.Unlock()
		return "", fmt.Errorf("unknown job %s", jobID)
	}
	switch js.Stage {
	case StageAccepted:
		if b.settling[jobID] {
			b.mu.Unlock()
			return "accepted-pending-chain", nil
		}
		b.settling[jobID] = true
		b.mu.Unlock()
		defer b.clearSettling(jobID)
		return b.retryAcceptedSettlement(ctx, jobID)
	case StageDeliveryReady:
		js.Stage = StageAccepted // the claim
		b.settling[jobID] = true
	default:
		stage := js.Stage
		b.mu.Unlock()
		return "", fmt.Errorf("%w: stage %s", ErrNotDeliveryReady, stage)
	}
	b.mu.Unlock()
	defer b.clearSettling(jobID)

	if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.Stage = string(StageAccepted) }); err != nil {
		return "", err
	}
	return b.settleAndObserve(ctx, jobID, settlementRecord{})
}

func (b *Brain) settleAndObserve(ctx context.Context, jobID string, rec settlementRecord) (string, error) {
	state, err := b.settleOnChain(ctx, jobID, rec)
	if err != nil {
		return state, err
	}
	if state == "accepted-settled" {
		b.recordMilestoneObservation(ctx, jobID)
	}
	return state, nil
}

func (b *Brain) clearSettling(jobID string) {
	b.mu.Lock()
	delete(b.settling, jobID)
	b.mu.Unlock()
}

// Settlement records, as the durable event log states them for an accepted job.
const (
	settlementSettled  = "settlement.executed"      // final
	settlementPending  = "settlement.pending_chain" // the honest no-lane stop; retried once the chain can take it
	settlementReverted = "settlement.reverted"      // mined and failed; retryable
	// settlementSubmitted is written the moment the lane's acceptDelivery is out, BEFORE
	// its receipt wait. Until a settlement.executed, .reverted or .dropped with the same
	// tx_hash follows, that transaction may still mine, and nothing is resubmitted. It names
	// the transaction (tx_hash, nonce), the signing address (signer_address) and the signed
	// bytes as sent (raw_tx), so the broadcast can be resolved against the right account and
	// re-sent unchanged.
	settlementSubmitted = "settlement.submitted"
	// settlementDropped: a recorded broadcast that never mined and never can, because the
	// signer's nonce has moved past it. Nothing settled; retryable. Recorded only after two
	// settlement.unseen observations agree with the chain head advanced between them.
	settlementDropped = "settlement.dropped"
	// settlementUnseen is one observation of a recorded broadcast with no receipt that the
	// node does not know: verdict "dropped" (the signer's nonce is past it) or "evicted"
	// (its nonce is still open), at chain head `head`. It resolves nothing by itself; it is
	// the persisted first look that a second look, at a higher head, confirms.
	settlementUnseen = "settlement.unseen"
	// settlementRebroadcast: the recorded signed bytes of an evicted broadcast were sent
	// again, unchanged (same nonce, same hash). The broadcast stays open.
	settlementRebroadcast = "settlement.rebroadcast"
)

// Verdicts a settlement.unseen observation carries.
const (
	unseenDropped = "dropped"
	unseenEvicted = "evicted"
)

// Who signed a recorded settlement. The daemon's lane signs with the demo's custodial
// customer key; a self-custody customer signs with their own wallet.
const (
	signerDaemonLane = "daemon_custodial"
	signerCustomer   = "customer"
)

// The customer-facing settlement status of a job (H2 §4.3), one of exactly four values.
const (
	// SettlementNone: the job is not accepted yet.
	SettlementNone = "none"
	// SettlementSettled: a settlement.executed is recorded.
	SettlementSettled = "settled"
	// SettlementPending: accepted, and nothing the customer does now changes anything: an
	// attempt is in flight, a broadcast is open, the job has no chain identity or no settle
	// lane is wired, or the chain has not reached Delivered. Pressing Accept is safe but
	// will most likely report pending again.
	SettlementPending = "pending"
	// SettlementRetry: accepted, and the last attempt reverted, failed, was dropped or was
	// never recorded (or the chain shows a settlement no record has yet): pressing Accept
	// again is what moves it.
	SettlementRetry = "retry"
)

// openBroadcast is a recorded broadcast that no later record resolves.
type openBroadcast struct {
	funding.Broadcast
	// seen is the newest settlement.unseen observation since the last rebroadcast; nil for none.
	seen *unseenSighting
}

type unseenSighting struct {
	verdict string
	head    uint64
}

// settlementRecord is an accepted job's settlement history, read from the event log.
type settlementRecord struct {
	settled bool
	// latest is the newest of pending_chain, reverted, dropped and submitted, "" for none.
	latest string
	// unresolved are the lane's broadcasts that no later record resolves, oldest first.
	unresolved []openBroadcast
	// ours is every transaction hash (lower-cased) the daemon's lane broadcast for the job.
	ours map[string]bool
}

// readSettlementRecord reads the accepted job's settlement history. A read error, or a
// submission record that does not say which transaction it was, is returned and never
// guessed past: the caller must not submit on an unknown record.
func (b *Brain) readSettlementRecord(ctx context.Context, jobID string) (settlementRecord, error) {
	rec := settlementRecord{ours: map[string]bool{}}
	rows, err := b.store.DB().QueryContext(ctx,
		`SELECT kind, payload_json FROM events WHERE entity_id=? AND kind IN (?, ?, ?, ?, ?, ?, ?) ORDER BY seq`,
		jobID, settlementSettled, settlementPending, settlementReverted, settlementSubmitted, settlementDropped,
		settlementUnseen, settlementRebroadcast)
	if err != nil {
		return settlementRecord{}, err
	}
	defer rows.Close()
	var order []string
	open := map[string]*openBroadcast{}
	for rows.Next() {
		var kind string
		var raw sql.NullString
		if err := rows.Scan(&kind, &raw); err != nil {
			return settlementRecord{}, err
		}
		var p struct {
			TxHash        string  `json:"tx_hash"`
			Nonce         *uint64 `json:"nonce"`
			SignerAddress string  `json:"signer_address"`
			RawTx         string  `json:"raw_tx"`
			Head          *uint64 `json:"head"`
			Verdict       string  `json:"verdict"`
		}
		if raw.Valid && raw.String != "" {
			if err := json.Unmarshal([]byte(raw.String), &p); err != nil {
				return settlementRecord{}, fmt.Errorf("%s record is unreadable: %w", kind, err)
			}
		}
		hash := strings.ToLower(p.TxHash)
		switch kind {
		case settlementSettled:
			rec.settled = true
			continue
		case settlementSubmitted:
			if hash == "" || p.Nonce == nil {
				return settlementRecord{}, fmt.Errorf("a %s record does not name its transaction and nonce", kind)
			}
			bc := funding.Broadcast{TxHash: p.TxHash, Nonce: *p.Nonce}
			if p.SignerAddress != "" {
				if !common.IsHexAddress(p.SignerAddress) {
					return settlementRecord{}, fmt.Errorf("a %s record names a signer that is not an address", kind)
				}
				bc.Signer = common.HexToAddress(p.SignerAddress)
			}
			if p.RawTx != "" {
				rawTx, err := hex.DecodeString(strings.TrimPrefix(p.RawTx, "0x"))
				if err != nil {
					return settlementRecord{}, fmt.Errorf("a %s record's signed bytes are unreadable: %w", kind, err)
				}
				bc.Raw = rawTx
			}
			rec.ours[hash] = true
			if _, again := open[hash]; !again {
				order = append(order, hash)
			}
			open[hash] = &openBroadcast{Broadcast: bc}
		case settlementReverted, settlementDropped:
			if hash != "" {
				delete(open, hash)
			}
		case settlementUnseen:
			// An observation, not an attempt: it never changes latest.
			if ob := open[hash]; ob != nil && p.Head != nil && (p.Verdict == unseenDropped || p.Verdict == unseenEvicted) {
				ob.seen = &unseenSighting{verdict: p.Verdict, head: *p.Head}
			}
			continue
		case settlementRebroadcast:
			// A re-send starts the observations over: the bytes are out again.
			if ob := open[hash]; ob != nil {
				ob.seen = nil
			}
			continue
		}
		rec.latest = kind
	}
	if err := rows.Err(); err != nil {
		return settlementRecord{}, err
	}
	for _, h := range order {
		if ob := open[h]; ob != nil {
			rec.unresolved = append(rec.unresolved, *ob)
		}
	}
	return rec, nil
}

// settlementStep is what the record and the chain say the next Accept on an accepted job
// does. nextSettlementStep is the ONE place these rules live: the retry acts on the step,
// and the customer's status read reports it.
type settlementStep int

const (
	stepSettled      settlementStep = iota // settlement.executed is recorded: final
	stepResolve                            // a recorded broadcast has no outcome yet: resolve it, submit nothing
	stepNoChain                            // no chain identity, or no settle lane wired: nothing can be submitted
	stepObserve                            // the chain shows the job settled and no record does yet: record it
	stepNotDelivered                       // the chain is not at Delivered: a submission would revert
	stepSubmit                             // reverted, failed, dropped or never recorded: submit again
)

// nextSettlementStep reads the chain only past the event-log rules, and fails closed: an
// unreadable chain oracle is an error, never a step.
func (b *Brain) nextSettlementStep(ctx context.Context, rec settlementRecord, vaultJobID string) (settlementStep, error) {
	switch {
	case rec.settled:
		return stepSettled, nil
	case len(rec.unresolved) > 0:
		return stepResolve, nil
	case vaultJobID == "" || b.funding == nil:
		return stepNoChain, nil
	}
	b.mu.Lock()
	landed, anchored := b.settlementLanded, b.deliveryAnchored
	b.mu.Unlock()
	if landed != nil {
		settled, err := landed(ctx, vaultJobID)
		if err != nil {
			return 0, fmt.Errorf("cannot read on-chain settlement: %w", err)
		}
		if settled {
			return stepObserve, nil
		}
	}
	if !b.funding.SettleLaneWired() {
		return stepNoChain, nil
	}
	if anchored != nil {
		ready, err := anchored(ctx, vaultJobID)
		if err != nil {
			return 0, fmt.Errorf("cannot read on-chain delivery: %w", err)
		}
		if !ready {
			return stepNotDelivered, nil
		}
	}
	return stepSubmit, nil
}

// SettlementStatus is the customer-facing settlement status (one of the Settlement*
// values), derived from the same record and the same nextSettlementStep rules the retry
// acts on. It is a READ: it records, resolves and submits nothing. Any read failure,
// including a wired chain oracle that cannot be read, is an error, never a guessed value.
func (b *Brain) SettlementStatus(ctx context.Context, jobID string) (string, error) {
	b.mu.Lock()
	js, ok := b.jobs[jobID]
	stage, inFlight := JobStage(""), b.settling[jobID]
	_, unrecorded := b.unrecorded[jobID]
	if ok {
		stage = js.Stage
	}
	b.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown job %s", jobID)
	}
	if stage != StageAccepted {
		return SettlementNone, nil
	}
	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("cannot read the settlement record: %w", err)
	}
	if rec.settled {
		return SettlementSettled, nil
	}
	if inFlight || unrecorded {
		// An attempt is running, or a broadcast is out that the next attempt records first.
		return SettlementPending, nil
	}
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return "", err
	}
	step, err := b.nextSettlementStep(ctx, rec, jm.VaultJobID)
	if err != nil {
		return "", err
	}
	switch step {
	case stepSettled:
		return SettlementSettled, nil
	case stepObserve, stepSubmit:
		return SettlementRetry, nil
	default:
		return SettlementPending, nil
	}
}

// retryAcceptedSettlement is a repeated Accept on an accepted job, holding the settling
// marker. A settled record is final: the observation repair runs and the state label is
// unchanged. A broadcast that went out unrecorded is recorded FIRST. A recorded broadcast
// whose outcome is unknown is resolved next, and nothing is resubmitted while it may still
// mine. A reverted, dropped, pending-chain or never-broadcast attempt is retried through the
// same settleOnChain path, which asks the chain first.
//
// A retry fails CLOSED on every read it depends on. An unreadable settlement record, a
// recorded broadcast the lane cannot read, or a wired chain oracle that cannot be read,
// returns an error and submits nothing. With the delivery oracle wired, a retry submits
// only when the job is Delivered on chain, so a customer repeating Accept before the
// operator anchors delivery costs no gas. A double settlement is impossible either way:
// JobVault.acceptDelivery reverts for any status but Delivered.
func (b *Brain) retryAcceptedSettlement(ctx context.Context, jobID string) (string, error) {
	if err := b.persistUnrecordedBroadcast(ctx, jobID); err != nil {
		return "", err
	}
	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("cannot read the settlement record before retrying Accept: %w", err)
	}
	if rec.settled {
		b.recordMilestoneObservation(ctx, jobID)
		return "accepted-pending-chain", nil
	}

	jm, err := b.memory.Get(jobID)
	if err != nil {
		return "", err
	}
	if jm.Stage != string(StageAccepted) {
		// The claim's own persist failed last time; finish it before settling.
		if err := b.memory.Update(jobID, func(jm *JobMemory) { jm.Stage = string(StageAccepted) }); err != nil {
			return "", err
		}
	}
	if len(rec.unresolved) > 0 {
		if state, done, err := b.resolveBroadcasts(ctx, jobID, jm.VaultJobID, rec); err != nil || done {
			return state, err
		}
		// Every recorded broadcast is now known not to have settled the job.
		rec.unresolved = nil
	}
	step, err := b.nextSettlementStep(ctx, rec, jm.VaultJobID)
	if err != nil {
		return "", fmt.Errorf("%w before retrying acceptDelivery", err)
	}
	switch step {
	case stepNoChain:
		if rec.latest == settlementPending {
			// Still nothing to submit through: nothing has changed since the pending record.
			return "accepted-pending-chain", nil
		}
	case stepNotDelivered:
		// Nothing the chain would accept yet; record nothing, submit nothing.
		return "accepted-pending-chain", nil
	}
	b.log.Info("retrying settlement for an accepted job", "job", jobID, "previous", rec.latest)
	return b.settleAndObserve(ctx, jobID, rec)
}

// ResolveOpenSettlements resolves, once, every recorded acceptDelivery broadcast that no
// later record resolves, across all accepted jobs, through the same resolveBroadcasts the
// retry uses: it records settlement.executed, .reverted, .dropped, .unseen and .rebroadcast
// exactly as an Accept would. It NEVER submits a new acceptDelivery (only an Accept does),
// and skips a job whose attempt is in flight. The daemon runs it at boot and on a ticker,
// so a broadcast left open by a crash or a failed receipt wait is resolved, and a dropped or
// evicted one confirmed, without waiting for the customer. Returns how many jobs it
// resolved and every error it met (one job's error never stops the others).
func (b *Brain) ResolveOpenSettlements(ctx context.Context) (int, error) {
	b.mu.Lock()
	var ids []string
	for id, js := range b.jobs {
		if js.Stage == StageAccepted {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	sort.Strings(ids)
	n := 0
	var errs []error
	for _, id := range ids {
		did, err := b.resolveOpenSettlement(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("job %s: %w", id, err))
		}
		if did {
			n++
		}
	}
	return n, errors.Join(errs...)
}

func (b *Brain) resolveOpenSettlement(ctx context.Context, jobID string) (bool, error) {
	b.mu.Lock()
	if b.settling[jobID] {
		b.mu.Unlock()
		return false, nil
	}
	b.settling[jobID] = true
	b.mu.Unlock()
	defer b.clearSettling(jobID)

	if err := b.persistUnrecordedBroadcast(ctx, jobID); err != nil {
		return false, err
	}
	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil {
		return false, err
	}
	if rec.settled || len(rec.unresolved) == 0 {
		return false, nil
	}
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return false, err
	}
	if _, _, err := b.resolveBroadcasts(ctx, jobID, jm.VaultJobID, rec); err != nil {
		return false, err
	}
	return true, nil
}

// resolveBroadcasts asks the lane what became of each recorded, unresolved acceptDelivery,
// oldest first, and records the answer. done=true means the caller returns state as is:
// the job settled, a broadcast may still mine, or a revert is being surfaced. done=false
// means every broadcast is now known never to have settled the job, so a fresh submission
// through the usual gates is safe.
func (b *Brain) resolveBroadcasts(ctx context.Context, jobID, vaultJobID string, rec settlementRecord) (string, bool, error) {
	reverted := false
	for _, bc := range rec.unresolved {
		res := funding.Resolution{State: funding.TxPending}
		err := funding.ErrSettlementUnresolvable
		if b.funding != nil {
			res, err = b.funding.ResolveSettlement(ctx, bc.Broadcast)
		}
		if errors.Is(err, funding.ErrSettlementUnresolvable) {
			// No wired lane can read it (the daemon restarted without the customer key, say).
			// The chain can still say whether the job settled, and the indexed JobSettled
			// hash says whose transaction did it; without that hash, nothing is claimed.
			if state, done, oerr := b.observeSettlement(ctx, jobID, vaultJobID, rec.ours, false); oerr != nil || done {
				return state, true, oerr
			}
			b.log.Warn("a recorded acceptDelivery cannot be resolved with no settlement lane wired; nothing is resubmitted",
				"job", jobID, "tx", bc.TxHash)
			return "accepted-pending-chain", true, nil
		}
		if err != nil {
			return "", true, fmt.Errorf("cannot resolve the recorded acceptDelivery %s before retrying: %w", bc.TxHash, err)
		}
		out := res.Outcome
		switch res.State {
		case funding.TxMined:
			if !out.Reverted {
				if _, err := b.store.Append(ctx, store.Event{
					Kind: settlementSettled, EntityID: jobID, Actor: "funding",
					Payload: map[string]any{
						"tx_hash": bc.TxHash, "block": out.Block, "gas_used": out.GasUsed, "signer": signerDaemonLane,
						"note": "the recorded acceptDelivery mined successfully; its receipt was read on a later resolution",
					},
				}); err != nil {
					return "", true, err
				}
				b.log.Info("settlement executed on chain (resolved from the recorded broadcast)", "job", jobID, "tx", bc.TxHash)
				b.recordMilestoneObservation(ctx, jobID)
				return "accepted-settled", true, nil
			}
			if _, err := b.store.Append(ctx, store.Event{
				Kind: settlementReverted, EntityID: jobID, Actor: "funding",
				Payload: map[string]any{
					"tx_hash": bc.TxHash, "block": out.Block, "gas_used": out.GasUsed,
					"note": "acceptDelivery MINED AND REVERTED — not settled; owner attention required",
				},
			}); err != nil {
				return "", true, err
			}
			b.log.Warn("settlement reverted on chain (resolved from the recorded broadcast)", "job", jobID, "tx", bc.TxHash)
			reverted = true
		case funding.TxDropped, funding.TxEvicted:
			dropped, err := b.confirmUnseen(ctx, jobID, bc, res)
			if err != nil {
				return "", true, err
			}
			if !dropped {
				// Unconfirmed, or evicted and re-sent: it may still mine, so nothing is resubmitted.
				return "accepted-pending-chain", true, nil
			}
		default:
			// Still pending, or nothing is known yet: it may mine, so nothing is resubmitted.
			return "accepted-pending-chain", true, nil
		}
	}
	if reverted {
		// A broadcast of ours reverts when someone else settled first (the customer's own
		// wallet): the chain says so, and that settlement is recorded under its signer.
		if state, done, err := b.observeSettlement(ctx, jobID, vaultJobID, rec.ours, true); err != nil || done {
			return state, true, err
		}
		// Otherwise surfaced exactly like a first attempt's revert; the next Accept retries.
		return "accepted-settlement-reverted", true, nil
	}
	return "", false, nil
}

// confirmUnseen handles one observation of a broadcast that has no receipt and that the node
// does not know. A single such look is not believed: through a load-balanced RPC a mined
// transaction can look exactly like this (the nonce backend ahead of the receipt backend).
// The first look is recorded (settlement.unseen, with its verdict and chain head) so it
// survives a restart; only a second look with the same verdict at a strictly higher head is
// acted on. A confirmed "dropped" is recorded as settlement.dropped (returns true: the job
// may be resubmitted). A confirmed "evicted" re-sends the recorded signed bytes unchanged,
// which cannot settle twice (same nonce, same hash), and the broadcast stays open.
func (b *Brain) confirmUnseen(ctx context.Context, jobID string, bc openBroadcast, res funding.Resolution) (bool, error) {
	verdict := unseenDropped
	if res.State == funding.TxEvicted {
		verdict = unseenEvicted
	}
	if bc.seen == nil || bc.seen.verdict != verdict {
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementUnseen, EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": bc.TxHash, "nonce": bc.Nonce, "head": res.Head, "verdict": verdict,
				"note": "the recorded acceptDelivery has no receipt and the node does not know it; " +
					"confirmed only by a second observation at a higher chain head",
			},
		}); err != nil {
			return false, err
		}
		b.log.Warn("a recorded acceptDelivery is unseen by the node; waiting for a second observation",
			"job", jobID, "tx", bc.TxHash, "verdict", verdict, "head", res.Head)
		return false, nil
	}
	if res.Head <= bc.seen.head {
		// The chain has not moved since the first look: this one adds nothing.
		return false, nil
	}
	if verdict == unseenDropped {
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementDropped, EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": bc.TxHash, "nonce": bc.Nonce, "head": res.Head, "first_seen_head": bc.seen.head,
				"note": "the recorded acceptDelivery never mined and cannot: the signer's nonce moved past it " +
					"(observed twice, the chain head advancing between)",
			},
		}); err != nil {
			return false, err
		}
		b.log.Warn("a recorded acceptDelivery was dropped; the settlement may be resubmitted", "job", jobID, "tx", bc.TxHash)
		return true, nil
	}
	// Evicted, confirmed. The kill switch stops settlements, a re-send included.
	if ferr := b.frozenErr(jobID, ""); ferr != nil {
		b.log.Warn("an evicted acceptDelivery is not re-sent while frozen", "job", jobID, "tx", bc.TxHash)
		return false, nil
	}
	if len(bc.Raw) == 0 {
		b.log.Error("an evicted acceptDelivery has no recorded signed bytes and cannot be re-sent; "+
			"it stays open until its nonce is used (see the H2 settlement recovery note)",
			"job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce)
		return false, nil
	}
	if err := b.funding.RebroadcastSettlement(ctx, bc.Broadcast); err != nil {
		// Nothing new is out; the next resolution looks again.
		b.log.Warn("re-sending an evicted acceptDelivery failed; it stays open", "job", jobID, "tx", bc.TxHash, "err", err)
		return false, nil
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: settlementRebroadcast, EntityID: jobID, Actor: "funding",
		Payload: map[string]any{
			"tx_hash": bc.TxHash, "nonce": bc.Nonce, "head": res.Head,
			"note": "the evicted acceptDelivery's recorded signed bytes were sent again, unchanged (same nonce, same hash)",
		},
	}); err != nil {
		return false, err
	}
	b.log.Warn("an evicted acceptDelivery was re-sent unchanged", "job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce)
	return false, nil
}

// SetDeliveryOracle wires the on-chain read a retried settlement consults before
// resubmitting: whether the job is Delivered, the one status acceptDelivery accepts. With
// none wired, a retry submits as the first attempt does and the contract stays the guard.
func (b *Brain) SetDeliveryOracle(anchored func(ctx context.Context, vaultJobID string) (bool, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deliveryAnchored = anchored
}

func (b *Brain) recordMilestoneObservation(ctx context.Context, jobID string) {
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

// recordSubmitted writes the settlement.submitted record for a broadcast.
func (b *Brain) recordSubmitted(ctx context.Context, jobID string, bc funding.Broadcast) error {
	payload := map[string]any{
		"tx_hash": bc.TxHash, "nonce": bc.Nonce, "signer": signerDaemonLane,
		"note": "acceptDelivery broadcast; its receipt has not been read yet",
	}
	if bc.Signer != (common.Address{}) {
		payload["signer_address"] = bc.Signer.Hex()
	}
	if len(bc.Raw) > 0 {
		payload["raw_tx"] = "0x" + hex.EncodeToString(bc.Raw)
	}
	_, err := b.store.Append(context.WithoutCancel(ctx), store.Event{
		Kind: settlementSubmitted, EntityID: jobID, Actor: "funding", Payload: payload,
	})
	return err
}

// persistUnrecordedBroadcast writes the settlement.submitted record of a broadcast that went
// out unrecorded in this process. Until it is written nothing else happens for the job: an
// error here fails the attempt closed.
func (b *Brain) persistUnrecordedBroadcast(ctx context.Context, jobID string) error {
	b.mu.Lock()
	bc, ok := b.unrecorded[jobID]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	if err := b.recordSubmitted(ctx, jobID, bc); err != nil {
		b.log.Error("acceptDelivery is OUT and its broadcast is STILL unrecorded; nothing is resubmitted until it is",
			"job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce, "err", err)
		return fmt.Errorf("acceptDelivery %s (nonce %d) is out but still unrecorded; nothing is resubmitted until it is: %w",
			bc.TxHash, bc.Nonce, err)
	}
	b.mu.Lock()
	delete(b.unrecorded, jobID)
	b.mu.Unlock()
	b.log.Info("recorded an acceptDelivery broadcast that went out unrecorded", "job", jobID, "tx", bc.TxHash)
	return nil
}

// settleOnChain is the chain half of an authenticated, claimed Accept: submit
// JobVault.acceptDelivery through Funding's CUSTOMER lane (SC-JV-005 — only the
// customer settles; the demo lane is daemon-custodial, stated openly) and record the
// outcome distinctly. No lane or no chain identity = the honest pending stop; a
// REVERT is mined-and-failed — surfaced to the owner, never recorded as settled.
//
// The broadcast itself is recorded (settlement.submitted) as soon as it is out and before
// the receipt wait, so a wait that fails leaves a record the next Accept resolves instead
// of a gap it would read as "never sent". If that record cannot be written AND the receipt
// wait fails, the transaction is out with no durable trace: the write is tried once more,
// and if it still fails the Accept fails with an explicit error naming the transaction, the
// broadcast is held in memory so the next attempt records it before anything else, and the
// log says so loudly. rec is the job's prior history (zero on the first attempt): it names
// the lane's own earlier transactions and suppresses a second, identical pending record.
func (b *Brain) settleOnChain(ctx context.Context, jobID string, rec settlementRecord) (string, error) {
	pending := func(note string) (string, error) {
		if rec.latest == settlementPending {
			// Already recorded, and nothing has changed; a repeat adds no record.
			return "accepted-pending-chain", nil
		}
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementPending, EntityID: jobID, Actor: "customer",
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
	if state, done, err := b.observeSettlement(ctx, jobID, jm.VaultJobID, rec.ours, true); err != nil || done {
		return state, err
	}
	if b.funding == nil {
		return pending("delivery accepted; no funding agent wired — settlement pending")
	}
	if jm.VaultJobID == "" {
		return pending("delivery accepted; no on-chain job identity yet (vault_job_id unset) — settlement pending")
	}
	var sent *funding.Broadcast
	recorded := false
	onBroadcast := func(bc funding.Broadcast) {
		sent = &bc
		if err := b.recordSubmitted(ctx, jobID, bc); err != nil {
			b.log.Error("acceptDelivery is out but its broadcast record failed", "job", jobID, "tx", bc.TxHash, "err", err)
			return
		}
		recorded = true
	}
	out, err := b.funding.SettleOnChain(context.WithoutCancel(ctx), jm.VaultJobID, onBroadcast)
	if err != nil {
		if sent != nil && !recorded {
			if rerr := b.recordSubmitted(ctx, jobID, *sent); rerr != nil {
				b.mu.Lock()
				b.unrecorded[jobID] = *sent
				b.mu.Unlock()
				b.log.Error("SETTLEMENT TRANSACTION IS OUT AND UNRECORDED: acceptDelivery was broadcast, its record "+
					"could not be written and its receipt was not read; the next attempt records it before anything else, "+
					"and after a restart only the chain read stands between this transaction and a resubmission",
					"job", jobID, "tx", sent.TxHash, "nonce", sent.Nonce, "signer", sent.Signer.Hex(),
					"receipt_err", err, "record_err", rerr)
				return "", fmt.Errorf("acceptDelivery %s (nonce %d) was broadcast but is unrecorded (%v) and its receipt was not read (%v); "+
					"the settlement may still land: nothing is resubmitted until the broadcast is recorded and resolved",
					sent.TxHash, sent.Nonce, rerr, err)
			}
			recorded = true
		}
		if recorded {
			// The transaction is out and recorded; whether it mines is the chain's to say,
			// and the next Accept asks about this exact transaction before anything else.
			b.log.Warn("acceptDelivery broadcast but its receipt was not read; the next Accept resolves it",
				"job", jobID, "tx", sent.TxHash, "err", err)
			return "accepted-pending-chain", nil
		}
		return "", fmt.Errorf("acceptDelivery submission: %w", err)
	}
	if !out.Submitted {
		return pending("delivery accepted; no customer chain lane wired — settlement pending")
	}
	if out.Reverted {
		if _, aerr := b.store.Append(ctx, store.Event{
			Kind: settlementReverted, EntityID: jobID, Actor: "funding",
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
		Kind: settlementSettled, EntityID: jobID, Actor: "funding",
		Payload: map[string]any{"tx_hash": out.TxHash, "block": out.Block, "gas_used": out.GasUsed, "signer": signerDaemonLane},
	}); err != nil {
		return "", err
	}
	b.log.Info("settlement executed on chain", "job", jobID, "tx", out.TxHash, "gas", out.GasUsed)
	return "accepted-settled", nil
}

// SetSettlementOracle wires the on-chain read settleOnChain consults before submitting.
// With none wired, every Accept goes to the customer lane as before.
func (b *Brain) SetSettlementOracle(landed func(ctx context.Context, vaultJobID string) (bool, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settlementLanded = landed
}

// observeSettlement handles a job the chain already shows as Accepted, most often the
// self-custody customer: their own wallet signed JobVault.acceptDelivery (SC-JV-005 needs
// nothing else), and the portal then sends the credential-gated Accept so the daemon's
// record matches. The chain is the oracle: if the job is already Accepted there, the
// settlement HAPPENED, and is recorded as executed. Submitting through the custodial lane
// instead would mine and revert (the job is no longer Delivered) and be reported as a
// failed settlement of a job that settled fine.
//
// The signer is derived from what actually happened, never assumed. The transaction hash
// comes from the indexed JobSettled event when the indexer has seen it: a hash the
// daemon's own lane broadcast (ours) was signed by the daemon's custodial lane, any other
// by the customer. Without an indexed hash the customer is named only when
// attributeWithoutHash says none of the lane's broadcasts could have done it; otherwise
// nothing is recorded yet. A missing hash is said so in the record rather than leaving the
// field looking complete.
//
// A wired oracle that cannot be read FAILS CLOSED, exactly as a retry does: the chain may
// already show the job settled (by the customer's wallet, or by a broadcast of ours that
// went out unrecorded), and submitting blind would spend gas on a revert and report a
// failed settlement of a job that settled. The Accept returns the error; the claim stands,
// and the next Accept reads the chain again.
func (b *Brain) observeSettlement(ctx context.Context, jobID, vaultJobID string, ours map[string]bool, attributeWithoutHash bool) (string, bool, error) {
	b.mu.Lock()
	landed, agent := b.settlementLanded, b.billingAgent
	b.mu.Unlock()
	if landed == nil || vaultJobID == "" {
		return "", false, nil
	}
	ok, err := landed(ctx, vaultJobID)
	if err != nil {
		b.log.Warn("cannot read on-chain settlement before acceptDelivery; nothing is submitted", "job", jobID, "err", err)
		return "", false, fmt.Errorf("cannot read on-chain settlement before acceptDelivery: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	// The indexed event is looked up on this deployment's chain only: a deterministic demo
	// job id can exist on testnet and mainnet alike. With no billing agent there is no chain
	// id to scope by, so no hash is claimed.
	var txHash string
	var block int64
	err = sql.ErrNoRows
	if agent != nil {
		err = b.store.DB().QueryRowContext(ctx,
			`SELECT transaction_hash, block_number FROM chain_events
			 WHERE chain_id=? AND kind='JobSettled' AND entity_id=? ORDER BY block_number LIMIT 1`,
			agent.ChainID(), vaultJobID).Scan(&txHash, &block)
	}
	signer := signerCustomer
	payload := map[string]any{}
	switch {
	case err == nil:
		payload["tx_hash"], payload["block"] = txHash, block
		if ours[strings.ToLower(txHash)] {
			signer = signerDaemonLane
		}
	case errors.Is(err, sql.ErrNoRows):
		if !attributeWithoutHash {
			return "", false, nil
		}
		payload["tx_hash_pending"] = "the indexer has not yet recorded JobSettled for this job"
	default:
		return "", false, err
	}
	actor := "customer"
	payload["signer"] = signer
	if signer == signerDaemonLane {
		actor = "funding"
		payload["note"] = "acceptDelivery was signed by the daemon's custodial customer lane; observed on chain"
	} else {
		payload["note"] = "acceptDelivery was signed by the customer's own wallet; observed on chain"
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: settlementSettled, EntityID: jobID, Actor: actor, Payload: payload,
	}); err != nil {
		return "", false, err
	}
	b.log.Info("settlement observed on chain", "job", jobID, "tx", txHash, "signer", signer)
	return "accepted-settled", true, nil
}

// VaultJobID is the job's bound on-chain identity, empty when none is bound yet. The
// customer surface returns it so a customer settling from their own wallet can name the
// on-chain job; it is the customer's own job, so this exposes nothing they are not party to.
func (b *Brain) VaultJobID(jobID string) string {
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return ""
	}
	return jm.VaultJobID
}
