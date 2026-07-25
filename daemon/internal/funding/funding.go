// Package funding is the Funding agent boundary (G3, PRD §3, FR-BRN-004).
//
// The only component that can move money, acting only on Brain-relayed, owner-approved
// instructions. Execute is the boundary itself: it records the instruction and returns.
// The lanes that actually move value are wrapped around it, each one nil until wired and
// each one honest about being unwired: SetChain for the two on-chain lanes, and SetPayer
// for the H3 payment lane that V6 added (ExecutePayment).
//
// Capability placement, layered (the Step-6 closure of the Grant question):
//
//  1. Workers cannot reach this package at all, no import path exists (AT-16).
//  2. The SOLE mutating entry point, Execute, demands an approval.Grant, the
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
// stronger type-level property: nameable or not, a Decision has no door, the only
// door demands the unforgeable Grant. boundary tests pin the method set and the
// empty-grant refusal.
//
// WHY THE PAYMENT SECRETS LIVE HERE (V6, AT-15's sidecar half). The H3 lane arrives with
// two capabilities. SIDECAR_AUTH_TOKEN spends, and because the sidecar's durable
// idempotency lookup precedes its approval check, it also RE-READS every already-paid
// resource. H2_APPROVAL_SECRET mints the authorization the sidecar trusts. Both are
// handed to SetPayer and held in this package only: internal/h3 borrows the secret for
// the length of one HMAC and stores neither, and no other package can even name the two
// variables (at15_test.go scans the tree for exactly that). The wiring layer reads them
// from the environment once and passes them here, the same posture as the chain signers.
//
// Holding the minter is not the power to authorize. ExecutePayment derives every signed
// field FROM the Grant, so the token it mints can only describe the deal the G7 lifecycle
// already gated: there is no parameter for a payee, an amount or a resource, and both the
// wire payee and the wire resource are hash-bound, so substituting either invalidates the
// owner's approval instead of redirecting the money (AT-05).
package funding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
)

// Instruction is the RECORD of an executed movement, derived inside Execute from the
// grant, never accepted from a caller. Billing and tests read these.
type Instruction struct {
	JobID        string
	Kind         string
	AmountMicros int64
	Merchant     string
	RequestID    string
	ApprovedAt   time.Time
}

// Agent is the funding boundary: it records every instruction for inspection and owns the
// lanes (chain and H3) that turn a recorded instruction into a moved value.
type Agent struct {
	mu       sync.Mutex
	executed []Instruction
	// seen dedups by RequestID: a callback retaining a valid Grant must not be able to
	// replay it into multiple movements (review-batch fix, belt-and-suspenders atop the
	// lifecycle's exactly-once; a real signer would make this durable).
	seen map[string]bool
	// The chain lanes (nil until wired): Funding holds the ONLY signer references in
	// the daemon, workers, Billing, and discovery cannot even name them.
	advanceLane Submitter
	settleLane  Submitter
	// The H3 payment lane (nil until wired), and the HMAC key that mints the
	// approvalToken the sidecar verifies. Same rule as the chain lanes: the only
	// reference in the daemon lives here. nil payer = the honest pending-settlement stop.
	payer          Payer
	approvalSecret []byte
	// chainID is the numeric chain id quotes are priced on; the sidecar derives
	// eip155:${chainId} from it, so a wrong value prices against the wrong network.
	chainID int64
}

// New returns a funding agent. Hand the pointer to Brain and to nothing else.
func New() *Agent { return &Agent{seen: make(map[string]bool)} }

// ChainOutcome is one submitted transaction's result, as Funding reports it upward.
// Reverted is a THIRD state, mined and failed, never conflated with success or with
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
// submissions; Funding holds the ONLY references, no other package can sign.
type Submitter interface {
	Submit(ctx context.Context, calldata []byte) (ChainOutcome, error)
}

