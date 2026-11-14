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
	// settlementSubmitted is written WRITE-AHEAD: once the lane's acceptDelivery is signed
	// and BEFORE it is sent, so no transaction can reach a node without one. If it cannot be
	// written, nothing is sent. Until a settlement.executed, .reverted or .dropped with the
	// same tx_hash follows, that transaction may be out and may still mine, and nothing new
	// is submitted. It names the transaction (tx_hash, nonce), the signing address
	// (signer_address), the signed bytes (raw_tx) and the chain head it was signed at
	// (sent_head), so the broadcast can be resolved against the right account, re-sent
	// unchanged, and aged. A fee replacement carries `replaces`: the tx_hash it replaces at
	// the same nonce.
	settlementSubmitted = "settlement.submitted"
	// settlementDropped: a recorded broadcast that never mined and never can, because the
	// signer's nonce has moved past it. Nothing settled by it. Recorded after two
	// settlement.unseen observations agree with the chain head advanced between them, or at
	// once (with `taken_by`) when another transaction at the same nonce has a receipt.
	settlementDropped = "settlement.dropped"
	// settlementUnseen is one observation of a recorded broadcast with no receipt that the
	// node does not know: verdict "dropped" (the signer's nonce is past it) or "evicted"
	// (its nonce is still open), at chain head `head`. It resolves nothing by itself; it is
	// the persisted first look that a second look, at a higher head, confirms.
	settlementUnseen = "settlement.unseen"
	// settlementRebroadcast: the recorded signed bytes of an evicted broadcast were sent
	// again, unchanged (same nonce, same hash). The broadcast stays open.
	settlementRebroadcast = "settlement.rebroadcast"
	// settlementStuck: a broadcast that will not mine and that the daemon will not replace
	// again: its `reason` is max_replacements (still unmined after the maximum number of fee
	// replacements at its nonce), signer_mismatch (another key signed it, so this lane
	// cannot sign at that account's nonce: the key rotated), fee_cap (outbidding it would pass
	// the fee cap), record_mismatch (its recorded signed bytes are not the recorded
	// transaction) or signer_unknown (the record names no signer and has no bytes). Written
	// once per transaction and logged at ERROR; nothing more is replaced automatically, and
	// the owner has to act. The broadcast stays open and is still resolved.
	settlementStuck = "settlement.stuck"
)

// Fee replacement of a recorded acceptDelivery the node holds but does not mine. Arc's
// blocks are ~500ms, so the default waits about two minutes (two settlement-resolver
// passes) per step.
const (
	defaultSettlementReplaceAfterBlocks = 240
	defaultSettlementMaxReplacements    = 3
	// defaultSettlementMaxRebroadcasts bounds how often one evicted transaction's same bytes
	// are re-sent. Each re-send starts its observations over, so a node that keeps evicting
	// it would otherwise keep it open forever; past the bound it is fee-replaced (or recorded
	// stuck) like one the node refuses to take back.
	defaultSettlementMaxRebroadcasts = 3
)

// The reasons a settlement.stuck record gives.
const (
	stuckMaxReplacements = "max_replacements"
	stuckSignerMismatch  = "signer_mismatch"
	stuckFeeCap          = "fee_cap"
	stuckRecordMismatch  = "record_mismatch"
	stuckSignerUnknown   = "signer_unknown"
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

// The customer-facing settlement status of a job (H2 §4.3). SettlementStatus returns one of
// the first four; the customer surface serves the fifth, SettlementUnknown, when it cannot
// read the status at all.
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
	// never recorded: pressing Accept again is what moves it. (A settlement the chain shows
	// and no record has yet is recorded by the read itself and reported settled.)
	SettlementRetry = "retry"
	// SettlementUnknown: the status could not be read (a chain oracle or the record was
	// unreadable). Never returned by SettlementStatus, which returns the error instead; the
	// customer surface serves it in place of a guessed value so the read itself still
	// answers. Pressing Accept is safe: it re-reads everything and fails closed.
	SettlementUnknown = "unknown"
)

