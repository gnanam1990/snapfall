// Package advancing is the advance's authorization path — the snap's daemon half,
// mirroring how the settlement (the fall) stops at the chain.
//
// FLOW: Brain proposes an advance intent (Kind "advance") → SubmitAdvance enters the
// approval lifecycle PRE-MARKED HumanApprovalRequired (policy.Evaluate is skipped by
// design; its rule 0 denies the kind by law if ever misrouted) → the owner approves in
// the SAME H2 inbox as every payment — that confirmation IS the 0:30 demo beat → the
// lifecycle mints the Grant behind its full gate set (hash re-verify, expiry, policy
// version, atomic freeze admission, durable write-ahead claim, exactly-once) → Funding
// records the request_advance instruction → the HONEST STOP: advance.pending_chain.
// No deployment exists, so FloatPool.requestAdvance cannot be submitted; when it can,
// the snap is one chain call away, exactly like the fall.
//
// RESTART POSTURE (the AT-09 crash case): an approved-but-unexecuted advance is NEVER
// auto-executed after a restart — EscalateInterrupted surfaces it to the owner
// (advance.interrupted) and the owner re-proposes; an EXECUTED advance can never
// re-execute (the lifecycle's durable payment.executing claim survives replay).
//
// KEY DISCIPLINE: this package holds no key and logs no secret — the chain signer
// arrives with the write path, inside Funding, behind this same Grant gate.
package advancing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Flow drives one advance from proposal to its terminal outcome.
type Flow struct {
	life   *approval.Lifecycle
	st     *store.Store
	fund   *funding.Agent
	log    *slog.Logger
	orgID  string
	window time.Duration

	// oracle, when set, answers the crash-window question from CHAIN STATE (never a
	// heuristic): SC-FP-003 permits one advance per job, so the chain says whether a
	// claimed-but-unconfirmed advance actually landed.
	oracle AdvanceOracle

	// rateOracle reads the org's CURRENT advance rate (bps) from chain at intent
	// creation, so the amount the owner approves matches what the contract draws.
	// nil = fall back to the base rate.
	rateOracle RateOracle

	// afterExecute is a TEST-ONLY hook (nil in production), invoked after the await
	// goroutine reaches its terminal outcome — tests await it deterministically.
	afterExecute func()
}

// RateOracle reads the org's current advance rate in basis points from chain state.
// Returns false when unavailable (no chain wired, RPC error, out-of-range).
type RateOracle func(ctx context.Context) (uint16, bool)

// SetRateOracle wires the on-chain advance-rate reader (main.go, from chain.Oracle).
func (f *Flow) SetRateOracle(o RateOracle) { f.rateOracle = o }

// AdvanceOracle is the chain-state reader the restart recovery consults
// (chain.Oracle satisfies it; tests fake it).
type AdvanceOracle interface {
	AdvanceLanded(ctx context.Context, vaultJobID string) (bool, error)
}

// SetOracle wires the restart oracle.
func (f *Flow) SetOracle(o AdvanceOracle) { f.oracle = o }

// New wires the flow. window bounds how long an advance proposal awaits its decision.
func New(life *approval.Lifecycle, st *store.Store, fund *funding.Agent, log *slog.Logger, orgID string, window time.Duration) *Flow {
	return &Flow{life: life, st: st, fund: fund, log: log, orgID: orgID, window: window}
}

// AdvanceRate is the demo advance rate: 50% of the escrowed quote (rate(org)'s base;
// the live rate is a chain read the write path adds — stated, not simulated). The fee
// is computed ON CHAIN by FloatPool at submission; the intent carries principal only.
const AdvanceRateBps = 5_000

