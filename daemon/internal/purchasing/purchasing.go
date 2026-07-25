// Package purchasing is the concrete brain.Purchaser: it routes a worker's Brain-stamped
// purchase intent through the REAL policy + approval pipeline, prices it against the
// seller's own x402 challenge, and, since V6, pays it through the H3 sidecar lane. It is
// the dependency the DD-worker's adaptation logic (AT-04) is tested against: a genuine
// policy decision, not a stub that always answers the same way.
//
// WHAT IS REAL here: the pre-intake quote (H3 §7 step 1), the policy evaluation, the
// escalation to HumanApprovalRequired, the owner's real approve/reject decision (with its
// structured reason), the injected-clock expiry, the freeze gate inside approval.Execute,
// the payment itself, and the budget hold's release or commit.
//
// WHAT IS STILL HONEST ABOUT BEING UNWIRED. With no payment lane wired, an approved
// purchase runs every execution gate and stops at purchase.pending_settlement with Status
// "approved-pending-integration", no data and no receipt, never a fabricated buy. That
// stop is not legacy scaffolding, it is the daemon's degraded mode, and four readers
// depend on its exact shape: integration/g8_adaptive_test.go's settlementAmounts, this
// package's own auto-approve test, billing's expense gap, and the dashboard activity feed.
// It is preserved key-for-key and pinned by TestV6_NoPayerStopsAtPendingSettlement.
//
// THE RELEASE RULE LIVES IN EXACTLY ONE PLACE (V6 §3.5). A payment failure releases the
// budget hold if and only if funding's classification proves nothing was signed
// (*h3.Error with PreSign() true, which covers both the sidecar's pre-sign 4xx codes and
// funding's own local refusals). A post-sign failure keeps the hold: the signed
// authorization is a bearer instrument that may still settle, so releasing it and letting
// a retry re-mint would risk spending the same budget twice. Resolving those is the boot
// status reconciler's job and the owner's, never this package's (H3 §7 step 4).
//
// THE TWO NAMING CONVENTIONS CROSS UPSTREAM OF HERE, and this package must keep them
// apart: Intent.Merchant / Intent.Resource stay policy-facing (the allowlisted HOST and
// the display string), while the quote's accept.payTo / accept.asset / accept.network and
// the absolute resource URL are copied VERBATIM into Intent.PayTo / Asset / Network /
// ResourceURL, where they are hash-bound into the approval the owner gives. Verbatim is
// load-bearing: the sidecar's payee check is case-insensitive but intentHash is not.
package purchasing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/budget"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// Event kinds this package writes. purchase.pending_settlement is FROZEN in name and in
// payload keys (see the package doc); the three V6 additions are records of what the
// payment lane actually did.
//
// EntityID: purchase.pending_settlement keys on the REQUEST id, and it stays that way
// because four readers already join on it. The V6 additions key on the JOB id, which is
// the log's majority convention (V6 §1.F) and what the ledger's fold predicate uses; the
// request id rides every payload either way, so nothing has to choose.
const (
	kindPendingSettlement = "purchase.pending_settlement"
	kindDelivered         = "purchase.delivered"
	kindUnresolved        = "purchase.unresolved"
	kindPayeeDivergence   = "purchase.payee_divergence"
	actorPurchaser        = "purchaser"
)

// Release codes this package records against a hold. They are machine-readable, they are
// not H3 codes, and they say WHY the money was never going to move.
const (
	// codeOwnerDeclined releases the hold behind a rejected or alternative-requested
	// approval. Nothing was executed, so nothing was signed.
	codeOwnerDeclined = "OWNER_DECLINED"
	// codeNoPayerWired releases the hold behind the honest stop. Submitted=false with a
	// nil error is positive proof that no request left the process.
	codeNoPayerWired = "NO_PAYER_WIRED"
)