// openBroadcast is a recorded broadcast that no later record resolves.
type openBroadcast struct {
	funding.Broadcast
	// seen is the newest settlement.unseen observation since the last rebroadcast; nil for none.
	seen *unseenSighting
	// root is the tx_hash (lower-cased) of the first submission in this broadcast's
	// replacement chain: its own, unless it `replaces` another. Every transaction with one
	// root occupies one nonce, so at most one of them can mine.
	root string
	// stuck: a settlement.stuck is already recorded for this transaction.
	stuck bool
	// rebroadcasts is how many times this transaction's same bytes were re-sent.
	rebroadcasts int
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
	// chainLen is how many submissions each replacement chain (by root) has recorded,
	// resolved or not: the original plus every replacement.
	chainLen map[string]int
}

// nonceGroups splits the unresolved broadcasts by replacement chain, oldest chain first and,
// within a chain, oldest (lowest fee) first.
func (r settlementRecord) nonceGroups() [][]openBroadcast {
	var order []string
	groups := map[string][]openBroadcast{}
	for _, ob := range r.unresolved {
		if _, ok := groups[ob.root]; !ok {
			order = append(order, ob.root)
		}
		groups[ob.root] = append(groups[ob.root], ob)
	}
	out := make([][]openBroadcast, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	return out
}

// readSettlementRecord reads the accepted job's settlement history. A read error, or a
// submission record that does not say which transaction it was, is returned and never
// guessed past: the caller must not submit on an unknown record.
func (b *Brain) readSettlementRecord(ctx context.Context, jobID string) (settlementRecord, error) {
	rec := settlementRecord{ours: map[string]bool{}, chainLen: map[string]int{}}
	rows, err := b.store.DB().QueryContext(ctx,
		`SELECT kind, payload_json FROM events WHERE entity_id=? AND kind IN (?, ?, ?, ?, ?, ?, ?, ?) ORDER BY seq`,
		jobID, settlementSettled, settlementPending, settlementReverted, settlementSubmitted, settlementDropped,
		settlementUnseen, settlementRebroadcast, settlementStuck)
	if err != nil {
		return settlementRecord{}, err
	}
	defer rows.Close()
	var order []string
	open := map[string]*openBroadcast{}
	rootOf := map[string]string{}
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
			SentHead      *uint64 `json:"sent_head"`
			Replaces      string  `json:"replaces"`
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
			if p.SentHead != nil {
				bc.SentHead = *p.SentHead
			}
			root := hash
			if prev := strings.ToLower(p.Replaces); prev != "" {
				r, ok := rootOf[prev]
				if !ok {
					return settlementRecord{}, fmt.Errorf("a %s record replaces %s, which no earlier record names", kind, p.Replaces)
				}
				root = r
			}
			if _, again := rootOf[hash]; !again {
				rec.chainLen[root]++
			}
			rootOf[hash] = root
			rec.ours[hash] = true
			if _, again := open[hash]; !again {
				order = append(order, hash)
			}
			open[hash] = &openBroadcast{Broadcast: bc, root: root}
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
				ob.rebroadcasts++
			}
			continue
		case settlementStuck:
			if ob := open[hash]; ob != nil {
				ob.stuck = true
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
	stepObserve                            // the chain shows the job settled and no record does yet: record it (an observation, never a transaction)
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

// SettlementStatus is the customer-facing settlement status (none, settled, pending or
// retry), derived from the same record and the same nextSettlementStep rules the retry acts
// on. It resolves and submits nothing. The one thing it may record is an OBSERVATION: when
// the chain shows the job settled and no record says so (the customer's own wallet settled
// it, or the oracle read failed during the Accept that would have recorded it), it records
// that settlement exactly as an Accept would (recordLandedSettlement) and reports settled,
// so a self-custody customer with no Accept left to press is not shown "retry" forever. Any
// read failure, including a wired chain oracle that cannot be read, is an error, never a
// guessed value. A frozen job reads the same: the kill switch stops settlement
// transactions, not the record of one the chain already shows.
func (b *Brain) SettlementStatus(ctx context.Context, jobID string) (string, error) {
	b.mu.Lock()
	js, ok := b.jobs[jobID]
	stage, inFlight := JobStage(""), b.settling[jobID]
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
	if inFlight {
		// An attempt is running.
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
	case stepObserve:
		return b.recordLandedSettlement(ctx, jobID)
	case stepSubmit:
		return SettlementRetry, nil
	default:
		return SettlementPending, nil
	}
}

// recordLandedSettlement records the settlement the chain shows for an accepted job whose
// record has none, as an observation: settlement.executed through observeSettlement, the
// path an Accept takes, with the signer derived from the indexed JobSettled hash. It never
// submits a transaction. It takes the job's settling marker, so it never races an Accept or
// the resolver into a second record; if an attempt holds the marker it records nothing and
// reports pending. Returns the resulting customer-facing status.
func (b *Brain) recordLandedSettlement(ctx context.Context, jobID string) (string, error) {
	b.mu.Lock()
	if b.settling[jobID] {
		b.mu.Unlock()
		return SettlementPending, nil
	}
	b.settling[jobID] = true
	b.mu.Unlock()
	defer b.clearSettling(jobID)

	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("cannot read the settlement record: %w", err)
	}
	recorded, err := b.observeUnrecordedSettlement(ctx, jobID, rec)
	switch {
	case err != nil:
		return "", err
	case recorded || rec.settled:
		return SettlementSettled, nil
	case len(rec.unresolved) > 0:
		return SettlementPending, nil
	default:
		// Read again under the marker, the chain no longer says settled: the step is a submit.
		return SettlementRetry, nil
	}
}

// observeUnrecordedSettlement is the observation step, run while holding the settling
// marker: for an accepted, unsettled job with no open broadcast of ours, it asks the chain
// whether the job settled anyway and records it (observeSettlement, attributing without an
// indexed hash: with no broadcast of ours open, none of ours can have done it unrecorded,
// since every one is recorded write-ahead). recorded=true means a settlement.executed was
// written now. It never submits anything.
func (b *Brain) observeUnrecordedSettlement(ctx context.Context, jobID string, rec settlementRecord) (bool, error) {
	if rec.settled || len(rec.unresolved) > 0 {
		return false, nil
	}
	jm, err := b.memory.Get(jobID)
	if err != nil {
		return false, err
	}
	_, done, err := b.observeSettlement(ctx, jobID, jm.VaultJobID, rec.ours, true)
	if err != nil || !done {
		return false, err
	}
	b.recordMilestoneObservation(ctx, jobID)
	return true, nil
}

// retryAcceptedSettlement is a repeated Accept on an accepted job, holding the settling
// marker. A settled record is final: the observation repair runs and the state label is
// unchanged. A recorded broadcast whose outcome is unknown is resolved first, and nothing new
// is submitted while it may still mine (a fee replacement at its own nonce is not new: see
// resolveBroadcasts). A reverted, dropped, pending-chain or never-broadcast attempt is retried through the
// same settleOnChain path, which asks the chain first.
//
// A retry fails CLOSED on every read it depends on. An unreadable settlement record, a
// recorded broadcast the lane cannot read, or a wired chain oracle that cannot be read,
// returns an error and submits nothing. With the delivery oracle wired, a retry submits
// only when the job is Delivered on chain, so a customer repeating Accept before the
// operator anchors delivery costs no gas. A double settlement is impossible either way:
// JobVault.acceptDelivery reverts for any status but Delivered.
func (b *Brain) retryAcceptedSettlement(ctx context.Context, jobID string) (string, error) {
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
// retry uses: it records settlement.executed, .reverted, .dropped, .unseen, .rebroadcast and
// .stuck exactly as an Accept would, and it re-sends or fee-replaces a stalled broadcast at
// its OWN nonce exactly as an Accept would. An accepted, unsettled job with NO open broadcast
// is asked about too: if the chain shows it settled (the customer's own wallet, or an Accept
// whose oracle read failed), the settlement is recorded as an observation, exactly as an
// Accept would record it. It NEVER submits an acceptDelivery at a new nonce (only an Accept
// does), and skips a job whose attempt is in flight. The daemon runs it at boot and on a
// ticker, so a broadcast left open by a crash or a failed receipt wait is resolved, and a
// dropped or evicted one confirmed, without waiting for the customer. Returns how many jobs
// it resolved and every error it met (one job's error never stops the others). A done ctx
// stops the pass between jobs; the rest wait for the next pass.
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
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("resolution pass stopped before job %s: %w", id, err))
			break
		}
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

	rec, err := b.readSettlementRecord(ctx, jobID)
	if err != nil {
		return false, err
	}
	if rec.settled {
		return false, nil
	}
	if len(rec.unresolved) == 0 {
		return b.observeUnrecordedSettlement(ctx, jobID, rec)
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

// resolveBroadcasts asks the lane what became of each recorded, unresolved acceptDelivery and
// records the answer, one nonce at a time (a broadcast and its fee replacements share a
// nonce, so at most one of them can mine), oldest first. done=true means the caller returns
// state as is: the job settled, a broadcast may still mine, or a revert is being surfaced.
// done=false means every broadcast is now known never to have settled the job, so a fresh
// submission through the usual gates is safe.
//
// Per nonce: a receipt for ANY transaction decides it (executed or reverted), and every other
// transaction at that nonce is recorded dropped, since it can never mine now. Otherwise, while
// the node holds any of them, the nonce is pending; the newest is fee-replaced once it has sat
// unmined long enough (replaceStalled). Otherwise every one is unseen, and confirmUnseen runs
// for each: only the newest (highest fee) is ever re-sent, and the nonce is dropped only when
// every one of them confirms dropped.
func (b *Brain) resolveBroadcasts(ctx context.Context, jobID, vaultJobID string, rec settlementRecord) (string, bool, error) {
	reverted := false
	for _, group := range rec.nonceGroups() {
		results := make([]funding.Resolution, len(group))
		mined, pending := -1, false
		for i, bc := range group {
			res := funding.Resolution{State: funding.TxPending}
			err := funding.ErrSettlementUnresolvable
			if b.funding != nil {
				res, err = b.funding.ResolveSettlement(ctx, bc.Broadcast)
			}
			if errors.Is(err, funding.ErrSettlementUnresolvable) {
				// No wired lane can read it (the daemon restarted without the customer key, or
				// the record names no signer, say). The chain can still say whether the job
				// settled, and the indexed JobSettled hash says whose transaction did it;
				// without that hash, nothing is claimed.
				if state, done, oerr := b.observeSettlement(ctx, jobID, vaultJobID, rec.ours, false); oerr != nil || done {
					return state, true, oerr
				}
				// A wired lane that refuses for good (the record names no signer and has no
				// bytes, or its bytes are not its transaction) will answer the same on every
				// pass, and nothing can replace it either: record it stuck, once, so the owner
				// is told. A lane that is simply not wired stays pending without a record.
				if reason := stuckReasonOf(err); reason != "" && !bc.stuck {
					if serr := b.recordStuck(ctx, jobID, bc, res.Head, rec.chainLen[bc.root]-1, reason, "unresolvable",
						"the recorded acceptDelivery cannot be resolved by the daemon's lane: "+err.Error()); serr != nil {
						return "", true, serr
					}
					return "accepted-pending-chain", true, nil
				}
				b.log.Warn("a recorded acceptDelivery cannot be resolved by the wired lane; nothing is resubmitted",
					"job", jobID, "tx", bc.TxHash, "err", err)
				return "accepted-pending-chain", true, nil
			}
			if err != nil {
				return "", true, fmt.Errorf("cannot resolve the recorded acceptDelivery %s before retrying: %w", bc.TxHash, err)
			}
			results[i] = res
			switch res.State {
			case funding.TxMined:
				if mined < 0 {
					mined = i
				}
			case funding.TxPending:
				pending = true
			}
		}
		head := uint64(0)
		for _, r := range results {
			if r.Head > head {
				head = r.Head
			}
		}
		if mined >= 0 {
			settled, err := b.recordMinedAtNonce(ctx, jobID, group, mined, results[mined].Outcome)
			if err != nil {
				return "", true, err
			}
			if settled {
				return "accepted-settled", true, nil
			}
			reverted = true
			continue
		}
		newest := len(group) - 1
		if pending {
			// The node holds it: it may mine, so nothing new is submitted, but a transaction
			// that sits unmined too long is re-priced at the same nonce.
			if err := b.replaceStalled(ctx, jobID, vaultJobID, group, rec.chainLen[group[0].root], head, "pending"); err != nil {
				return "", true, err
			}
			return "accepted-pending-chain", true, nil
		}
		allDropped := true
		for i, bc := range group {
			if results[i].State == funding.TxEvicted && i != newest {
				// An older, lower-fee transaction at this nonce: only the newest is re-sent.
				allDropped = false
				continue
			}
			out, err := b.confirmUnseen(ctx, jobID, bc, results[i])
			if err != nil {
				return "", true, err
			}
			switch out {
			case unseenConfirmedDropped:
			case unseenNotResent:
				// Evicted, confirmed, and the same bytes cannot go out again (a re-send the node
				// refuses, such as one priced under the base fee, or no bytes recorded): the
				// only way forward at this nonce is a replacement.
				allDropped = false
				if err := b.replaceStalled(ctx, jobID, vaultJobID, group, rec.chainLen[bc.root], results[i].Head, "evicted, not re-sent"); err != nil {
					return "", true, err
				}
			default:
				allDropped = false
			}
		}
		if !allDropped {
			// Unconfirmed, or evicted and re-sent: it may still mine, so nothing is resubmitted.
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

// recordMinedAtNonce records the outcome of the one transaction at a nonce that has a
// receipt (executed, or reverted), then records every other transaction at that nonce as
// dropped: the receipt proves the nonce is taken, so none of them can ever mine. Returns
// settled=true for a successful receipt.
func (b *Brain) recordMinedAtNonce(ctx context.Context, jobID string, group []openBroadcast, mined int, out funding.ChainOutcome) (bool, error) {
	bc := group[mined]
	if !out.Reverted {
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementSettled, EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": bc.TxHash, "block": out.Block, "gas_used": out.GasUsed, "signer": signerDaemonLane,
				"note": "the recorded acceptDelivery mined successfully; its receipt was read on a later resolution",
			},
		}); err != nil {
			return false, err
		}
		b.log.Info("settlement executed on chain (resolved from the recorded broadcast)", "job", jobID, "tx", bc.TxHash)
	} else {
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementReverted, EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": bc.TxHash, "block": out.Block, "gas_used": out.GasUsed,
				"note": "acceptDelivery MINED AND REVERTED — not settled; owner attention required",
			},
		}); err != nil {
			return false, err
		}
		b.log.Warn("settlement reverted on chain (resolved from the recorded broadcast)", "job", jobID, "tx", bc.TxHash)
	}
	for i, other := range group {
		if i == mined {
			continue
		}
		if _, err := b.store.Append(ctx, store.Event{
			Kind: settlementDropped, EntityID: jobID, Actor: "funding",
			Payload: map[string]any{
				"tx_hash": other.TxHash, "nonce": other.Nonce, "taken_by": bc.TxHash,
				"note": "another acceptDelivery at the same nonce mined, so this one never can",
			},
		}); err != nil {
			return false, err
		}
	}
	if !out.Reverted {
		b.recordMilestoneObservation(ctx, jobID)
	}
	return !out.Reverted, nil
}