// Propose opens the human-authorized advance request and spawns the await that
// executes on approval. Returns the pending request (its ID lands in the H2 inbox).
func (f *Flow) Propose(ctx context.Context, jobID, vaultJobID, quoteUSDC string) (approval.Request, error) {
	// Read the org's CURRENT advance rate from chain at intent-creation time, so the
	// amount the owner approves matches what FloatPool.requestAdvance will draw. A
	// hardcoded 50% diverged from the real rate the moment the org's rate climbed above
	// the base (rate only ever climbs). Falls back to the base rate if unavailable.
	//
	// KNOWN LIMIT (logged for a post-recording design decision, not solved here): the
	// contract recomputes the advance at EXECUTION time, so if the rate climbs between
	// approval and execution (another job settles in the window), the draw exceeds the
	// approved amount. The AT-05-shaped answer — verify the rate hasn't moved at
	// execution and refuse if it has, or label the intent figure advisory — is a real
	// design call deferred deliberately.
	rateBps := int64(AdvanceRateBps)
	if f.rateOracle != nil {
		if bps, ok := f.rateOracle(ctx); ok {
			rateBps = int64(bps)
		}
	}
	principal, err := principalMicros(quoteUSDC, rateBps)
	if err != nil {
		return approval.Request{}, fmt.Errorf("advance proposal: %w", err)
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return approval.Request{}, err
	}
	res, err := f.life.SubmitAdvance(ctx, approval.Intent{
		IntentID: "adv_" + hex.EncodeToString(nonce[:6]),
		OrgID:    f.orgID, JobID: jobID, AgentID: "funding",
		Kind:     policy.KindAdvance,
		ChainRef: vaultJobID,
		Resource: "FloatPool.requestAdvance",
		Purpose: fmt.Sprintf("working-capital advance: %d%% of the %s USDC escrowed receivable (fee computed on-chain at submission)",
			rateBps/100, quoteUSDC),
		AmountMicros: principal, MaxAmountMicros: principal,
		Nonce:     "0x" + hex.EncodeToString(nonce),
		ExpiresAt: time.Now().Add(f.window),
	})
	if err != nil {
		return approval.Request{}, err
	}
	// The await goroutine outlives THIS call — it waits for a separate owner-approval
	// request, which may arrive long after Propose returns. It must NOT be bound to the
	// caller's context: the owner-initiated HTTP path passes r.Context(), which is
	// cancelled the instant the proposal POST returns, and a request-scoped await would
	// exit before the approval ever lands (approved-but-never-executed — the silent
	// money-path no-op). Detach: keep values, drop the caller's cancellation. The
	// deadline timer bounds the wait; a crash is covered by EscalateInterrupted. This is
	// the G8 rootCtx lesson, applied to the advance flow.
	go f.await(context.WithoutCancel(ctx), *res.Request)
	return *res.Request, nil
}

// await executes the advance when (and only when) the owner approves.
func (f *Flow) await(ctx context.Context, req approval.Request) {
	defer func() {
		if f.afterExecute != nil {
			f.afterExecute()
		}
	}()
	deadline := time.NewTimer(time.Until(req.ExpiresAt))
	defer deadline.Stop()
	select {
	case <-ctx.Done():
		return // daemon shutdown; restart posture takes over (never auto-executed)
	case <-deadline.C:
		return // expired unanswered; the lifecycle expires it on its own terms
	case <-f.life.DecisionSignal(req.ID):
	}
	snap, ok := f.life.Snapshot(req.ID)
	if !ok || snap.State != approval.StateApproved {
		return // rejected or alternative-requested: no execution path exists
	}

	// Mirror the Purchaser's shield: honor cancellation only BEFORE execution begins;
	// past this point the lifecycle's durable claim decides, not a context.
	if ctx.Err() != nil {
		return
	}
	execErr := f.life.Execute(context.WithoutCancel(ctx), snap.Intent, req.ID, func(ectx context.Context, g approval.Grant) error {
		out, err := f.fund.ExecuteAdvance(ectx, g)
		if err != nil {
			return err
		}
		return f.recordAdvanceOutcome(ectx, g, out)
	})
	if execErr != nil {
		// Refused (freeze, expiry, policy change) or failed: visible, never silent.
		if _, err := f.st.Append(context.WithoutCancel(ctx), store.Event{
			Kind: "advance.halted", EntityID: req.JobID, Actor: "funding",
			Payload: map[string]any{"request_id": req.ID, "error": execErr.Error()},
		}); err != nil {
			f.log.Error("advance halt event append failed", "job", req.JobID, "err", err)
		}
		f.log.Warn("advance not executed", "job", req.JobID, "request", req.ID, "err", execErr)
	}
}

// recordAdvanceOutcome makes the chain outcome durable, distinctly — success
// (advance.executed), REVERTED (advance.reverted: mined and failed, surfaced to the
// owner, never recorded as done), or the honest pending_chain stop when no lane or no
// chain identity is wired.
func (f *Flow) recordAdvanceOutcome(ectx context.Context, g approval.Grant, out funding.ChainOutcome) error {
	pending := func(note string) error {
		_, err := f.st.Append(ectx, store.Event{
			Kind: "advance.pending_chain", EntityID: g.Intent().JobID, Actor: "funding",
			Payload: map[string]any{
				"request_id": g.RequestID(), "principal_micros": g.Intent().AmountMicros, "note": note,
			},
		})
		return err
	}
	if !out.Submitted {
		return pending("advance approved; no treasury chain lane or on-chain job identity — submission pending")
	}
	if out.Reverted {
		if _, aerr := f.st.Append(ectx, store.Event{
			Kind: "advance.reverted", EntityID: g.Intent().JobID, Actor: "funding",
			Payload: map[string]any{
				"request_id": g.RequestID(), "tx_hash": out.TxHash, "block": out.Block, "gas_used": out.GasUsed,
				"note": "requestAdvance MINED AND REVERTED — not an advance; owner attention required",
			},
		}); aerr != nil {
			f.log.Error("revert event append failed", "err", aerr)
		}
		return fmt.Errorf("requestAdvance reverted on chain (tx %s)", out.TxHash)
	}
	_, err := f.st.Append(ectx, store.Event{
		Kind: "advance.executed", EntityID: g.Intent().JobID, Actor: "funding",
		Payload: map[string]any{
			"request_id": g.RequestID(), "tx_hash": out.TxHash, "block": out.Block, "gas_used": out.GasUsed,
			"principal_micros": g.Intent().AmountMicros,
		},
	})
	return err
}