// Clock is injected time with a DEADLINE notifier. The Purchaser's expiry wake fires from
// the SAME clock the approval lifecycle enforces expiry on, no second independent timeout.
// The real clock uses a timer; a fake clock (tests) releases deadlines on Advance.
type Clock interface {
	Now() time.Time
	// Deadline returns a channel that receives once the clock reaches t (or immediately if
	// t is already past). Used in a select so a blocked Purchase wakes at the expiry.
	Deadline(t time.Time) <-chan struct{}
}

// RealClock is the production Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) Deadline(t time.Time) <-chan struct{} {
	ch := make(chan struct{})
	d := time.Until(t)
	if d <= 0 {
		close(ch)
		return ch
	}
	time.AfterFunc(d, func() { close(ch) })
	return ch
}

// Purchaser implements brain.Purchaser over the real policy+approval pipeline.
type Purchaser struct {
	life  *approval.Lifecycle
	store *store.Store
	// fund is the money boundary. This package never holds a payment client, a bearer
	// token or an HMAC key: it holds the Agent, whose only payment door demands the
	// approval-minted Grant (AT-15's placement, see funding's package doc).
	fund *funding.Agent
	// led is the reservation ledger. nil = no budget accounting, the pre-V6 behaviour.
	led    *budget.Ledger
	clock  Clock
	orgID  string
	window time.Duration // how long a HumanApprovalRequired request stays open
	// afterSubmit is a TEST-ONLY hook (nil in production) invoked with the request ID right
	// after Submit, so a test can drive the owner's decision deterministically instead of
	// polling for the pending request. Unexported, no setter, inert in production.
	afterSubmit func(reqID string)
}

// New builds the Purchaser. window is the approval expiry (SEC-006).
//
// fund and led are both optional and independently so, because a purchase must degrade in
// steps rather than all at once: a nil fund (or one with no payer wired) stops honestly at
// purchase.pending_settlement, and a nil led means no budget is held or released, which is
// exactly the pre-V6 behaviour every existing wiring had.
func New(life *approval.Lifecycle, st *store.Store, fund *funding.Agent, led *budget.Ledger,
	clock Clock, orgID string, window time.Duration) *Purchaser {
	return &Purchaser{life: life, store: st, fund: fund, led: led, clock: clock, orgID: orgID, window: window}
}

// Decide runs one purchase through quote + policy + approval + payment and returns the
// structured outcome. It implements brain.Purchaser. jobID/agentKind on the intent were
// stamped by Brain.
func (p *Purchaser) Decide(ctx context.Context, in brain.PurchaseIntent) (worker.PurchaseOutcome, error) {
	nonce, err := freshNonce()
	if err != nil {
		return worker.PurchaseOutcome{}, err
	}
	now := p.clock.Now()
	intent := approval.Intent{
		IntentID:        "pi_" + nonce[2:14],
		OrgID:           p.orgID,
		JobID:           in.JobID,
		AgentID:         in.AgentKind,
		Merchant:        in.Merchant,
		Resource:        in.Resource,
		AmountMicros:    in.AmountMicros,
		MaxAmountMicros: in.MaxAmountMicros,
		Purpose:         in.Purpose,
		Nonce:           nonce,
		ExpiresAt:       now.Add(p.window),
		AlternativeTo:   in.AlternativeTo,
	}
	// An unset ceiling means "the amount" (V6 §1.B). Normalizing it HERE is what keeps the
	// payment door's hash-bound invariant satisfiable: the door refuses a ceiling that
	// differs from the evaluated amount, because rule 1 is fed AmountMicros only, and a
	// zero ceiling would fail PRICE_EXCEEDS_RESERVED at the sidecar.
	if intent.MaxAmountMicros == 0 {
		intent.MaxAmountMicros = intent.AmountMicros
	}

	// H3 §7 step 1: quote BEFORE intake, so the price the owner approves is the seller's
	// live price and the payee is bound into the approval rather than chosen after it.
	bound, refusal, ok := p.quoteIntoIntent(ctx, intent)
	if !ok {
		// Refused before intake: no nonce claimed, no approval opened, no budget held. The
		// worker gets a structured reason it can adapt to.
		return refusal, nil
	}
	intent = bound

	res, err := p.life.Submit(ctx, intent)
	if err != nil {
		if errors.Is(err, approval.ErrDenied) {
			return denyOutcome(res.Decision), nil
		}
		return worker.PurchaseOutcome{}, err
	}
	if res.Request != nil && p.afterSubmit != nil {
		p.afterSubmit(res.Request.ID)
	}

	switch res.Decision.Outcome {
	case policy.Deny:
		return denyOutcome(res.Decision), nil
	case policy.AutoApprove:
		// Execute against the BOUND intent (Submit set PolicyVersion on it) or the AT-05
		// hash check refuses it.
		return p.execute(ctx, res.Request.Intent, res.Request.ID, res.Decision)
	case policy.HumanApprovalRequired:
		return p.awaitAndExecute(ctx, res.Request.Intent, res.Request)
	default:
		return worker.PurchaseOutcome{}, fmt.Errorf("purchasing: unknown outcome %q", res.Decision.Outcome)
	}
}