// SetChain wires the two submission lanes: the TREASURY lane signs requestAdvance
// (the operator org draws the advance) and the CUSTOMER lane signs acceptDelivery
// (SC-JV-005: only the customer settles, for the demo the customer wallet is
// DAEMON-CUSTODIAL, stated openly; production replaces this lane with the customer's
// own wallet). Either nil = that action stops honestly at *.pending_chain.
func (a *Agent) SetChain(treasuryAdvance, customerSettle Submitter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advanceLane, a.settleLane = treasuryAdvance, customerSettle
}

// ExecuteAdvance is the ADVANCE door: it takes the approval-minted GRANT, never raw
// calldata, so no caller can aim this lane at an arbitrary contract call, performs
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
// job's customer and status is Delivered, a misdirected call reverts, it cannot
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
		return fmt.Errorf("funding: refused, grant is empty (forged outside the approval lifecycle); FR-BRN-004")
	}
	in := g.Intent()
	a.mu.Lock()
	defer a.mu.Unlock()
	// One movement per approval, even if a caller replays the same Grant.
	if a.seen[g.RequestID()] {
		return fmt.Errorf("funding: refused, grant for request %s already executed (replay)", g.RequestID())
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

// ── the H3 payment lane (V6) ─────────────────────────────────────────────────

// Payer is the H3 sidecar lane (internal/h3.Client behind an interface so funding stays
// fake-testable, exactly like Submitter). Funding holds the ONLY reference in the daemon.
//
// Status is part of the lane even though ExecutePayment never calls it: the boot
// reconciler resolves a crashed payment through this same client, and a fake that cannot
// answer Status cannot stand in for the real one in a test of that path.
type Payer interface {
	Quote(ctx context.Context, resource string, chainID int64) (h3.Quote, error)
	Pay(ctx context.Context, in h3.WireIntent, tok h3.ApprovalToken) (h3.PayResult, error)
	Status(ctx context.Context, paymentID string) (h3.StatusResult, error)
}

// PaymentOutcome is one H3 payment attempt's result, as Funding reports it upward.
//
// READ THE ERROR FIRST. ExecutePayment returns a non-nil error for every outcome that is
// not a completed call, and the outcome then carries the CLASSIFICATION so a caller can
// log why without re-deriving it. The release rule is PreSign, and its authority is the
// returned error (errors.As to *h3.Error), never the state: a transport failure is
// deliberately not an *h3.Error, because the sidecar may have received the request and
// signed, so that hold must stand.
type PaymentOutcome struct {
	// Submitted is true when the pay request LEFT this process. False with a NIL error is
	// the honest stop: no payer is wired, nothing moved, and the caller records
	// purchase.pending_settlement exactly as it did before V6. False with a non-nil error
	// is a local refusal: nothing was sent, so nothing was signed.
	Submitted bool
	// PaymentID is H3 §4.1's deterministic id, PRECOMPUTED from the grant's own wire hash
	// and nonce, never read out of a response or an error envelope: the sidecar's
	// in-flight-lock branch answers PAYMENT_IN_PROGRESS with paymentId:null, so only the
	// precomputed value is always available and always resolvable at /v1/status.
	PaymentID string
	// State is the sidecar's payment state (h3.StateDelivered | StateFailed |
	// StateReconciling | StateSigned | StateSubmitted), "" when no 200 arrived. Classify
	// it with h3.Committed / h3.Unresolved and never with a terminal flag: the sidecar
	// computes terminal from {SETTLED,FAILED,EXPIRED}, so a DELIVERED success reports
	// false and a poll-until-terminal caller would wait forever.
	State string
	// PaidMicros is the sidecar's amountPaid as int64 micros (atomic USDC and micros are
	// the SAME number: USDC is 6dp). Meaningful ONLY when h3.Committed(State), and the
	// figure the budget must be committed at: the sidecar's durable record is the
	// authority, never this daemon's expectation.
	PaidMicros int64
	// ReceiptHash is the bytes32 0x-hex join key worker.PurchaseReceipt and Billing want.
	// H3 emits no receiptHash of its own, so this is the AuthNonce: deterministic per
	// intent, computable before the call, and the value actually signed into the EIP-3009
	// authorization. The day JobVault.recordExpense(bytes32) is wired, the join key
	// already exists and must be this same value.
	ReceiptHash string
	// AuthNonce is H3 §4.4's EIP-3009 authorization nonce, keccak256(intentHash|"auth").
	AuthNonce string
	// Payee is receipt.payee AS RETURNED, verbatim. The sidecar copies it from the
	// SELLER's response (preferring the base64 x-payment-response header) while the
	// VERIFIED quantity is accept.payTo, compared case-insensitively before signing. So it
	// is recorded, never asserted byte-equal to the approved payee.
	Payee string
	// PayeeDiverged is true when Payee differs from the grant's approved PayTo under a
	// CASE-INSENSITIVE comparison. The payment is not failed for it, because the money
	// already moved; the caller records the divergence, since this package holds no store.
	PayeeDiverged bool
	// Code is the H3 error code on failure, "" on success. It comes from the envelope's
	// `code` and never from the HTTP status: route-not-found is a 404 carrying BAD_REQUEST,
	// so only the code says what actually happened.
	Code string
	// PreSign is true when it is PROVABLE that no authorization was signed: the caller
	// releases its reservation in full, immediately. False means the hold STANDS until a
	// status poll resolves it, because a signed authorization is a bearer instrument that
	// may still settle.
	PreSign bool
	// Retriable is the sidecar's own advisory flag, passed through unjudged.
	Retriable bool
}

// SetPayer wires the H3 payment lane and the approvalToken key.
//
// approvalSecret is H2_APPROVAL_SECRET, the HMAC key the sidecar verifies; the bearer
// token is already inside p, which is why this signature never carries it. chainID is the
// numeric chain id quotes are priced on.
//
// A nil payer is the honest stop, not a broken daemon: purchases still run every approval
// gate and still record purchase.pending_settlement, exactly as they did before V6, and no
// money moves. Wiring-only, like SetChain: it installs a lane and moves nothing.
func (a *Agent) SetPayer(p Payer, approvalSecret []byte, chainID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Copy the key: a wiring layer that reuses or zeroes its buffer must not be able to
	// silently invalidate every authorization this agent mints afterwards.
	a.approvalSecret = append([]byte(nil), approvalSecret...)
	a.payer, a.chainID = p, chainID
}

// Quote is a READ door, classified: it asks the sidecar to probe a seller for its x402
// 402 challenge. It moves no money, mints no authorization and touches no key, so it
// demands no Grant, the same class as Executed(). It lives here rather than in the
// caller so SIDECAR_AUTH_TOKEN stays confined to the one package that already holds spend
// capabilities; handing that token to purchasing is the AT-15 hole this placement closes.
//
// Call it BEFORE approval intake (H3 §7 step 1): the quoted price becomes the evaluated
// amount, and accept.payTo / accept.asset / accept.network are hash-bound into the intent,
// which is what makes them unsubstitutable afterwards.
//
// With no payer wired it returns a ZERO quote and a NIL error, the same "no lane" shape
// SettleOnChain uses. The discriminator is Accept.PayTo == "": internal/h3 refuses a 200
// whose challenge has an empty payee, so an empty PayTo can only mean no lane. Never copy
// an empty payTo or asset into an intent; it hashes into the approval and then fails
// MERCHANT_CHANGED at pay time, which reads like a hostile seller instead of a stub.
func (a *Agent) Quote(ctx context.Context, resource string) (h3.Quote, error) {
	a.mu.Lock()
	lane, chainID := a.payer, a.chainID
	a.mu.Unlock()
	if lane == nil {
		return h3.Quote{}, nil
	}
	if chainID <= 0 {
		return h3.Quote{}, refuse(h3.CodeBadRequest,
			"the H3 lane is wired with chain id %d: the sidecar prices against eip155:${chainId}, so this would quote on the wrong network", chainID)
	}
	return lane.Quote(ctx, resource, chainID)
}

// ExecutePayment is the PAYMENT door. Like Execute and ExecuteAdvance it takes ONLY the
// approval-minted Grant: the wire intent, the approvalToken and the precomputed paymentId
// are all DERIVED from the grant, so no caller can aim it at a different payee, amount or
// resource, and Execute's empty-grant and replay refusals run first, before any wire
// value is built and before anything leaves the process.
//
// The ordering is the crash-window contract (V6 design §4). The reservation and the
// lifecycle's write-ahead payment.executing claim are both durable before this is called,
// so a crash anywhere inside it recovers through the persisted paymentId: the boot
// reconciler polls /v1/status and PAYMENT_NOT_FOUND means nothing was ever signed.
func (a *Agent) ExecutePayment(ctx context.Context, g approval.Grant) (PaymentOutcome, error) {
	// The Grant gates FIRST, exactly as in ExecuteAdvance: a forged (empty) grant and a
	// replayed one are refused here, so an attacker holding the Agent pointer without a
	// Grant cannot cause even one outbound request.
	if err := a.Execute(ctx, g); err != nil {
		return PaymentOutcome{}, err
	}

	a.mu.Lock()
	lane, secret := a.payer, a.approvalSecret
	a.mu.Unlock()

	if lane == nil {
		// THE HONEST STOP, unchanged from Phase 1: no money moved, no error, and the
		// caller records purchase.pending_settlement.
		return PaymentOutcome{Submitted: false}, nil
	}
	if len(secret) == 0 {
		// A lane with no HMAC key cannot mint an authorization the sidecar will trust.
		// Refuse locally instead of spending a round trip to learn it as
		// APPROVAL_TOKEN_INVALID, which reads like a rotated secret rather than
		// half-finished wiring.
		return PaymentOutcome{}, refuse(h3.CodeApprovalTokenInvalid,
			"the H3 lane is wired without an approval secret: refusing to send a payment the sidecar cannot verify")
	}

	wire, tok, bad := payableFromGrant(g, secret)
	if bad != nil {
		// A local refusal, so nothing was sent and nothing was signed: bad.PreSign() is
		// true and the caller releases the hold in full.
		return PaymentOutcome{}, bad
	}
	// Both derivations are fixed by the grant, so they are known BEFORE the call and stay
	// correct when the response is lost (crash window W3).
	paymentID := h3.PaymentID(tok.IntentHash, wire.Nonce)
	authNonce := h3.AuthNonce(tok.IntentHash)

	out := PaymentOutcome{
		Submitted: true,
		PaymentID: paymentID,
		AuthNonce: authNonce,
		// H3 has no receiptHash; the AuthNonce IS the bytes32 join key (see the field doc).
		ReceiptHash: authNonce,
	}

	res, err := lane.Pay(ctx, wire, tok)
	if err != nil {
		var he *h3.Error
		if errors.As(err, &he) {
			out.Code, out.PreSign, out.Retriable = he.Code, he.PreSign(), he.Retriable
			return out, err
		}
		// NOT an *h3.Error: a transport failure, a short read, or a body that would not
		// parse. The outcome is UNKNOWN (the request may have arrived and signed), so
		// PreSign stays false and the hold must stand until a status poll resolves it.
		return out, err
	}

	out.State = res.State
	if res.Receipt != nil {
		out.Payee = res.Receipt.Payee
		// Case-insensitive on purpose: the seller echoes an address whose casing is its
		// own, while the pre-sign check that actually protected us compared it to
		// accept.payTo case-insensitively too. A byte comparison here would fail a
		// perfectly good payment on EIP-55 checksum casing.
		out.PayeeDiverged = !strings.EqualFold(strings.TrimSpace(res.Receipt.Payee), strings.TrimSpace(wire.Merchant))
	}
	if h3.Committed(res.State) {
		micros, perr := h3.ParseAtomicMicros(res.AmountPaid)
		if perr != nil {
			// Committed, with an unreadable figure. Do NOT fall back to the intent's
			// amount: committing a guessed number is how a budget quietly stops matching
			// the money. A plain error (not *h3.Error) keeps the hold, and the reconciler
			// reads the authoritative figure from /v1/status on the precomputed id.
			return out, fmt.Errorf("funding: H3 reported %s for payment %s but its amountPaid %q is unreadable (%w); "+
				"refusing to commit a budget at a guessed figure - reconcile from /v1/status",
				res.State, paymentID, res.AmountPaid, perr)
		}
		out.PaidMicros = micros
	}
	return out, nil
}

// The one wire value and the one token value that the Grant cannot supply.
//
// approval.Grant carries the intent, the request id and the gate-passed instant, and
// nothing else: whether the approval was AUTO_APPROVE or HUMAN_APPROVED, and who approved
// it, live on the approval Request (State / DecidedBy), which this package deliberately
// cannot reach. Holding a *approval.Lifecycle here to read them would hand the money
// boundary the Decide door, which is a far worse trade than an under-specific audit line.
//
// AUTO_APPROVE with the policy engine as approver is the conservative filling: it
// fabricates no owner action, which is the one lie this codebase never tells. Claiming
// HUMAN_APPROVED on a policy-approved payment would assert an approval the owner never
// gave. The sidecar treats the two identically (both mean "the treasury signed for it"),
// so nothing about the money changes; only the sidecar's own record is less specific than
// the daemon's event log, which still names the real decider on approval.approve.
//
// The fix is a Decision()/Approver() accessor on approval.Grant, minted inside Execute
// from the Request. That is an approval-package change, outside V6's funding scope.
const (
	wireDecision = h3.DecisionAutoApprove
	wireApprover = "policy-engine" // the auto-approve decider's name (lifecycle.go:310)
)

// payableFromGrant derives the ENTIRE pay body from the grant: the wire intent, the
// approval token, and the HMAC over it. Nothing is accepted alongside the grant, which is
// what makes the Grant the only door: a caller cannot aim this at another payee, another
// resource or another amount, because there is no parameter for one.
//
// It returns *h3.Error rather than a bare error because the classification is what the
// caller acts on: a locally raised refusal is provably pre-sign (HTTPStatus 0 with a code
// outside the post-sign set), so the reservation is released in full.
func payableFromGrant(g approval.Grant, secret []byte) (h3.WireIntent, h3.ApprovalToken, *h3.Error) {
	in := g.Intent()

	// This door carries outbound x402 spend only. An advance is money INTO the treasury
	// and executes on chain through ExecuteAdvance; the same literals Execute uses, so
	// this defense-in-depth check needs no policy import.
	if in.Kind != "" && in.Kind != "payment" {
		return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
			"intent kind %q cannot pay through the H3 lane, which prices and settles outbound x402 spend only "+
				"(an advance is human-authorized and executes on chain, via ExecuteAdvance)", in.Kind)
	}
	if in.AmountMicros <= 0 {
		return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
			"the approved amount is %d micros: refusing to mint an authorization for a non-positive amount", in.AmountMicros)
	}
	// The overpay window, closed structurally at the door that mints the authorization.
	// maxAmount is the sidecar's ceiling (a live price above it is PRICE_EXCEEDS_RESERVED)
	// and it is HASH-BOUND, so funding can neither widen it nor substitute the amount for
	// an unset one without diverging the intentHash the sidecar recomputes. A ceiling
	// ABOVE the amount would also authorize a figure the policy engine never tested: rule
	// 1 is fed AmountMicros only (approval/lifecycle.go:283). Both shapes are a fix at
	// intake (set MaxAmountMicros = AmountMicros there), never a substitution here.
	if in.MaxAmountMicros != in.AmountMicros {
		return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
			"the reserve ceiling is %d micros but the evaluated amount is %d: the ceiling is hash-bound, so it must be "+
				"set equal to the amount at intake; a zero ceiling fails PRICE_EXCEEDS_RESERVED and a wider one would "+
				"authorize an amount the job-budget rule never tested", in.MaxAmountMicros, in.AmountMicros)
	}
	amount, aerr := h3.FormatAtomicMicros(in.AmountMicros)
	if aerr != nil {
		return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
			"the approved amount (%d micros) is not a payable atomic figure: %v", in.AmountMicros, aerr)
	}
	maxAmount, merr := h3.FormatAtomicMicros(in.MaxAmountMicros)
	if merr != nil {
		return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
			"the reserve ceiling (%d micros) is not a payable atomic figure: %v", in.MaxAmountMicros, merr)
	}

	// The quote-derived, hash-bound fields. Empty here means the H3 §7 step 1 quote never
	// ran. internal/h3 would refuse the same body, but naming the daemon-side cause is the
	// difference between "fix the wiring" and "suspect the seller".
	for _, f := range []struct{ name, value string }{
		{"PayTo (the quote's accept.payTo, the ADDRESS the wire carries)", in.PayTo},
		{"ResourceURL (the absolute URL the sidecar probes)", in.ResourceURL},
		{"Asset (the quote's accept.asset)", in.Asset},
		{"Network (the quote's accept.network)", in.Network},
	} {
		if strings.TrimSpace(f.value) == "" {
			return h3.WireIntent{}, h3.ApprovalToken{}, refuse(h3.CodeBadRequest,
				"the approved intent carries no %s: Quote must run BEFORE intake (H3 §7 step 1) so the quote's values are "+
					"hash-bound into the approval the owner gave", f.name)
		}
	}

	// SECOND precision, formatted exactly as approval.CanonicalWire formats it. A
	// fractional second would hash differently than the sidecar's recomputation and every
	// pay would return INTENT_HASH_MISMATCH; h3.WireIntent takes a pre-formatted string so
	// a time.Time cannot slip onto the wire, and this is the one place that formats it.
	expiresAt := in.ExpiresAt.UTC().Format(time.RFC3339)

	// The hash the sidecar recomputes over the body below, and the ONLY value the approval
	// token binds. Computed from the grant's own intent, so the token cannot describe
	// terms the lifecycle did not gate.
	intentHash := approval.WireHash(in)

	wire := h3.WireIntent{
		IntentID: in.IntentID,
		JobID:    in.JobID,
		TaskID:   in.TaskID,
		AgentID:  in.AgentID,
		// The two conventions cross HERE, and only here: the wire carries the payee
		// ADDRESS and the absolute URL, while the policy allowlist matched the HOST
		// (in.Merchant) and the display resource (in.Resource). Both wire values are
		// hash-bound, so substituting either invalidates the owner's approval rather than
		// redirecting the money, which is precisely AT-05's purpose.
		Resource:      in.ResourceURL,
		Merchant:      in.PayTo,
		Network:       in.Network,
		Asset:         in.Asset,
		Amount:        amount,
		MaxAmount:     maxAmount,
		Purpose:       in.Purpose,
		Nonce:         in.Nonce,
		Decision:      wireDecision,
		PolicyVersion: in.PolicyVersion,
		// createdAt is the one wire key H3 excludes from the hash, so reporting the
		// grant's gate-passed instant here cannot move the hash.
		CreatedAt: g.GrantedAt().UTC().Format(time.RFC3339),
		ExpiresAt: expiresAt,
	}

	tok := h3.ApprovalToken{
		IntentHash: intentHash,
		Decision:   wireDecision,
		// The sidecar requires approvedAmount == intent.amount, and so do we: the token
		// authorizes the evaluated figure, not a ceiling.
		ApprovedAmount: amount,
		Approver:       wireApprover,
		PolicyVersion:  in.PolicyVersion,
		IssuedAt:       g.GrantedAt().UTC().Format(time.RFC3339),
		ExpiresAt:      expiresAt,
	}
	// The HMAC is the last step and the only use of the secret. Every field it covers was
	// read from the grant above, which is why holding the key is not the power to
	// authorize: the token can only ever describe the deal the lifecycle already gated.
	h3.SignApproval(secret, &tok)
	return wire, tok, nil
}

// refuse builds a locally raised H3 refusal: HTTPStatus 0 (nothing left the process) with
// a code outside the post-sign set, so the caller's PreSign() truthfully reports that
// nothing was signed and the reservation is released in full. Same shape internal/h3 uses
// for its own pre-flight refusals, so one classification rule covers both layers.
func refuse(code, format string, args ...any) *h3.Error {
	return &h3.Error{Code: code, Message: "funding: " + fmt.Sprintf(format, args...)}
}