// EscalateInterrupted surfaces advance requests that survived a restart in a
// non-terminal-executed state: approved-but-unexecuted advances are NEVER auto-executed
// (the await goroutine died with the old process; re-running money movement on boot is
// the AT-09 hazard) — the owner is told and re-proposes. Call once at startup, after
// Lifecycle.Recover.
func (f *Flow) EscalateInterrupted(ctx context.Context) (int, error) {
	escalated := 0
	for _, req := range f.life.Requests() {
		if req.Intent.Kind != policy.KindAdvance {
			continue
		}
		if req.Executed {
			// THE CRASH WINDOW: claim durable, outcome unknown (the process may have
			// died between submission and receipt). The chain is the oracle — never a
			// heuristic: SC-FP-003 permits one advance per job, so openAdvanceOf
			// answers definitively. No outcome record + oracle says landed → record
			// it; says not landed (or no oracle/no chain ref) → escalate to the owner.
			n, err := f.resolveExecutedClaim(ctx, req)
			if err != nil {
				return escalated, err
			}
			escalated += n
			continue
		}
		if req.State == approval.StateRejected || req.State == approval.StateExpired ||
			req.State == approval.StateAlternativeRequested {
			continue
		}
		if _, err := f.st.Append(ctx, store.Event{
			Kind: "advance.interrupted", EntityID: req.JobID, Actor: "funding",
			Payload: map[string]any{
				"request_id": req.ID, "state": string(req.State),
				"note": "advance proposal interrupted by a restart; not resumed — re-propose if still wanted",
			},
		}); err != nil {
			return escalated, err
		}
		escalated++
	}
	return escalated, nil
}

// resolveExecutedClaim closes one executed-claim request: 0 or 1 escalations.
func (f *Flow) resolveExecutedClaim(ctx context.Context, req approval.Request) (int, error) {
	var outcomes int
	if err := f.st.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events
		WHERE kind IN ('advance.executed','advance.pending_chain','advance.reverted')
		  AND payload_json LIKE ?`, "%"+req.ID+"%").Scan(&outcomes); err != nil {
		return 0, err
	}
	if outcomes > 0 {
		return 0, nil // outcome already durable; nothing to resolve
	}
	if f.oracle != nil && req.Intent.ChainRef != "" {
		landed, err := f.oracle.AdvanceLanded(ctx, req.Intent.ChainRef)
		if err == nil && landed {
			_, aerr := f.st.Append(ctx, store.Event{
				Kind: "advance.executed", EntityID: req.JobID, Actor: "funding",
				Payload: map[string]any{
					"request_id": req.ID, "principal_micros": req.Intent.AmountMicros,
					"recovered_from_chain": true,
					"note":                 "claim survived a crash; the chain confirms the advance landed (openAdvanceOf)",
				},
			})
			return 0, aerr
		}
		// Oracle errors fall through to escalation: uncertain = owner decides.
	}
	_, err := f.st.Append(ctx, store.Event{
		Kind: "advance.interrupted", EntityID: req.JobID, Actor: "funding",
		Payload: map[string]any{
			"request_id": req.ID, "state": string(req.State),
			"note": "executed claim with no outcome record and no chain confirmation — owner attention required; never auto-resubmitted",
		},
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

// principalMicros converts a human-facing USDC quote to the 50% advance principal in
// micros, exactly (no floats near money).
func principalMicros(quoteUSDC string, rateBps int64) (int64, error) {
	if rateBps <= 0 || rateBps > 10_000 {
		return 0, fmt.Errorf("advance rate %d bps out of range", rateBps)
	}
	parts := strings.SplitN(strings.TrimSpace(quoteUSDC), ".", 2)
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok || whole.Sign() < 0 {
		return 0, fmt.Errorf("invalid quote %q", quoteUSDC)
	}
	micros := new(big.Int).Mul(whole, big.NewInt(1_000_000))
	if len(parts) == 2 {
		frac := parts[1]
		if frac == "" || len(frac) > 6 {
			return 0, fmt.Errorf("invalid quote %q", quoteUSDC)
		}
		f, ok := new(big.Int).SetString(frac+strings.Repeat("0", 6-len(frac)), 10)
		if !ok {
			return 0, fmt.Errorf("invalid quote %q", quoteUSDC)
		}
		micros.Add(micros, f)
	}
	principal := micros.Mul(micros, big.NewInt(rateBps)).Div(micros, big.NewInt(10_000))
	if !principal.IsInt64() || principal.Int64() <= 0 {
		return 0, fmt.Errorf("advance principal out of range for quote %q", quoteUSDC)
	}
	return principal.Int64(), nil
}