// replaceStalled fee-replaces the newest transaction at a nonce the chain is not mining:
// still held by the node, or evicted and not re-sendable. It acts only once that transaction
// has sat for replaceAfterBlocks since it was signed (its sent_head; for a record without
// one, since this process first saw it stall). The replacement is acceptDelivery again, at
// the SAME nonce with a higher fee, recorded write-ahead as a settlement.submitted that
// `replaces` it; if the record cannot be written nothing is sent. At most one transaction
// per nonce can mine, so no number of replacements can settle twice. Past maxReplacements,
// or when the lane refuses for good (another key signed it, the fee cap, a record whose
// bytes are not its transaction, no signer at all), a settlement.stuck naming the reason is
// recorded (once per transaction) and logged at ERROR, and nothing more is replaced.
//
// It never fails the caller for a replacement that did not happen: the broadcast stays open
// and the next resolution looks again. It returns only a store error.
func (b *Brain) replaceStalled(ctx context.Context, jobID, vaultJobID string, group []openBroadcast, chainLen int, head uint64, why string) error {
	newest := group[len(group)-1]
	if newest.stuck {
		// Already recorded stuck: nothing more is replaced automatically.
		return nil
	}
	key := strings.ToLower(newest.TxHash)
	b.mu.Lock()
	after, maxRepl := b.replaceAfterBlocks, b.maxReplacements
	since := newest.SentHead
	if since == 0 {
		h, ok := b.stallSince[key]
		if !ok {
			h = head
			b.stallSince[key] = h
		}
		since = h
	}
	b.mu.Unlock()
	if head < since+after {
		return nil
	}
	if chainLen-1 >= maxRepl {
		return b.recordStuck(ctx, jobID, newest, head, chainLen-1, stuckMaxReplacements, why,
			"acceptDelivery is still unmined after the maximum number of fee replacements at its nonce")
	}
	// The kill switch stops settlements, a replacement included.
	if ferr := b.frozenErr(jobID, ""); ferr != nil {
		b.log.Warn("a stalled acceptDelivery is not replaced while frozen", "job", jobID, "tx", newest.TxHash)
		return nil
	}
	if b.funding == nil || vaultJobID == "" {
		return nil
	}
	if ctx.Err() != nil {
		// The pass ran out of time (the bounded boot pass): sign nothing now, the next pass looks again.
		return nil
	}
	recorded := false
	repl, err := b.funding.ReplaceSettlement(context.WithoutCancel(ctx), vaultJobID, newest.Broadcast, func(bc funding.Broadcast) error {
		if err := b.recordSubmitted(ctx, jobID, bc, newest.TxHash); err != nil {
			b.log.Error("a replacement acceptDelivery was signed but its record failed; it is NOT sent",
				"job", jobID, "replaces", newest.TxHash, "err", err)
			return err
		}
		recorded = true
		return nil
	})
	switch {
	case err == nil:
		b.log.Warn("a stalled acceptDelivery was replaced at the same nonce with a higher fee",
			"job", jobID, "replaces", newest.TxHash, "tx", repl.TxHash, "nonce", newest.Nonce, "why", why)
	case recorded:
		b.log.Warn("a replacement acceptDelivery is recorded but its send was not acknowledged; it is resolved by hash",
			"job", jobID, "replaces", newest.TxHash, "tx", repl.TxHash, "err", err)
	case stuckReasonOf(err) != "":
		// The lane will never replace this transaction: asking again every pass changes nothing.
		return b.recordStuck(ctx, jobID, newest, head, chainLen-1, stuckReasonOf(err), why,
			"acceptDelivery cannot be fee-replaced by the daemon's lane: "+err.Error())
	default:
		b.log.Warn("a stalled acceptDelivery could not be replaced; it stays open", "job", jobID, "tx", newest.TxHash, "err", err)
	}
	return nil
}

