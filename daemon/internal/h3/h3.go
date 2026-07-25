// Package h3 is the daemon's client for the H3 payment sidecar
// (docs/handshakes/H3-sidecar-api.md; the running service is sidecar/src/service.ts).
//
// PURITY IS THE POINT. This package imports nothing from internal/: not approval, not
// funding, not policy, not store. It speaks HTTP and computes the three H3 derivations,
// and that is all. The reason is capability placement: the bearer token is a spend
// capability (and, per the idempotent-replay ordering at sidecar/src/service.ts:281-301,
// a READ capability over every already-paid resource), and H2_APPROVAL_SECRET mints the
// authorization the sidecar trusts. Both live in internal/funding, the one package that
// already holds signer references. Because this package holds NEITHER (every secret
// arrives as an argument) funding can hold it without leaking a signer anywhere else.
//
// Everything on the wire is a pre-formatted string. That is structural, not stylistic:
// the sidecar recomputes intentHash over the exact bytes we send, and the daemon's
// approval.CanonicalWire formats expiresAt with time.RFC3339 (second precision) while
// json.Marshal of a time.Time emits RFC3339Nano. A time.Time in WireIntent would be a
// 409 INTENT_HASH_MISMATCH on every single pay, so the type makes it unrepresentable.
//
// WHERE THIS FOLLOWS THE BUILT SIDECAR RATHER THAN THE SPEC. The client talks to a
// running process, so the process wins:
//
//  1. 409 PAYMENT_IN_PROGRESS may carry paymentId:null (service.ts:302-306, the
//     in-flight-lock branch) even though spec §2.0 says it MUST carry one. So Error
//     deliberately has NO PaymentID field at all: callers use their own precomputed
//     PaymentID(intentHash, nonce) and can always reach Status.
//  2. `details` is accepted by errorReply (service.ts:86,96) but no call site ever
//     passes it, so no field exists here and nothing branches on it. PRICE_CHANGED
//     therefore cannot teach a caller the new price; the caller re-quotes.
//  3. RESOURCE_NOT_FOUND and UPSTREAM_UNREACHABLE occur on /v1/pay despite being absent
//     from spec §2.2's table: both are raised inside probeChallenge (buyer.ts:158,161,
//     168), which runs strictly before the write-ahead store.upsert at service.ts:372.
//     Provably pre-sign. See PreSign.
//  4. `terminal` is computed from {SETTLED,FAILED,EXPIRED} (service.ts:61,201-203), so a
//     successful DELIVERED payment reports terminal:false. StatusResult has NO Terminal
//     field: branch on State, via Committed/Unresolved.
//  5. SUBMITTED is never persisted (the service writes only SIGNED/DELIVERED/
//     RECONCILING/FAILED), but it stays in the vocabulary for forward-compat, in the
//     unresolved set. RECONCILING is in the unresolved set too, which spec §2.2 omits.
//  6. Route-not-found is HTTP 404 with code BAD_REQUEST (service.ts:478). Classify on
//     Code, NEVER on HTTPStatus: a 404 means a missing payment only when the code is
//     PAYMENT_NOT_FOUND.
//
// Follows the SPEC where it is stricter, because Go satisfies both for free: lowercase
// hex HMAC output (§3.4; hex.EncodeToString is already lowercase even though h3.ts:98
// would accept uppercase), and second-precision expiresAt (validated before sending).
package h3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

// DefaultBaseURL is where the sidecar listens. It binds 127.0.0.1 ONLY
// (service.ts:486), so a non-loopback base URL cannot reach it.
const DefaultBaseURL = "http://127.0.0.1:4020"

// Payment states (sidecar/src/store.ts:21). SETTLED is unreachable in the dry run and
// SUBMITTED is never persisted; both are here so a future facilitator does not require a
// vocabulary change.
const (
	StateSigned      = "SIGNED"
	StateSubmitted   = "SUBMITTED"
	StateDelivered   = "DELIVERED"
	StateReconciling = "RECONCILING"
	StateSettled     = "SETTLED"
	StateFailed      = "FAILED"
	StateExpired     = "EXPIRED"
)