// quoteIntoIntent runs the H3 §7 step 1 quote and returns the intent with the
// quote-derived, HASH-BOUND wire fields populated: accept.payTo, accept.asset,
// accept.network and the absolute resource URL, all copied verbatim, plus the quoted price
// as the evaluated amount.
//
// ok=false returns a structured refusal to report instead: a seller that cannot be priced
// is not a purchase, and refusing before intake costs nothing, no nonce claim, no
// approval record, no budget hold, no owner prompt.
//
// With no lane wired the quote answers with an EMPTY accept.payTo and a nil error (funding
// documents that as its only safe discriminator, since internal/h3 refuses a 200 whose
// challenge has no payee). The intent then keeps its Phase-1 stub shape and the flow ends
// at purchase.pending_settlement, exactly as it did before V6.
func (p *Purchaser) quoteIntoIntent(ctx context.Context, in approval.Intent) (approval.Intent, worker.PurchaseOutcome, bool) {
	if p.fund == nil {
		return in, worker.PurchaseOutcome{}, true
	}
	resourceURL := resolveURL(in.Resource, in.Merchant)
	q, err := p.fund.Quote(ctx, resourceURL)
	if err != nil {
		return in, refusedOutcome("quote-unavailable", fmt.Sprintf(
			"the seller could not be priced for %s: %v", nonEmpty(resourceURL, in.Resource), err)), false
	}
	if q.Accept.PayTo == "" {
		return in, worker.PurchaseOutcome{}, true // no lane: keep the stub shape
	}
	if q.Accept.Asset == "" || q.Accept.Network == "" {
		// A challenge missing either of these hashes an empty value into the approval and
		// then fails ASSET_CHANGED at pay time, which reads like a substituting seller
		// rather than an unusable quote. Refuse it while refusing is still free.
		return in, refusedOutcome("quote-unusable", fmt.Sprintf(
			"the challenge for %s names payee %s but asset %q and network %q: nothing payable can be built from it",
			resourceURL, q.Accept.PayTo, q.Accept.Asset, q.Accept.Network)), false
	}
	priced, err := h3.ParseAtomicMicros(q.Accept.Amount)
	if err != nil {
		return in, refusedOutcome("quote-unusable", fmt.Sprintf(
			"the quoted price for %s is unreadable: %v", resourceURL, err)), false
	}
	if priced <= 0 {
		return in, refusedOutcome("quote-unusable", fmt.Sprintf(
			"the seller quoted %d atomic USDC for %s: refusing to open an approval for a non-positive price",
			priced, resourceURL)), false
	}
	// The worker's own ceiling is a real constraint (discovery filters candidates by it, and
	// AT-04's adaptation re-queries strictly below a rejected amount). A seller pricing
	// above it is refused rather than silently overpaid: the worker asked for a cheaper
	// thing than the one on offer.
	if in.MaxAmountMicros > 0 && priced > in.MaxAmountMicros {
		return in, refusedOutcome("price-above-requested-cap", fmt.Sprintf(
			"the seller quoted %s USDC for %s but the request capped it at %s: refusing to spend above the requested ceiling",
			policy.FormatUSDC(priced), resourceURL, policy.FormatUSDC(in.MaxAmountMicros))), false
	}

	// VERBATIM, all four. The live payee/asset comparisons at the sidecar are
	// case-insensitive but intentHash is not, so a re-cased or checksummed copy passes the
	// check and hashes to something the sidecar never computed.
	in.PayTo = q.Accept.PayTo
	in.Asset = q.Accept.Asset
	in.Network = q.Accept.Network
	in.ResourceURL = resourceURL
	// The quoted price becomes the EVALUATED amount, and the ceiling equals it (V6 §1.B):
	// the policy engine tests exactly the figure the treasury will authorize.
	in.AmountMicros = priced
	in.MaxAmountMicros = priced
	return in, worker.PurchaseOutcome{}, true
}