// stuckReasonOf names why the lane refused a replacement for good, "" for a refusal that may
// pass (an RPC error, a nonce already used, a refused record).
func stuckReasonOf(err error) string {
	switch {
	case errors.Is(err, funding.ErrSignerMismatch):
		return stuckSignerMismatch
	case errors.Is(err, funding.ErrFeeCapExceeded):
		return stuckFeeCap
	case errors.Is(err, funding.ErrBroadcastMismatch):
		return stuckRecordMismatch
	case errors.Is(err, funding.ErrSignerUnknown):
		return stuckSignerUnknown
	}
	return ""
}

// recordStuck writes the one settlement.stuck for a transaction the daemon will not replace
// again, naming the reason, and logs it at ERROR.
func (b *Brain) recordStuck(ctx context.Context, jobID string, newest openBroadcast, head uint64, replacements int, reason, why, note string) error {
	if _, err := b.store.Append(ctx, store.Event{
		Kind: settlementStuck, EntityID: jobID, Actor: "funding",
		Payload: map[string]any{
			"tx_hash": newest.TxHash, "nonce": newest.Nonce, "head": head, "replacements": replacements, "reason": reason,
			"note": note + "; owner attention required (see the H2 settlement recovery note)",
		},
	}); err != nil {
		return err
	}
	b.log.Error("SETTLEMENT STUCK: acceptDelivery will not be replaced again; owner attention required",
		"job", jobID, "tx", newest.TxHash, "nonce", newest.Nonce, "replacements", replacements, "reason", reason, "why", why)
	return nil
}