// The only two decisions the treasury signs for (H3 §2.2 step 6, service.ts:339-341).
const (
	DecisionAutoApprove   = "AUTO_APPROVE"
	DecisionHumanApproved = "HUMAN_APPROVED"
)

// The frozen error-code enum (H3 §2.4). Additive-only after the freeze; an existing code
// never changes meaning. Callers branch on these and never on prose or HTTP status.
const (
	CodeUnauthenticated        = "UNAUTHENTICATED"
	CodeBadRequest             = "BAD_REQUEST"
	CodeResourceNotFound       = "RESOURCE_NOT_FOUND"
	CodeNoMatchingNetwork      = "NO_MATCHING_NETWORK"
	CodeChallengeUnavailable   = "CHALLENGE_UNAVAILABLE"
	CodeUpstreamUnreachable    = "UPSTREAM_UNREACHABLE"
	CodePaymentInProgress      = "PAYMENT_IN_PROGRESS"
	CodeApprovalTokenInvalid   = "APPROVAL_TOKEN_INVALID"
	CodeIntentHashMismatch     = "INTENT_HASH_MISMATCH"
	CodeIntentNotApproved      = "INTENT_NOT_APPROVED"
	CodeApprovalExpired        = "APPROVAL_EXPIRED"
	CodeApprovedAmountMismatch = "APPROVED_AMOUNT_MISMATCH"
	CodeMerchantChanged        = "MERCHANT_CHANGED"
	CodeAssetChanged           = "ASSET_CHANGED"
	CodePriceChanged           = "PRICE_CHANGED"
	CodePriceExceedsReserved   = "PRICE_EXCEEDS_RESERVED"
	CodePaymentRejected        = "PAYMENT_REJECTED"
	CodeFacilitatorError       = "FACILITATOR_ERROR"
	CodePaymentNotFound        = "PAYMENT_NOT_FOUND"
	CodeInternal               = "INTERNAL"
)

// ── wire types ──────────────────────────────────────────────────────────────