// httpMethods are the tokens resolveURL tolerates in front of a legacy display resource.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
}

// resolveURL turns a catalog resource into the ABSOLUTE http(s) URL the sidecar probes.
//
// discovery's catalog now carries absolute URLs (discovery.go's V6 note), so the common
// path is identity, and identity matters: the string is hash-bound, so any normalization
// here would produce an intentHash the sidecar cannot reproduce. Two legacy shapes are
// tolerated because they still exist in tests and in older catalogs: a leading HTTP method
// token ("GET https://host/x"), which was never part of the address, and a bare path
// ("/v1/x"), which is resolved against the merchant HOST as a last resort.
//
// It returns "" when no URL can be built without inventing one. The caller then asks
// funding for a quote it will refuse by name, which is the honest failure: naming the
// unpriceable resource beats guessing an origin and blaming the seller.
func resolveURL(resource, merchant string) string {
	raw := strings.TrimSpace(resource)
	if i := strings.IndexByte(raw, ' '); i > 0 && httpMethods[strings.ToUpper(raw[:i])] {
		raw = strings.TrimSpace(raw[i+1:])
	}
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return raw // VERBATIM: hash-bound
	}
	if strings.HasPrefix(raw, "/") && merchant != "" && !strings.ContainsAny(merchant, " /") {
		return "https://" + merchant + raw
	}
	return ""
}