// What confirmUnseen did with one observation of an unseen broadcast.
type unseenOutcome int

const (
	unseenWaiting          unseenOutcome = iota // a first look, or the chain has not moved: nothing acted on
	unseenConfirmedDropped                      // dropped, confirmed and recorded: it can never mine
	unseenResent                                // evicted, confirmed, and its same bytes were sent again
	unseenNotResent                             // evicted, confirmed, but not re-sent (frozen, no bytes, refused, or re-sent the maximum number of times)
)

// confirmUnseen handles one observation of a broadcast that has no receipt and that the node
// does not know. A single such look is not believed: through a load-balanced RPC a mined
// transaction can look exactly like this (the nonce backend ahead of the receipt backend).
// The first look is recorded (settlement.unseen, with its verdict and chain head) so it
// survives a restart; only a second look with the same verdict at a strictly higher head is
// acted on. A confirmed "dropped" is recorded as settlement.dropped. A confirmed "evicted"
// re-sends the recorded signed bytes unchanged, which cannot settle twice (same nonce, same
// hash), and the broadcast stays open; at most maxRebroadcasts times per transaction, after
// which it is left to fee replacement (or settlement.stuck).
func (b *Brain) confirmUnseen(ctx context.Context, jobID string, bc openBroadcast, res funding.Resolution) (unseenOutcome, error) {
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
			return unseenWaiting, err
		}
		b.log.Warn("a recorded acceptDelivery is unseen by the node; waiting for a second observation",
			"job", jobID, "tx", bc.TxHash, "verdict", verdict, "head", res.Head)
		return unseenWaiting, nil
	}
	if res.Head <= bc.seen.head {
		// The chain has not moved since the first look: this one adds nothing.
		return unseenWaiting, nil
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
			return unseenWaiting, err
		}
		b.log.Warn("a recorded acceptDelivery was dropped; the settlement may be resubmitted", "job", jobID, "tx", bc.TxHash)
		return unseenConfirmedDropped, nil
	}
	// Evicted, confirmed. The kill switch stops settlements, a re-send included.
	if ferr := b.frozenErr(jobID, ""); ferr != nil {
		b.log.Warn("an evicted acceptDelivery is not re-sent while frozen", "job", jobID, "tx", bc.TxHash)
		return unseenNotResent, nil
	}
	if len(bc.Raw) == 0 {
		b.log.Error("an evicted acceptDelivery has no recorded signed bytes and cannot be re-sent; "+
			"it is replaced at its nonce once stalled long enough (see the H2 settlement recovery note)",
			"job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce)
		return unseenNotResent, nil
	}
	b.mu.Lock()
	maxRe := b.maxRebroadcasts
	b.mu.Unlock()
	if bc.rebroadcasts >= maxRe {
		b.log.Warn("an evicted acceptDelivery was already re-sent the maximum number of times; it is replaced at its nonce instead",
			"job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce, "rebroadcasts", bc.rebroadcasts)
		return unseenNotResent, nil
	}
	if err := b.funding.RebroadcastSettlement(ctx, bc.Broadcast); err != nil {
		// Nothing new is out; the next resolution looks again.
		b.log.Warn("re-sending an evicted acceptDelivery failed; it stays open", "job", jobID, "tx", bc.TxHash, "err", err)
		return unseenNotResent, nil
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: settlementRebroadcast, EntityID: jobID, Actor: "funding",
		Payload: map[string]any{
			"tx_hash": bc.TxHash, "nonce": bc.Nonce, "head": res.Head,
			"note": "the evicted acceptDelivery's recorded signed bytes were sent again, unchanged (same nonce, same hash)",
		},
	}); err != nil {
		return unseenWaiting, err
	}
	b.log.Warn("an evicted acceptDelivery was re-sent unchanged", "job", jobID, "tx", bc.TxHash, "nonce", bc.Nonce)
	return unseenResent, nil
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

// recordSubmitted writes the write-ahead settlement.submitted record for a signed, not yet
// sent, acceptDelivery. replaces names the transaction a fee replacement takes over from at
// the same nonce ("" for a first submission).
func (b *Brain) recordSubmitted(ctx context.Context, jobID string, bc funding.Broadcast, replaces string) error {
	payload := map[string]any{
		"tx_hash": bc.TxHash, "nonce": bc.Nonce, "signer": signerDaemonLane,
		"note": "acceptDelivery signed and recorded before it is sent; it may be out and may still mine until a later record resolves it",
	}
	if bc.Signer != (common.Address{}) {
		payload["signer_address"] = bc.Signer.Hex()
	}
	if len(bc.Raw) > 0 {
		payload["raw_tx"] = "0x" + hex.EncodeToString(bc.Raw)
	}
	if bc.SentHead > 0 {
		payload["sent_head"] = bc.SentHead
	}
	if replaces != "" {
		payload["replaces"] = replaces
		payload["note"] = "a fee replacement of a stalled acceptDelivery at the same nonce, signed and recorded before it is sent"
	}
	_, err := b.store.Append(context.WithoutCancel(ctx), store.Event{
		Kind: settlementSubmitted, EntityID: jobID, Actor: "funding", Payload: payload,
	})
	return err
}

// settleOnChain is the chain half of an authenticated, claimed Accept: submit
// JobVault.acceptDelivery through Funding's CUSTOMER lane (SC-JV-005 — only the
// customer settles; the demo lane is daemon-custodial, stated openly) and record the
// outcome distinctly. No lane or no chain identity = the honest pending stop; a
// REVERT is mined-and-failed — surfaced to the owner, never recorded as settled.
//
// The transaction is recorded WRITE-AHEAD (settlement.submitted) once it is signed and
// before it is sent: the lane signs locally, so its hash, nonce, signer and bytes are known
// before any node sees them. If that record cannot be written, nothing is sent and the
// Accept fails with nothing out. Once it is written, any failure after it (the send RPC
// timing out after the node accepted the bytes, an error from one backend of a
// load-balanced endpoint while another took them, the receipt wait failing) leaves an open
// record the next resolution asks about BY HASH; a send error is never read as "not sent".
// rec is the job's prior history (zero on the first attempt): it names the lane's own
// earlier transactions and suppresses a second, identical pending record.
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
	var recorded *funding.Broadcast
	onSigned := func(bc funding.Broadcast) error {
		if err := b.recordSubmitted(ctx, jobID, bc, ""); err != nil {
			b.log.Error("acceptDelivery was signed but its record could not be written; it is NOT sent",
				"job", jobID, "tx", bc.TxHash, "err", err)
			return err
		}
		recorded = &bc
		return nil
	}
	out, err := b.funding.SettleOnChain(context.WithoutCancel(ctx), jm.VaultJobID, onSigned)
	if err != nil {
		if recorded != nil {
			// Recorded before it was sent. Whether it reached a node and whether it mines is
			// the chain's to say, and the next resolution asks about this exact transaction
			// before anything else; nothing new is submitted meanwhile.
			b.log.Warn("acceptDelivery is recorded but its send or receipt was not confirmed; it is resolved by hash",
				"job", jobID, "tx", recorded.TxHash, "nonce", recorded.Nonce, "err", err)
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
		b.log.Warn("cannot read on-chain settlement; nothing is recorded or submitted", "job", jobID, "err", err)
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