// WireIntent is H3 §3.1's ApprovedIntent, the `intent` half of the pay body.
//
// EVERY field is a string and all 16 keys must be present, including CreatedAt, which is
// NOT hashed: isWireIntent (service.ts:159-162) type-checks all sixteen and 400s if any
// is missing or non-string.
//
// ExpiresAt is PRE-FORMATTED at SECOND precision, and that is load-bearing rather than
// tidy: the sidecar recomputes intentHash over these bytes and the approvalToken carries
// the daemon's hash, which approval.CanonicalWire built with time.RFC3339. A fractional
// second here diverges the two hashes and every pay returns INTENT_HASH_MISMATCH. Pay
// refuses to send one (see validatePayable) rather than letting the sidecar discover it.
type WireIntent struct {
	IntentID string `json:"intentId"`
	JobID    string `json:"jobId"`
	TaskID   string `json:"taskId"`
	AgentID  string `json:"agentId"`
	// Resource is an ABSOLUTE http(s) URL the sidecar HTTP-probes, taken from the
	// daemon's Intent.ResourceURL. A display string like "GET /v1/company-profile"
	// yields 404 RESOURCE_NOT_FOUND "resource is not a valid URL" (buyer.ts:158).
	Resource string `json:"resource"`
	Network  string `json:"network"`
	Asset    string `json:"asset"`
	// Merchant is the x402 payee ADDRESS (quote.accept.payTo), taken from the daemon's
	// Intent.PayTo, NOT Intent.Merchant, which is the HOST the policy allowlist matches.
	// The sidecar compares this case-insensitively to the live accept.payTo
	// (buyer.ts:199-212) and a host fails MERCHANT_CHANGED. Copy payTo VERBATIM: the
	// live equality check lowercases but intentHash does not, so a re-cased address
	// passes the live check and hashes differently than the sidecar computes.
	Merchant string `json:"merchant"`
	// Amount is canonical atomic USDC: /^(0|[1-9][0-9]*)$/ (service.ts:170). Never a
	// decimal, never 0x, never a float anywhere in this package.
	Amount        string `json:"amount"`
	MaxAmount     string `json:"maxAmount"`
	Purpose       string `json:"purpose"`
	Nonce         string `json:"nonce"`
	Decision      string `json:"decision"`
	PolicyVersion string `json:"policyVersion"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
}

// ApprovalToken is H3 §3.2's approval-decision object. All 8 keys must be present
// strings (isApprovalToken, service.ts:163-166) even though only 4 enter the HMAC.
type ApprovalToken struct {
	IntentHash     string `json:"intentHash"`
	Decision       string `json:"decision"`
	ApprovedAmount string `json:"approvedAmount"`
	Approver       string `json:"approver"`
	PolicyVersion  string `json:"policyVersion"`
	IssuedAt       string `json:"issuedAt"`
	ExpiresAt      string `json:"expiresAt"`
	// Signature is HMAC-SHA256 lowercase hex over
	// intentHash|decision|approvedAmount|expiresAt. Mint it with SignApproval.
	Signature string `json:"signature"`
}

// AcceptExtra carries the EIP-712 domain bits the seller advertises (x402.ts:33-36).
type AcceptExtra struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// AcceptOption is one x402 payment option, verbatim from the seller's 402 challenge
// (x402.ts:17-37). PayTo, Asset, Network and Amount are copied into the intent
// UNMODIFIED: they are hash-bound, so any normalization diverges the hash.
type AcceptOption struct {
	Scheme            string       `json:"scheme"`
	Network           string       `json:"network"`
	Amount            string       `json:"amount"`
	Asset             string       `json:"asset"`
	PayTo             string       `json:"payTo"`
	MaxTimeoutSeconds int64        `json:"maxTimeoutSeconds"`
	Description       string       `json:"description,omitempty"`
	Extra             *AcceptExtra `json:"extra,omitempty"`
}

// Quote is the 200 body of POST /v1/quote (H3 §2.1). QuoteExpiresAt is ADVISORY: pay
// re-probes live and does not enforce it.
type Quote struct {
	Resource       string       `json:"resource"`
	Network        string       `json:"network"`
	Accept         AcceptOption `json:"accept"`
	Price          string       `json:"price"`
	PriceDisplay   string       `json:"priceDisplay"`
	QuotedAt       string       `json:"quotedAt"`
	QuoteExpiresAt string       `json:"quoteExpiresAt"`
}

// Receipt is the H3 receipt (service.ts:174-185). Note what is NOT here: H3 emits no
// receiptHash, so the daemon derives its bytes32 join key from AuthNonce instead.
type Receipt struct {
	Resource string `json:"resource"`
	Amount   string `json:"amount"`
	Asset    string `json:"asset"`
	Network  string `json:"network"`
	Payer    string `json:"payer"`
	// Payee is copied from the SELLER's response, preferring the base64
	// x-payment-response header (service.ts:180, buyer.ts:346-349). The VERIFIED
	// quantity is accept.payTo, compared case-insensitively pre-sign. So never assert
	// Payee == intent.Merchant with byte equality: compare case-insensitively, and on a
	// real mismatch record what was returned and raise a divergence rather than failing
	// the payment, because the money already moved.
	Payee string `json:"payee"`
	// AuthNonce is the EIP-3009 authorization nonce actually signed on chain, equal to
	// AuthNonce(intentHash) by construction.
	AuthNonce string `json:"authNonce"`
	// Settlement is "NOT_BROADCAST" in the dry run (the seller verifies but never
	// broadcasts), so DELIVERED is committed-final. See Committed.
	Settlement string `json:"settlement"`
}

// PayResult is the 200 body of POST /v1/pay (H3 §2.2), for a fresh execution or an
// idempotent replay.
type PayResult struct {
	// PaymentID is the sidecar's echo, equal to PaymentID(intentHash, Nonce) by
	// construction. Callers still hold their OWN precomputed value, because an error
	// envelope may carry paymentId:null and a lost response carries nothing at all.
	PaymentID string `json:"paymentId"`
	// State is DELIVERED here in practice; a mid-flight record replays as a 409, never
	// as a 200 that reads as success with amountPaid:null (service.ts:291-300).
	State string `json:"state"`
	// IdempotentReplay is true when the sidecar returned a stored record without
	// contacting the seller again.
	IdempotentReplay bool `json:"idempotentReplay"`
	// AmountPaid is atomic USDC, "" when the wire carried null. Commit at THIS figure,
	// never at the caller's expectation.
	AmountPaid string `json:"amountPaid"`
	// Receipt is nil before DELIVERED.
	Receipt *Receipt `json:"receipt"`
	// AuthorizationSignature is audit evidence (FR-X402-004), not a key.
	AuthorizationSignature string `json:"authorizationSignature"`
	// Data is the purchased payload, opaque to the daemon. Kept raw so nothing in the
	// money path has to understand a seller's schema; it is "null" before DELIVERED.
	Data       json.RawMessage `json:"data"`
	IntentHash string          `json:"intentHash"`
	ExecutedAt string          `json:"executedAt"`
}

// StatusResult is the 200 body of GET /v1/status/{paymentId} (H3 §2.3).
//
// There is deliberately NO Terminal field. The sidecar computes terminal from
// {SETTLED,FAILED,EXPIRED} (service.ts:61,203), so a successful DELIVERED payment
// reports terminal:false and a caller that trusted it would poll a finished payment
// forever. Branch on State through Committed and Unresolved.
type StatusResult struct {
	PaymentID        string   `json:"paymentId"`
	State            string   `json:"state"`
	IntentHash       string   `json:"intentHash"`
	IdempotencyNonce string   `json:"idempotencyNonce"`
	AmountReserved   string   `json:"amountReserved"`
	AmountPaid       string   `json:"amountPaid"`
	Receipt          *Receipt `json:"receipt"`
	Reason           string   `json:"reason"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

// ── state classification ────────────────────────────────────────────────────

// Committed reports whether a state means the money is spent and the outcome is final.
// DELIVERED counts: the dry-run seller verifies but never broadcasts, so settlement rests
// at NOT_BROADCAST and SETTLED never arrives (H3 §5, §7 step 3). Waiting for SETTLED
// would hold a completed payment open forever.
func Committed(state string) bool {
	return state == StateDelivered || state == StateSettled
}

// Unresolved reports whether a state means the outcome is still UNKNOWN, so a hold
// against it must stand. SIGNED means a key was used exactly once and the result was
// never observed; SUBMITTED is never persisted today but stays in the set for
// forward-compat; RECONCILING is in the set even though spec §2.2 step 3 omits it, and
// note that NOTHING in the sidecar ever advances RECONCILING (service.ts:390 writes it,
// no path leaves it), so a poll-until-terminal loop over it never terminates. That is an
// owner escalation, never an automatic release: a signed authorization is a bearer
// instrument and may still settle (H3 §7 step 4).
//
// FAILED and EXPIRED are neither Committed nor Unresolved: nothing settled, release.
func Unresolved(state string) bool {
	return state == StateSigned || state == StateSubmitted || state == StateReconciling
}

// ── the error envelope ──────────────────────────────────────────────────────

// Error is H3 §2.0's uniform error envelope.
//
// PaymentID is DELIBERATELY absent. The spec says the envelope MUST carry one at or
// after the durable write, but the built in-flight-lock branch returns
// PAYMENT_IN_PROGRESS with paymentId:null (service.ts:302-306). Rather than model a
// field that is sometimes a lie, callers use their own precomputed
// PaymentID(intentHash, nonce), which is always correct and always resolvable.
//
// Details is absent for the same kind of reason: errorReply accepts a details object
// (service.ts:86,96) but no call site passes one, so requiring it would encode a
// promise the process does not keep. PRICE_EXCEEDS_RESERVED's quoted/reserved figures
// live only inside the free-text Message, which §1.4 forbids branching on.
//
// HTTPStatus == 0 marks a refusal this client raised BEFORE any request left the
// process. Nothing was sent, so nothing was signed, and the Code is always chosen so
// PreSign reports that truthfully.
type Error struct {
	HTTPStatus int
	Code       string
	Message    string
	Retriable  bool
}

// Error renders the envelope for logs. Safe to log: the sidecar's messages are
// documented as loggable and no secret ever enters one.
func (e *Error) Error() string {
	where := fmt.Sprintf("HTTP %d", e.HTTPStatus)
	if e.HTTPStatus == 0 {
		where = "refused locally, no request sent"
	}
	return fmt.Sprintf("H3 %s (%s): %s", e.Code, where, e.Message)
}

// postSign is EXACTLY the two codes H3 §2.2 classifies as post-signature. Everything
// else the sidecar can return on /v1/pay is raised before the write-ahead upsert at
// service.ts:372, INCLUDING RESOURCE_NOT_FOUND and UPSTREAM_UNREACHABLE, which are
// absent from the spec's pay table but come out of probeChallenge (buyer.ts:158,161,
// 168) and are therefore provably pre-sign.
var postSign = map[string]bool{
	CodePaymentRejected:  true,
	CodeFacilitatorError: true,
}

// PreSign reports whether it is PROVABLE that no authorization was signed, which is the
// whole budget-release rule in one function (H3 §2.2 "Release safety"). True means the
// caller releases its reservation in full, immediately. False means the hold STANDS
// until a status poll resolves it: the signed authorization is a bearer instrument and
// may still settle, so releasing early and re-minting risks a double-spend of the
// budget (H3 §7 step 4).
//
// PAYMENT_IN_PROGRESS is special-cased to false. Nothing about it says a signature
// happened, but a payment may be in flight and the reservation still backs it; the
// caller polls Status on its own precomputed paymentId, where a PAYMENT_NOT_FOUND means
// "no durable record, safe to release" (that is exactly the in-flight-lock case).
func (e *Error) PreSign() bool {
	// INTERNAL never reaches here from this package: decodeEnvelope strips it to a plain error
	// because it straddles the sidecar's write-ahead SIGNED upsert. Refuse it here too, so a
	// hand-constructed one can never read as "nothing was signed".
	if e.Code == CodeInternal {
		return false
	}
	if e.Code == CodePaymentInProgress {
		return false
	}
	return !postSign[e.Code]
}

// ── the three derivations (H3 §3.4, §4.1, §4.4) ──────────────────────────────

// keccak256Hex is Ethereum-style legacy Keccak over utf8 bytes, which is what viem's
// keccak256(toHex(s)) computes, NOT standard SHA3-256. Duplicated from
// internal/approval on purpose: this package imports nothing from internal/, and the
// cross-language golden vectors in client_test.go are what keep the two copies honest.
func keccak256Hex(data []byte) string {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// PaymentID computes H3 §4.1's deterministic payment id:
//
//	"pay_" + keccak256(utf8(intentHash + "|" + nonce))[2:18]
//
// Fully determined by the intent, so a caller precomputes it at reserve time, persists
// it, and can always reach Status: even when a pay response is lost to a timeout, or the
// 409 envelope carries paymentId:null (service.ts:306). Inputs are the 0x-prefixed
// lowercase hex strings joined by a literal "|".
func PaymentID(intentHash, nonce string) string {
	return "pay_" + keccak256Hex([]byte(intentHash+"|"+nonce))[2:18]
}

// AuthNonce computes H3 §4.4's deterministic EIP-3009 authorization nonce:
//
//	keccak256(utf8(intentHash + "|auth"))
//
// Same intent, same on-chain nonce, so the seller's replay guard catches a duplicate
// even across a sidecar restart. It is also the only deterministic bytes32 a payment
// produces, which is why the daemon uses it as the recordExpense receiptHash join key:
// H3's receipt has no receiptHash of its own (service.ts:174-185).
func AuthNonce(intentHash string) string {
	return keccak256Hex([]byte(intentHash + "|auth"))
}

// SignApproval mints t.Signature: HMAC-SHA256 over
// intentHash|decision|approvedAmount|expiresAt, lowercase hex (H3 §3.4, h3.ts:87-93).
// The secret is the raw utf8 bytes of H2_APPROVAL_SECRET used as the HMAC key.
//
// This package never HOLDS the secret, only borrows it for the length of the call. The
// key lives in internal/funding, whose only payment door demands an approval-minted
// Grant and derives every signed field from it, so no caller can mint an authorization
// for terms the owner never approved. Putting the arithmetic here rather than there is
// what lets it be tested against the sidecar's own vector without a signer in scope.
func SignApproval(secret []byte, t *ApprovalToken) {
	msg := t.IntentHash + "|" + t.Decision + "|" + t.ApprovedAmount + "|" + t.ExpiresAt
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	t.Signature = hex.EncodeToString(mac.Sum(nil))
}

// ── the one atomic/micros conversion ────────────────────────────────────────

// ParseAtomicMicros converts an atomic-USDC wire amount to the daemon's int64 micros.
//
// The two units are the SAME number: USDC is 6dp, so one atomic unit is one micro
// ("40000" is 0.040000 USDC is 40_000 micros). No scaling, and no float anywhere on the
// path. It fails closed on a non-canonical string or a value wider than int64 rather than
// silently truncating an amount, because the result is what gets committed against a
// budget.
func ParseAtomicMicros(atomic string) (int64, error) {
	if !isCanonicalAtomic(atomic) {
		return 0, fmt.Errorf("atomic USDC amount %q is not a base-10 non-negative integer string (service.ts:170)", atomic)
	}
	micros, err := strconv.ParseInt(atomic, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("atomic USDC amount %q does not fit in int64 micros: %w", atomic, err)
	}
	return micros, nil
}

// FormatAtomicMicros renders int64 micros as the canonical atomic wire string, refusing a
// negative figure. A "-1" would type-check as a string at the sidecar and then 400 on the
// canonical-amount regexp, so the refusal belongs on this side where the caller can see
// which amount was wrong.
func FormatAtomicMicros(micros int64) (string, error) {
	if micros < 0 {
		return "", fmt.Errorf("atomic USDC amounts are non-negative, got %d micros", micros)
	}
	return strconv.FormatInt(micros, 10), nil
}

// ── fail-closed pre-flight validation ───────────────────────────────────────

// isCanonicalAtomic mirrors service.ts:170's /^(0|[1-9][0-9]*)$/ : a base-10
// non-negative integer string with no sign, decimal point, leading zero or 0x. Money is
// int64 micros or an atomic string in this codebase and never a float, so a value that
// fails here is a bug upstream, not a formatting preference.
func isCanonicalAtomic(v string) bool {
	if v == "" {
		return false
	}
	if v == "0" {
		return true
	}
	if v[0] == '0' {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

// isHex32 reports whether s is a 0x-prefixed 32-byte lowercase hex string.
func isHex32(s string) bool {
	if len(s) != 66 || !strings.HasPrefix(s, "0x") || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

// refuse builds the pre-flight refusal shape: HTTPStatus 0 (nothing left the process)
// with a code outside postSign, so PreSign correctly reports that nothing was signed and
// the caller releases its hold in full.
func refuse(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// validatePayable refuses a pay body the sidecar would reject anyway, plus the one thing
// it would ACCEPT and then fail on: a fractional-second expiresAt, which parses fine in
// JS but hashes differently than approval.CanonicalWire's second-precision rendering and
// so returns INTENT_HASH_MISMATCH on every attempt. Catching that here turns the single
// most likely first-run failure into a message that names its own cause.
//
// It returns *Error rather than a bare error because the classification matters more
// than the type: a local refusal is provably pre-sign, and the caller's release rule
// reads PreSign.
func validatePayable(in WireIntent, tok ApprovalToken) *Error {
	// The security-relevant fields. An empty Nonce would burn the idempotency key an
	// entire payment hangs off; an empty Merchant or Asset passes the sidecar's
	// type-check and then fails the live equality check as MERCHANT_CHANGED /
	// ASSET_CHANGED, which reads like a hostile seller rather than a wiring bug.
	for _, f := range []struct{ name, value string }{
		{"intent.nonce", in.Nonce},
		{"intent.resource", in.Resource},
		{"intent.merchant", in.Merchant},
		{"intent.asset", in.Asset},
		{"intent.network", in.Network},
		{"intent.expiresAt", in.ExpiresAt},
		{"approvalToken.intentHash", tok.IntentHash},
		{"approvalToken.signature", tok.Signature},
	} {
		if strings.TrimSpace(f.value) == "" {
			return refuse(CodeBadRequest, "%s is empty: refusing to send an underspecified payment to the sidecar", f.name)
		}
	}

	for _, f := range []struct{ name, value string }{
		{"intent.amount", in.Amount},
		{"intent.maxAmount", in.MaxAmount},
		{"approvalToken.approvedAmount", tok.ApprovedAmount},
	} {
		if !isCanonicalAtomic(f.value) {
			return refuse(CodeBadRequest, "%s is not a canonical atomic USDC string (service.ts:170): %q", f.name, f.value)
		}
	}

	// Second precision, enforced. approval.CanonicalWire formats with time.RFC3339
	// (hash.go:201), so a fraction here means the daemon's intentHash and the sidecar's
	// recomputation cannot agree.
	if strings.Contains(in.ExpiresAt, ".") {
		return refuse(CodeBadRequest,
			"intent.expiresAt %q carries sub-second precision; H3 §3.3 hashes the RFC3339 second-precision "+
				"rendering (approval hash.go:201), so this would fail INTENT_HASH_MISMATCH on every pay", in.ExpiresAt)
	}
	if _, err := time.Parse(time.RFC3339, in.ExpiresAt); err != nil {
		return refuse(CodeBadRequest, "intent.expiresAt %q is not RFC3339: %v", in.ExpiresAt, err)
	}

	// The three equality checks the sidecar makes between token and intent
	// (service.ts:327-332). Making them here means a wiring mistake is named at the
	// door instead of arriving as APPROVAL_TOKEN_INVALID, which reads like a bad secret.
	if tok.Decision != in.Decision {
		return refuse(CodeApprovalTokenInvalid,
			"approvalToken.decision %q != intent.decision %q: the token authorizes a different deal", tok.Decision, in.Decision)
	}
	if tok.ExpiresAt != in.ExpiresAt {
		return refuse(CodeApprovalTokenInvalid,
			"approvalToken.expiresAt %q != intent.expiresAt %q: the token authorizes a different window", tok.ExpiresAt, in.ExpiresAt)
	}
	if tok.ApprovedAmount != in.Amount {
		return refuse(CodeApprovedAmountMismatch,
			"approvalToken.approvedAmount %q != intent.amount %q", tok.ApprovedAmount, in.Amount)
	}

	// Gate 1, mirrored locally. H3 §7's chosen default is that Funding short-circuits
	// before pay on a non-approved decision and the sidecar's INTENT_NOT_APPROVED gate
	// is belt-and-suspenders. This is that short-circuit: the payment door never carries
	// an unapproved decision, so a mis-wired caller cannot even reach the treasury.
	if in.Decision != DecisionAutoApprove && in.Decision != DecisionHumanApproved {
		return refuse(CodeIntentNotApproved,
			"intent.decision is %q: the treasury signs only AUTO_APPROVE or HUMAN_APPROVED (H3 §2.2 step 6)", in.Decision)
	}

	// Shape checks on the hash and the HMAC. A malformed intentHash cannot match the
	// sidecar's recomputation, and a malformed signature cannot verify; both would spend
	// a probe and a round trip to learn nothing.
	if !isHex32(tok.IntentHash) {
		return refuse(CodeBadRequest,
			"approvalToken.intentHash %q is not a 0x-prefixed 32-byte lowercase hex hash", tok.IntentHash)
	}
	if len(tok.Signature) != 64 || tok.Signature != strings.ToLower(tok.Signature) {
		return refuse(CodeBadRequest,
			"approvalToken.signature must be 64 lowercase hex chars (H3 §3.4), got %d chars", len(tok.Signature))
	}
	if _, err := hex.DecodeString(tok.Signature); err != nil {
		return refuse(CodeBadRequest, "approvalToken.signature is not hex: %v", err)
	}
	return nil
}