// awaitAndExecute blocks for the owner's decision OR the approval expiry, whichever comes
// first, then acts. The expiry wake is the SAME ExpiresAt approval enforces, driven by the
// injected clock: no second timeout, and the goroutine always terminates.
func (p *Purchaser) awaitAndExecute(ctx context.Context, intent approval.Intent, req *approval.Request) (worker.PurchaseOutcome, error) {
	select {
	case <-ctx.Done():
		// Shutdown (or caller cancellation) while awaiting the owner is a safe interruption.
		// No claim exists yet; the task ends and restart escalates it (serve pin 3). The
		// budget hold stays open and unclaimed, which is precisely the shape the expiry
		// sweeper reclaims (V6 §2.5 path 1), nothing is orphaned by returning here.
		return worker.PurchaseOutcome{}, fmt.Errorf("purchase interrupted awaiting the owner's decision: %w", ctx.Err())
	case <-p.life.DecisionSignal(req.ID):
		// The owner decided (or expiry was marked). Read the terminal state.
	case <-p.clock.Deadline(req.ExpiresAt):
		// The approval window elapsed on the injected clock, fall through to execute,
		// where the lifecycle's own expiry check returns ErrExpired (same clock, consistent).
	}

	snap, ok := p.life.Snapshot(req.ID)
	if !ok {
		return worker.PurchaseOutcome{}, approval.ErrUnknownRequest
	}
	switch snap.State {
	case approval.StateApproved:
		return p.execute(ctx, intent, req.ID, policy.Decision{Outcome: policy.HumanApprovalRequired})
	case approval.StateExpired:
		// The hold is unclaimed and provably dead; the expiry sweeper is its named owner
		// (V6 §2.5 path 1) and releasing here as well would blur that ownership.
		return expiredOutcome(), nil
	case approval.StateRejected, approval.StateAlternativeRequested:
		// The owner said no, so nothing was ever executed and nothing was signed: release
		// the hold NOW rather than pinning the job's budget until the window elapses. This
		// is the demo's rejection beat, the $4.00 hold has to come back before the
		// cheaper alternative is evaluated, or the adaptation is evaluated against a
		// budget that is still paying for the thing the owner declined.
		//
		// A failed release is swallowed DELIBERATELY: the owner's structured reason must
		// reach the worker verbatim (AT-04 asserts it byte-for-byte), and an unreleased
		// hold is not lost, it has no execution claim, so the sweeper reclaims it at
		// expiry.
		_ = p.release(ctx, req.ID, nonEmpty(snap.Reason, "the owner declined the purchase"), codeOwnerDeclined, true)
		return worker.PurchaseOutcome{
			Decision: string(policy.Deny), Status: "denied",
			Reason:    nonEmpty(snap.Reason, "owner declined the purchase"),
			Code:      "owner-" + string(snap.State),
			RequestID: req.ID, // the anchor a linked alternative's AlternativeTo points at
		}, nil
	case approval.StatePending:
		// We only wake on a decision (which leaves Pending) or the deadline. Still Pending
		// here means the deadline fired: the approval window elapsed on the injected clock.
		// (An owner approval that raced past expiry would show StateApproved and be caught
		// by Execute's own expiry check, same clock, consistent.)
		return expiredOutcome(), nil
	default:
		return worker.PurchaseOutcome{}, fmt.Errorf("purchasing: unexpected state %q", snap.State)
	}
}

// execute runs the approval Execute gates, INCLUDING the freeze gate, and then the
// payment. A freeze that engaged since the policy decision is refused HERE by the existing
// gate, before any Grant is minted.
func (p *Purchaser) execute(ctx context.Context, intent approval.Intent, reqID string, d policy.Decision) (worker.PurchaseOutcome, error) {
	// SHUTDOWN RULE (serve pin 2, the freeze in-flight ruling applied to SIGTERM):
	// cancellation is honored ONLY here, BEFORE the write-ahead claim, a safe refusal
	// that consumes nothing. Past this check, Execute runs under a shielded context:
	// cancellation can never abort between the claim and completion, because aborting
	// there recreates the exact double-pay hazard the in-flight ruling exists to avoid.
	if err := ctx.Err(); err != nil {
		return worker.PurchaseOutcome{}, fmt.Errorf("purchase interrupted before execution (no claim written): %w", err)
	}
	shielded := context.WithoutCancel(ctx)

	// paid is written by the executor before Execute returns (the executor runs
	// synchronously inside it), so reading it afterwards needs no synchronization.
	var paid funding.PaymentOutcome
	// bookkeeping records a durable-write failure that happened AFTER the money moved. It
	// is deliberately NOT the executor's error: see settle.
	var bookkeeping error

	execErr := p.life.Execute(shielded, intent, reqID, func(ctx context.Context, g approval.Grant) error {
		if p.fund == nil {
			// No money boundary wired at all (a degraded wiring, or a test of the pipeline
			// without the lane): the same honest stop, for the same reason.
			return p.pendingSettlement(ctx, g, intent)
		}
		out, err := p.fund.ExecutePayment(ctx, g)
		paid = out
		if err != nil {
			// THE RELEASE RULE, in one place, keyed on funding's classification and never
			// on a state or an HTTP status. PreSign() true means it is PROVABLE that no
			// authorization was signed, which covers the sidecar's pre-sign codes AND
			// funding's own local refusals, since both arrive as *h3.Error.
			var he *h3.Error
			if errors.As(err, &he) && he.PreSign() {
				if rerr := p.release(ctx, g.RequestID(), he.Message, he.Code, true); rerr != nil {
					return fmt.Errorf("%w (and the pre-sign hold could not be released; the expiry sweeper owns it now: %v)", err, rerr)
				}
			}
			// Post-sign, or unclassifiable: the hold STANDS. A signed authorization is a
			// bearer instrument that may still settle, so the boot status reconciler and
			// StalledSince own it from here (V6 §2.5 path 2 and 3, H3 §7 step 4).
			return err
		}
		if !out.Submitted {
			// THE HONEST STOP, preserved key-for-key: no payer wired, nothing moved.
			return p.pendingSettlement(ctx, g, intent)
		}
		bookkeeping = p.settle(ctx, g, intent, out)
		return nil
	})

	switch {
	case paid.Submitted && h3.Committed(paid.State):
		// The money MOVED and the sidecar's record is final. Report it as delivered even
		// when execErr is non-nil, because the only error reachable here is a failed
		// post-payment append (approval.Execute's audit-gap error): reporting a completed
		// purchase as denied would send the worker shopping for a second copy of a thing
		// it already bought.
		out := deliveredOutcome(intent, reqID, d, paid)
		if gap := errors.Join(execErr, bookkeeping); gap != nil {
			out.Reason += fmt.Sprintf("; audit gap, reconcile from /v1/status: %v", gap)
		}
		return out, nil
	case execErr == nil && paid.Submitted:
		// A 200 whose state is not committed (SIGNED / RECONCILING): the payment left the
		// process and its outcome is UNKNOWN. No receipt is fabricated and no failure is
		// claimed; the hold stands and the reconciler owns it.
		return worker.PurchaseOutcome{
			Decision: decisionLabel(d), Status: "approved-pending-integration", Code: "payment-unresolved",
			Reason: fmt.Sprintf("the payment %s left the daemon and the sidecar reports %s: the outcome is not yet known, "+
				"so nothing is recorded as bought and the budget stays held", paid.PaymentID, paid.State),
			RequestID: reqID,
		}, nil
	case execErr == nil:
		return worker.PurchaseOutcome{
			Decision: decisionLabel(d), Status: "approved-pending-integration",
			Reason:    "approved by policy/owner; money movement pending the F2 sidecar client",
			RequestID: reqID,
		}, nil
	case errors.Is(execErr, approval.ErrExpired):
		return expiredOutcome(), nil
	default:
		// A freeze engaging between decision and execution lands here (frozen: ... from the
		// gate), as does an expired-or-replayed approval and every payment refusal:
		// reported as denied, never as executed. The code separates "the gates refused" from
		// "the payment lane refused", because only the second one means the seller, the
		// sidecar or the wiring is the thing to look at.
		code := "execution-refused"
		var he *h3.Error
		if errors.As(execErr, &he) {
			code = "payment-failed"
		}
		return worker.PurchaseOutcome{
			Decision: string(policy.Deny), Status: "denied", Code: code,
			Reason: execErr.Error(), RequestID: reqID,
		}, nil
	}
}

// pendingSettlement records the honest stop: an approval consumed exactly once, no money
// moved, and the log says so in the same four payload keys it has always used.
//
// It also RELEASES the hold, with pre-sign true. That is not a fabricated release: an
// unwired lane returns Submitted=false with a nil error, which is positive proof that no
// request left the process and therefore that nothing was signed. Keeping the hold instead
// would pin a job's budget forever on a payment that can never happen, the hold carries
// an execution claim, so the expiry sweeper is forbidden to touch it (correctly: it cannot
// tell a claimed hold from a signed one), and the stall escalation would page the owner
// about money that never moved.
func (p *Purchaser) pendingSettlement(ctx context.Context, g approval.Grant, intent approval.Intent) error {
	if _, err := p.store.Append(ctx, store.Event{
		Kind: kindPendingSettlement, EntityID: g.RequestID(), Actor: actorPurchaser,
		Payload: map[string]any{
			"job_id": intent.JobID, "merchant": intent.Merchant,
			"amount_micros": intent.AmountMicros, "note": "approved; money movement pending F2 sidecar client",
		},
	}); err != nil {
		return err
	}
	// Swallowed on purpose: the purchase itself succeeded (as far as an unwired lane can),
	// and an unreleased hold is conservative rather than lost.
	_ = p.release(ctx, g.RequestID(), "no payment lane is wired: the approval was consumed but nothing was sent", codeNoPayerWired, true)
	return nil
}

// settle turns a submitted payment into durable records: the budget commit at the figure
// the SIDECAR reported, the delivered record, and the payee-divergence record when the
// seller echoed an address that is not the approved payee.
//
// Its error is deliberately NOT propagated as the executor's error. Past this point the
// money has moved, and the executor's error path appends payment.failed and consumes the
// intent as failed, a lie about a completed payment, and one that would send the worker
// shopping again. A failed commit here is the W4 crash window by another route: the hold
// stays open with its execution claim, so the boot status reconciler polls the precomputed
// payment id, reads DELIVERED and commits at the same authoritative figure. The caller
// folds this error into the outcome's reason so the audit gap is visible, not silent.
func (p *Purchaser) settle(ctx context.Context, g approval.Grant, intent approval.Intent, out funding.PaymentOutcome) error {
	if !h3.Committed(out.State) {
		// Submitted, outcome unknown. Record exactly that: no commit (nothing is proven
		// spent), no release (money may have moved), no receipt.
		_, err := p.store.Append(ctx, store.Event{
			Kind: kindUnresolved, EntityID: intent.JobID, Actor: actorPurchaser,
			Payload: map[string]any{
				"request_id": g.RequestID(), "job_id": intent.JobID, "intent_id": intent.IntentID,
				"payment_id": out.PaymentID, "state": out.State, "held_micros": intent.MaxAmountMicros,
				"note": "the payment left the daemon and its outcome is not yet known; the budget hold stands",
			},
		})
		return err
	}

	var problems []error
	if p.led != nil {
		// The sidecar's amountPaid is the authority, never this daemon's expectation.
		if err := p.led.Commit(ctx, g.RequestID(), out.PaidMicros, out.PaymentID, out.ReceiptHash, out.State); err != nil {
			problems = append(problems, fmt.Errorf("committing the budget hold for %s: %w", g.RequestID(), err))
		}
	}
	if out.PayeeDiverged {
		// The money already moved, so this is a RECORD, not a refusal (V6 §3.4): the
		// sidecar's pre-sign check compared accept.payTo case-insensitively and passed,
		// while receipt.payee is copied from the seller's own response. Divergence is worth
		// an owner's attention and worth nobody's fabricated failure.
		if _, err := p.store.Append(ctx, store.Event{
			Kind: kindPayeeDivergence, EntityID: intent.JobID, Actor: actorPurchaser,
			Payload: map[string]any{
				"request_id": g.RequestID(), "job_id": intent.JobID, "payment_id": out.PaymentID,
				"approved_pay_to": intent.PayTo, "receipt_payee": out.Payee,
				"amount_micros": out.PaidMicros, "receipt_hash": out.ReceiptHash,
			},
		}); err != nil {
			problems = append(problems, fmt.Errorf("recording the payee divergence for %s: %w", g.RequestID(), err))
		}
	}
	if _, err := p.store.Append(ctx, store.Event{
		Kind: kindDelivered, EntityID: intent.JobID, Actor: actorPurchaser,
		Payload: map[string]any{
			"request_id": g.RequestID(), "job_id": intent.JobID, "intent_id": intent.IntentID,
			"merchant": intent.Merchant, "pay_to": intent.PayTo, "resource": intent.ResourceURL,
			"amount_micros": out.PaidMicros, "payment_id": out.PaymentID,
			"receipt_hash": out.ReceiptHash, "auth_nonce": out.AuthNonce,
			"state": out.State, "payee": out.Payee,
		},
	}); err != nil {
		problems = append(problems, fmt.Errorf("recording the delivered purchase %s: %w", g.RequestID(), err))
	}
	return errors.Join(problems...)
}

// release returns an open hold to the budget. With no ledger wired it is a no-op, which is
// the pre-V6 behaviour; ErrNoSuchHold from a wiring with no Reserve hook is likewise not a
// failure worth surfacing, because there was nothing to give back.
func (p *Purchaser) release(ctx context.Context, requestID, reason, code string, preSign bool) error {
	if p.led == nil {
		return nil
	}
	err := p.led.Release(ctx, requestID, reason, code, preSign)
	if errors.Is(err, budget.ErrNoSuchHold) {
		return nil
	}
	return err
}

// deliveredOutcome is the report for a payment the sidecar completed.
//
// Data stays nil. The funding lane deliberately does not surface the purchased payload
// (PaymentOutcome carries no body), and inventing one here would be exactly the fabricated
// buy this pipeline refuses; the provenance in the receipt is what downstream joins on.
func deliveredOutcome(intent approval.Intent, reqID string, d policy.Decision, out funding.PaymentOutcome) worker.PurchaseOutcome {
	return worker.PurchaseOutcome{
		Decision: decisionLabel(d), Status: "delivered",
		Reason: fmt.Sprintf("paid %s USDC to %s for %s; the sidecar reports %s",
			policy.FormatUSDC(out.PaidMicros), intent.Merchant, nonEmpty(intent.Resource, intent.ResourceURL), out.State),
		RequestID: reqID,
		Receipt: &worker.PurchaseReceipt{
			Merchant: intent.Merchant,
			// Atomic USDC and micros are the same number (USDC is 6dp), so no scaling and
			// no float on this path.
			AmountAtomic: strconv.FormatInt(out.PaidMicros, 10),
			// H3 emits no receiptHash; this is the AuthNonce, the bytes32 actually signed
			// into the EIP-3009 authorization and the join key Billing will need the day
			// JobVault.recordExpense is wired (V6 §3.4).
			ReceiptHash: out.ReceiptHash,
			PaymentID:   out.PaymentID,
		},
	}
}

// refusedOutcome reports a pre-intake refusal in the vocabulary the worker already adapts
// to. Status "denied" with a machine-readable code is the shape an execution refusal
// already uses: worker.PurchaseOutcome has no "unpriceable" status, and inventing one
// would break every reader of the frozen vocabulary (worker.go:61).
func refusedOutcome(code, reason string) worker.PurchaseOutcome {
	return worker.PurchaseOutcome{Decision: string(policy.Deny), Status: "denied", Code: code, Reason: reason}
}

func denyOutcome(d policy.Decision) worker.PurchaseOutcome {
	out := worker.PurchaseOutcome{Decision: string(policy.Deny), Status: "denied"}
	if d.Reason != nil {
		out.Reason, out.Code = d.Reason.Message, d.Reason.Code
	}
	return out
}

func expiredOutcome() worker.PurchaseOutcome {
	return worker.PurchaseOutcome{
		Decision: string(policy.HumanApprovalRequired), Status: "expired", Code: "approval-expired",
		Reason: "the approval window elapsed before the owner decided",
	}
}

func decisionLabel(d policy.Decision) string {
	if d.Outcome == "" {
		return string(policy.AutoApprove)
	}
	return string(d.Outcome)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func freshNonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(b[:]), nil
}
