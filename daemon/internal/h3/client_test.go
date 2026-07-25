package h3

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The sidecar's own integration-test constants (sidecar/src/service-test.ts:39-40), so a
// fixture here and a live run there exercise the same bytes.
const (
	testToken  = "test-sidecar-token-0123456789abcdef"
	testSecret = "test-h2-approval-secret-0123456789"
)

// GV-1, the cross-language golden vector. The same four constants are asserted in
// sidecar/src/h3-golden-vector-test.ts (npm run test:h3-vectors) and the hash in
// daemon/internal/approval/hash_test.go, so a divergence in any of the three
// implementations fails a test in whichever language moved.
const (
	gv1IntentHash   = "0x1230b4badedc3411ed0cb4f53d6be2ad5f7fccda30e9569bbd66c785e97e9e04"
	gv1PaymentID    = "pay_dbe42421492fc2e0"
	gv1AuthNonce    = "0x3baaf2b15df9861bb66384e486ad5c61c7b999cd15c94527af6487dc767eb71b"
	gv1ExpiresAt    = "2026-07-22T10:05:00Z"
	gv1ApprovalSig  = "8a11843f21949c053d995ab07a41797239ec3da498e4cc6fb4dd43cc3b0a74d6"
	gv2IntentHash   = "0x6a306e8ebe6e8f3b0ceadf22f9b7d7de490f19a7513e925c617232b6dd0893ed"
	deadPayToMixed  = "0x000000000000000000000000000000000000dEaD"
	profileResource = "http://127.0.0.1:4021/v1/company-profile"
)

var gv1Nonce = "0x" + strings.Repeat("ab", 32)

func fixtureIntent() WireIntent {
	return WireIntent{
		IntentID:      "pi_9f3c1a2b",
		JobID:         "job_104",
		TaskID:        "task_research_01",
		AgentID:       "market-researcher",
		Resource:      profileResource,
		Network:       "eip155:5042002",
		Asset:         "0x2222222222222222222222222222222222222222",
		Merchant:      "0x1111111111111111111111111111111111111111",
		Amount:        "40000",
		MaxAmount:     "40000",
		Purpose:       "Competitor profile for job_104",
		Nonce:         gv1Nonce,
		Decision:      DecisionAutoApprove,
		PolicyVersion: "pol_7",
		CreatedAt:     "2026-07-22T10:00:00Z",
		ExpiresAt:     gv1ExpiresAt,
	}
}

func fixtureToken() ApprovalToken {
	tok := ApprovalToken{
		IntentHash:     gv1IntentHash,
		Decision:       DecisionAutoApprove,
		ApprovedAmount: "40000",
		Approver:       "policy-engine",
		PolicyVersion:  "pol_7",
		IssuedAt:       "2026-07-22T10:00:01Z",
		ExpiresAt:      gv1ExpiresAt,
	}
	SignApproval([]byte(testSecret), &tok)
	return tok
}

// ── captured wire bodies, byte-faithful to the built sidecar ─────────────────

// payDelivered200 is service.ts:payResponse for a fresh DELIVERED execution.
const payDelivered200 = `{
  "paymentId": "pay_dbe42421492fc2e0",
  "state": "DELIVERED",
  "idempotentReplay": false,
  "amountPaid": "40000",
  "receipt": {
    "resource": "http://127.0.0.1:4021/v1/company-profile",
    "amount": "40000",
    "asset": "0x2222222222222222222222222222222222222222",
    "network": "eip155:5042002",
    "payer": "0x3333333333333333333333333333333333333333",
    "payee": "0x1111111111111111111111111111111111111111",
    "authNonce": "0x3baaf2b15df9861bb66384e486ad5c61c7b999cd15c94527af6487dc767eb71b",
    "settlement": "NOT_BROADCAST"
  },
  "authorizationSignature": "0x4c1f0e7d",
  "data": {
    "company": "Acme Robotics",
    "employees": 1200
  },
  "intentHash": "0x1230b4badedc3411ed0cb4f53d6be2ad5f7fccda30e9569bbd66c785e97e9e04",
  "executedAt": "2026-07-22T10:00:02Z"
}`

// quote200 is service.ts:handleQuote's 200. payTo is the seller's mixed-case default
// (seller.ts:55-84) precisely so the verbatim-copy assertion has something to catch.
const quote200 = `{
  "resource": "http://127.0.0.1:4021/v1/company-profile",
  "network": "eip155:5042002",
  "accept": {
    "scheme": "exact",
    "network": "eip155:5042002",
    "amount": "40000",
    "asset": "0x2222222222222222222222222222222222222222",
    "payTo": "0x000000000000000000000000000000000000dEaD",
    "maxTimeoutSeconds": 300,
    "description": "Competitor company profile (0.040000 USDC)",
    "extra": {
      "name": "USD Coin",
      "version": "2"
    }
  },
  "price": "40000",
  "priceDisplay": "0.040000",
  "quotedAt": "2026-07-22T10:00:00Z",
  "quoteExpiresAt": "2026-07-22T10:05:00Z"
}`

// statusDelivered200 is service.ts:statusResponse. Note terminal:false on a SUCCESSFUL
// payment: TERMINAL is {SETTLED,FAILED,EXPIRED} (service.ts:61,203).
const statusDelivered200 = `{
  "paymentId": "pay_dbe42421492fc2e0",
  "state": "DELIVERED",
  "terminal": false,
  "intentHash": "0x1230b4badedc3411ed0cb4f53d6be2ad5f7fccda30e9569bbd66c785e97e9e04",
  "idempotencyNonce": "0xabab",
  "amountReserved": "40000",
  "amountPaid": "40000",
  "receipt": null,
  "reason": null,
  "createdAt": "2026-07-22T10:00:00Z",
  "updatedAt": "2026-07-22T10:00:02Z"
}`

// health200 is the /health body, the one route that answers without a bearer token.
const health200 = `{
  "ok": true,
  "service": "snapfall-h3-sidecar"
}`

// envelope builds service.ts:errorReply's body. paymentIDJSON is the literal JSON for
// that key: `null` is a real, shipped value on the in-flight-lock branch.
//
// Verified against the running sidecar: key order, 2-space indentation, the
// "content-type: application/json; charset=utf-8" and "x-h3-version: 1.0" headers, and
// paymentId:null on UNAUTHENTICATED / PAYMENT_NOT_FOUND / APPROVAL_TOKEN_INVALID and on
// the route-not-found 404 whose code is BAD_REQUEST.
func envelope(code, message, paymentIDJSON string, retriable bool) string {
	r := "false"
	if retriable {
		r = "true"
	}
	return `{
  "error": {
    "code": "` + code + `",
    "message": "` + message + `",
    "paymentId": ` + paymentIDJSON + `,
    "retriable": ` + r + `
  }
}`
}

// ── plumbing ────────────────────────────────────────────────────────────────

// recorded captures what actually left the daemon. calls == 0 is an assertion in its own
// right: a locally refused pay must never touch the sidecar.
type recorded struct {
	calls  int
	method string
	path   string
	auth   string
	ctype  string
	body   []byte
}

func stub(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the captured request body: %v", err)
		}
		rec.calls++
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.auth = r.Header.Get("authorization")
		rec.ctype = r.Header.Get("content-type")
		rec.body = raw
		reply(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, testToken, srv.Client()), rec
}

// writeH3 replies exactly as the sidecar does: 2-space-indented JSON, the version
// header, and the charset on the content type (service.ts:435-443).
func writeH3(w http.ResponseWriter, status int, body string) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("x-h3-version", "1.0")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// asH3 asserts err is an *Error, the way every caller classifies a failure.
func asH3(t *testing.T, err error) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an H3 error, got nil")
	}
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v (%T) is not an *h3.Error, so no caller can classify it", err, err)
	}
	return he
}

// ── the golden vectors ──────────────────────────────────────────────────────

// The two derivations hang off intentHash, so pinning them catches a divergence in the
// derivation even when the hash itself agrees. Both constants come from the shipped
// TypeScript implementation, not from this one.
func TestGoldenVector_PaymentIDAndAuthNonceMatchTheSidecar(t *testing.T) {
	if got := PaymentID(gv1IntentHash, gv1Nonce); got != gv1PaymentID {
		t.Errorf("PaymentID = %s, want %s (H3 §4.1; sidecar h3.ts:76-78)", got, gv1PaymentID)
	}
	if got := AuthNonce(gv1IntentHash); got != gv1AuthNonce {
		t.Errorf("AuthNonce = %s, want %s (H3 §4.4; sidecar h3.ts:82-84)", got, gv1AuthNonce)
	}
	// Different intents must not collide on either derivation, or the seller's replay
	// guard and the status lookup would both be aliased.
	if PaymentID(gv2IntentHash, gv1Nonce) == gv1PaymentID {
		t.Error("two distinct intent hashes produced the same paymentId")
	}
	if AuthNonce(gv2IntentHash) == gv1AuthNonce {
		t.Error("two distinct intent hashes produced the same on-chain authNonce")
	}
	// The paymentId is 16 hex chars after the prefix, never the whole hash.
	if len(gv1PaymentID) != len("pay_")+16 {
		t.Fatalf("the pinned paymentId %q is not pay_ + 16 hex chars", gv1PaymentID)
	}
}

// The HMAC is the authorization the sidecar trusts, so its preimage is pinned against a
// value computed by node:crypto over the shipped preimage, not by this package.
func TestSignApproval_MatchesTheSidecarHMAC(t *testing.T) {
	tok := fixtureToken()
	if tok.Signature != gv1ApprovalSig {
		t.Fatalf("signature = %s, want %s\npreimage is intentHash|decision|approvedAmount|expiresAt, "+
			"lowercase hex, secret as raw utf8 key (H3 §3.4, h3.ts:87-93)", tok.Signature, gv1ApprovalSig)
	}
	if tok.Signature != strings.ToLower(tok.Signature) {
		t.Error("signature must be lowercase hex: the sidecar compares constant-time and byte-sensitively")
	}
	// Every one of the four hashed terms must move the signature. A term that did not
	// would be a field the owner's authorization does not actually cover.
	for _, mutate := range []func(*ApprovalToken){
		func(x *ApprovalToken) { x.IntentHash = gv2IntentHash },
		func(x *ApprovalToken) { x.Decision = DecisionHumanApproved },
		func(x *ApprovalToken) { x.ApprovedAmount = "40001" },
		func(x *ApprovalToken) { x.ExpiresAt = "2026-07-22T10:06:00Z" },
	} {
		other := fixtureToken()
		mutate(&other)
		SignApproval([]byte(testSecret), &other)
		if other.Signature == gv1ApprovalSig {
			t.Error("a hashed approval term did not change the signature")
		}
	}
	// A different secret must not verify against the same bytes.
	wrong := fixtureToken()
	SignApproval([]byte("not-the-h2-secret-0123456789abcd"), &wrong)
	if wrong.Signature == gv1ApprovalSig {
		t.Error("two different secrets produced the same signature")
	}
}

// Atomic USDC and the daemon's int64 micros are the same number, and this is the only
// place the two vocabularies meet. A float would be a rounding bug in a money path.
func TestAtomicMicros_RoundTripsAndFailsClosed(t *testing.T) {
	for _, atomic := range []string{"0", "1", "40000", "6000000", "9223372036854775807"} {
		micros, err := ParseAtomicMicros(atomic)
		if err != nil {
			t.Fatalf("ParseAtomicMicros(%q): %v", atomic, err)
		}
		back, err := FormatAtomicMicros(micros)
		if err != nil {
			t.Fatalf("FormatAtomicMicros(%d): %v", micros, err)
		}
		if back != atomic {
			t.Errorf("round trip of %q produced %q", atomic, back)
		}
	}
	if got, err := ParseAtomicMicros("40000"); err != nil || got != 40_000 {
		t.Errorf("40000 atomic must be 40000 micros, got %d (%v)", got, err)
	}
	// Everything the sidecar's canonical-amount regexp refuses, plus an int64 overflow.
	for _, bad := range []string{"", "0.04", "-1", "0x40000", "040000", "4e4", " 40000", "9223372036854775808"} {
		if _, err := ParseAtomicMicros(bad); err == nil {
			t.Errorf("ParseAtomicMicros(%q) must fail closed rather than guess an amount", bad)
		}
	}
	if _, err := FormatAtomicMicros(-1); err == nil {
		t.Error("a negative amount must be refused here, not discovered as a 400 at the sidecar")
	}
}

// ── pay: the happy path ─────────────────────────────────────────────────────

func TestPay_DeliveredCarriesTheReceiptAndTheGoods(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, payDelivered200)
	})

	out, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
	if err != nil {
		t.Fatalf("pay: %v", err)
	}

	if rec.method != http.MethodPost || rec.path != "/v1/pay" {
		t.Errorf("pay hit %s %s, want POST /v1/pay", rec.method, rec.path)
	}
	// One space, case-sensitive, constant-time compared (h3.ts:102-107).
	if rec.auth != "Bearer "+testToken {
		t.Errorf("authorization = %q, want %q", rec.auth, "Bearer "+testToken)
	}
	if !strings.HasPrefix(rec.ctype, "application/json") {
		t.Errorf("content-type = %q, want application/json", rec.ctype)
	}

	var sent struct {
		Intent map[string]any `json:"intent"`
		Token  map[string]any `json:"approvalToken"`
	}
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("the body we sent does not parse as {intent, approvalToken}: %v", err)
	}
	// All 16 intent keys present AS STRINGS, or isWireIntent 400s (service.ts:159-162).
	intentKeys := []string{
		"intentId", "jobId", "taskId", "agentId", "resource", "network", "asset", "merchant",
		"amount", "maxAmount", "purpose", "nonce", "decision", "policyVersion", "createdAt", "expiresAt",
	}
	if len(sent.Intent) != len(intentKeys) {
		t.Errorf("intent carried %d keys, want exactly %d", len(sent.Intent), len(intentKeys))
	}
	for _, k := range intentKeys {
		v, ok := sent.Intent[k]
		if !ok {
			t.Errorf("intent.%s is missing; isWireIntent requires all 16 keys", k)
			continue
		}
		if _, isString := v.(string); !isString {
			t.Errorf("intent.%s is %T on the wire, must be a string", k, v)
		}
	}
	tokenKeys := []string{"intentHash", "decision", "approvedAmount", "approver", "policyVersion", "issuedAt", "expiresAt", "signature"}
	if len(sent.Token) != len(tokenKeys) {
		t.Errorf("approvalToken carried %d keys, want exactly %d", len(sent.Token), len(tokenKeys))
	}
	for _, k := range tokenKeys {
		if _, ok := sent.Token[k]; !ok {
			t.Errorf("approvalToken.%s is missing; isApprovalToken requires all 8 keys", k)
		}
	}
	// THE first-run failure, pinned on the wire: second precision, no fraction. A
	// time.Time here would marshal as RFC3339Nano and mismatch every intentHash.
	if got := sent.Intent["expiresAt"]; got != gv1ExpiresAt {
		t.Errorf("wire expiresAt = %v, want the second-precision %q", got, gv1ExpiresAt)
	}
	// The amounts are canonical atomic strings, never micros-as-number, never a decimal.
	if got := sent.Intent["amount"]; got != "40000" {
		t.Errorf("wire amount = %v, want the atomic string \"40000\"", got)
	}

	if out.State != StateDelivered {
		t.Fatalf("state = %q, want DELIVERED", out.State)
	}
	if !Committed(out.State) || Unresolved(out.State) {
		t.Error("DELIVERED must classify as committed-final and not unresolved (H3 §7 step 3)")
	}
	if out.IdempotentReplay {
		t.Error("idempotentReplay must be false for a fresh execution")
	}
	if out.AmountPaid != "40000" {
		t.Errorf("amountPaid = %q, want 40000; commit at THIS figure, not at our expectation", out.AmountPaid)
	}
	if out.Receipt == nil {
		t.Fatal("a DELIVERED pay must carry a receipt")
	}
	if out.Receipt.AuthNonce != gv1AuthNonce {
		t.Errorf("receipt.authNonce = %s, want the derived %s (it is the daemon's receiptHash join key)",
			out.Receipt.AuthNonce, gv1AuthNonce)
	}
	if out.Receipt.Settlement != "NOT_BROADCAST" {
		t.Errorf("settlement = %q, want NOT_BROADCAST in the dry run", out.Receipt.Settlement)
	}
	if !strings.Contains(string(out.Data), "Acme Robotics") {
		t.Errorf("data did not survive as raw JSON: %s", out.Data)
	}
	if out.PaymentID != PaymentID(gv1IntentHash, gv1Nonce) {
		t.Errorf("the sidecar echoed %s but the deterministic id is %s; the two must agree by construction",
			out.PaymentID, PaymentID(gv1IntentHash, gv1Nonce))
	}
}

// ── pay: classification, which is the whole release rule ────────────────────

func TestPay_PreSignFailuresReleaseAndPostSignFailuresDoNot(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		code      string
		paymentID string
		retriable bool
		preSign   bool
	}{
		// AT-05's live-equality defence, all raised before the write-ahead upsert.
		{"merchant swapped", http.StatusConflict, CodeMerchantChanged, "null", false, true},
		{"price moved", http.StatusConflict, CodePriceChanged, "null", false, true},
		{"asset swapped", http.StatusConflict, CodeAssetChanged, "null", false, true},
		{"over the ceiling", http.StatusPaymentRequired, CodePriceExceedsReserved, "null", false, true},
		{"hash mismatch", http.StatusConflict, CodeIntentHashMismatch, "null", false, true},
		{"bad approval HMAC", http.StatusUnauthorized, CodeApprovalTokenInvalid, "null", false, true},
		{"window elapsed", http.StatusGone, CodeApprovalExpired, "null", false, true},
		{"not approved", http.StatusForbidden, CodeIntentNotApproved, "null", false, true},
		// Absent from spec §2.2's table but raised inside probeChallenge
		// (buyer.ts:158,161,168), which runs strictly before the write-ahead: pre-sign.
		{"resource not a 402", http.StatusNotFound, CodeResourceNotFound, "null", false, true},
		{"seller unreachable", http.StatusBadGateway, CodeUpstreamUnreachable, "null", true, true},
		{"no usable challenge", http.StatusBadGateway, CodeChallengeUnavailable, "null", true, true},
		// The only two post-sign codes: the authorization is a bearer instrument.
		{"seller rejected the payment", http.StatusPaymentRequired, CodePaymentRejected, `"pay_dbe42421492fc2e0"`, false, false},
		{"transport died after submit", http.StatusBadGateway, CodeFacilitatorError, `"pay_dbe42421492fc2e0"`, true, false},
		// Not a signature claim, but a payment may be in flight and the hold backs it.
		{"in flight, id resolvable", http.StatusConflict, CodePaymentInProgress, `"pay_dbe42421492fc2e0"`, true, false},
		// The SHIPPED in-flight-lock branch: paymentId is NULL despite spec §2.0
		// (service.ts:302-306). Nothing here may read it, so nothing may break on it.
		{"in flight, id null", http.StatusConflict, CodePaymentInProgress, "null", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
				writeH3(w, tc.status, envelope(tc.code, "as returned by the sidecar", tc.paymentID, tc.retriable))
			})
			_, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
			he := asH3(t, err)
			if rec.calls != 1 {
				t.Fatalf("pay made %d calls, want 1", rec.calls)
			}
			if he.Code != tc.code || he.HTTPStatus != tc.status {
				t.Fatalf("classified as %s/HTTP %d, want %s/HTTP %d", he.Code, he.HTTPStatus, tc.code, tc.status)
			}
			if he.Retriable != tc.retriable {
				t.Errorf("retriable = %v, want %v", he.Retriable, tc.retriable)
			}
			if he.PreSign() != tc.preSign {
				t.Fatalf("PreSign() = %v, want %v for %s: a wrong answer here either leaks budget or "+
					"releases a hold behind a signature that may still settle", he.PreSign(), tc.preSign, tc.code)
			}
			if !strings.Contains(he.Error(), tc.code) {
				t.Errorf("Error() = %q, must name the code for the log", he.Error())
			}
		})
	}
}

// The envelope's paymentId is not merely ignored: it is not modelled, so no future
// caller can start depending on a field the shipped sidecar sometimes sends as null.
func TestError_DoesNotModelPaymentIDOrDetails(t *testing.T) {
	et := reflect.TypeOf(Error{})
	for _, absent := range []string{"PaymentID", "PaymentId", "Details"} {
		if _, found := et.FieldByName(absent); found {
			t.Errorf("h3.Error must NOT carry %s: PAYMENT_IN_PROGRESS may send paymentId:null "+
				"(service.ts:302-306) and no call site ever passes details (service.ts:86,96)", absent)
		}
	}
	// The same discipline for the status body's `terminal`, which is false on a
	// SUCCESSFUL DELIVERED payment (service.ts:61,203).
	if _, found := reflect.TypeOf(StatusResult{}).FieldByName("Terminal"); found {
		t.Error("h3.StatusResult must NOT carry Terminal: it is false for DELIVERED, so reading it " +
			"would poll a finished payment forever")
	}
}

// A response nobody can classify must not inherit PreSign's default reading. Post-sign
// is the fail-closed answer: keep the hold, reconcile through status.
func TestPay_UnclassifiableResponseIsNotAnH3Error(t *testing.T) {
	bodies := []string{
		// an interposed proxy, not the sidecar
		`<html>502 Bad Gateway</html>`,
		// the envelope shape with no stable code
		`{"error":{"message":"no code here"}}`,
		// a present but blank code
		`{"error":{"code":"","message":"blank"}}`,
	}
	for _, body := range bodies {
		c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
			writeH3(w, http.StatusBadGateway, body)
		})
		_, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
		if err == nil {
			t.Fatalf("a non-2xx must be an error (body %s)", body)
		}
		var he *Error
		if errors.As(err, &he) {
			t.Errorf("body %s became a classifiable *h3.Error (code %q); an unclassifiable outcome must "+
				"NOT be readable as pre-sign", body, he.Code)
		}
	}
}

// A transport failure may have arrived at the sidecar and signed. It must never be
// classifiable, or a lost response would release a hold behind a live authorization.
func TestPay_TransportFailureIsNotAnH3Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := New(srv.URL, testToken, srv.Client())
	srv.Close()

	_, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
	if err == nil {
		t.Fatal("pay against a dead sidecar must fail")
	}
	var he *Error
	if errors.As(err, &he) {
		t.Fatalf("a transport failure became %s: the request may have arrived and signed, so the hold must stand", he.Code)
	}
}

// ── pay: the refusals that never leave the process ──────────────────────────

// Every one of these is provably pre-sign (nothing was sent), and each is a wiring
// mistake the sidecar would either reject on arrival or, worse, accept and then fail on.
func TestPay_RefusesLocallyWithoutTouchingTheSidecar(t *testing.T) {
	frac := fixtureIntent()
	frac.ExpiresAt = "2026-07-22T10:05:00.000Z"
	fracToken := fixtureToken()
	fracToken.ExpiresAt = frac.ExpiresAt
	SignApproval([]byte(testSecret), &fracToken)

	decimal := fixtureIntent()
	decimal.Amount = "0.04"
	decimalToken := fixtureToken()
	decimalToken.ApprovedAmount = "0.04"
	SignApproval([]byte(testSecret), &decimalToken)

	pending := fixtureIntent()
	pending.Decision = "HUMAN_APPROVAL_REQUIRED"
	pendingToken := fixtureToken()
	pendingToken.Decision = pending.Decision
	SignApproval([]byte(testSecret), &pendingToken)

	noNonce := fixtureIntent()
	noNonce.Nonce = ""

	windowSkew := fixtureIntent()
	skewToken := fixtureToken()
	skewToken.ExpiresAt = "2026-07-22T10:06:00Z"
	SignApproval([]byte(testSecret), &skewToken)

	amountSkew := fixtureIntent()
	amountSkewToken := fixtureToken()
	amountSkewToken.ApprovedAmount = "50000"
	SignApproval([]byte(testSecret), &amountSkewToken)

	shortSig := fixtureIntent()
	shortSigToken := fixtureToken()
	shortSigToken.Signature = "DEADBEEF"

	cases := []struct {
		name   string
		intent WireIntent
		token  ApprovalToken
		code   string
	}{
		// The single most likely first-run failure: RFC3339Nano on the wire against
		// approval.CanonicalWire's second precision (hash.go:201).
		{"sub-second expiresAt", frac, fracToken, CodeBadRequest},
		{"decimal amount, not atomic", decimal, decimalToken, CodeBadRequest},
		{"decision still pending", pending, pendingToken, CodeIntentNotApproved},
		{"no idempotency nonce", noNonce, fixtureToken(), CodeBadRequest},
		{"token authorizes another window", windowSkew, skewToken, CodeApprovalTokenInvalid},
		{"token authorizes another amount", amountSkew, amountSkewToken, CodeApprovedAmountMismatch},
		{"signature is not 64 lowercase hex", shortSig, shortSigToken, CodeBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("a locally refused pay must never reach the sidecar")
				writeH3(w, http.StatusOK, payDelivered200)
			})
			_, err := c.Pay(context.Background(), tc.intent, tc.token)
			he := asH3(t, err)
			if rec.calls != 0 {
				t.Fatalf("%d requests left the process; a pre-flight refusal must send nothing", rec.calls)
			}
			if he.Code != tc.code {
				t.Fatalf("code = %s, want %s (message: %s)", he.Code, tc.code, he.Message)
			}
			if he.HTTPStatus != 0 {
				t.Errorf("HTTPStatus = %d, want 0 to mark a refusal raised before any request left", he.HTTPStatus)
			}
			if !he.PreSign() {
				t.Fatal("a refusal that sent nothing is provably pre-sign; reporting otherwise pins budget forever")
			}
		})
	}
}

// ── status ──────────────────────────────────────────────────────────────────

func TestStatus_DeliveredIgnoresTerminalAndCommitsAtAmountPaid(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, statusDelivered200)
	})
	out, err := c.Status(context.Background(), gv1PaymentID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/v1/status/"+gv1PaymentID {
		t.Errorf("status hit %s %s, want GET /v1/status/%s", rec.method, rec.path, gv1PaymentID)
	}
	if rec.auth != "Bearer "+testToken {
		t.Errorf("status must carry the bearer token, got %q", rec.auth)
	}
	if !Committed(out.State) {
		t.Fatalf("state %q must classify as committed even though the body says terminal:false", out.State)
	}
	if out.AmountPaid != "40000" || out.AmountReserved != "40000" {
		t.Errorf("amountPaid/amountReserved = %q/%q, want 40000/40000", out.AmountPaid, out.AmountReserved)
	}
	if out.Receipt != nil {
		t.Error("a null receipt must decode to nil, not to a zero-valued receipt")
	}
	if out.Reason != "" {
		t.Errorf("a null reason must decode to the empty string, got %q", out.Reason)
	}
}

// The 404 that means "nothing was ever signed" is the CODE, not the status. Route-not-
// found is also a 404 (service.ts:478) and means nothing of the kind.
func TestStatus_NotFoundIsClassifiedByCodeNotByHTTPStatus(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusNotFound, envelope(CodePaymentNotFound, "no payment record for "+gv1PaymentID, "null", false))
	})
	_, err := c.Status(context.Background(), gv1PaymentID)
	he := asH3(t, err)
	if he.Code != CodePaymentNotFound || he.HTTPStatus != http.StatusNotFound {
		t.Fatalf("got %s/HTTP %d, want PAYMENT_NOT_FOUND/404", he.Code, he.HTTPStatus)
	}
	if !he.PreSign() {
		t.Error("no durable record means nothing was signed: the hold is safe to release in full")
	}

	// Same status, different code, entirely different meaning.
	c2, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusNotFound, envelope(CodeBadRequest, "no such route: GET /v1/statuz/x", "null", false))
	})
	_, err = c2.Status(context.Background(), gv1PaymentID)
	he2 := asH3(t, err)
	if he2.Code == CodePaymentNotFound {
		t.Fatal("a route-not-found 404 must never read as PAYMENT_NOT_FOUND, or a misrouted client would " +
			"release holds it never resolved")
	}
	if he2.Code != CodeBadRequest {
		t.Errorf("code = %s, want BAD_REQUEST", he2.Code)
	}
}

// SIGNED, SUBMITTED and RECONCILING all mean the outcome is unknown, so the hold stands.
func TestStatus_UnresolvedStatesKeepTheHold(t *testing.T) {
	for _, state := range []string{StateSigned, StateSubmitted, StateReconciling} {
		body := strings.Replace(statusDelivered200, `"state": "DELIVERED"`, `"state": "`+state+`"`, 1)
		c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
			writeH3(w, http.StatusOK, body)
		})
		out, err := c.Status(context.Background(), gv1PaymentID)
		if err != nil {
			t.Fatalf("status %s: %v", state, err)
		}
		if !Unresolved(out.State) || Committed(out.State) {
			t.Errorf("%s must be unresolved and not committed", state)
		}
	}
	// FAILED and EXPIRED are neither: nothing settled, so release.
	for _, state := range []string{StateFailed, StateExpired} {
		if Unresolved(state) || Committed(state) {
			t.Errorf("%s must be neither unresolved nor committed", state)
		}
	}
}

func TestStatus_RefusesAnEmptyPaymentID(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("status with no id must not reach the sidecar")
	})
	if _, err := c.Status(context.Background(), "  "); err == nil {
		t.Fatal("an empty paymentId must be refused")
	}
	if rec.calls != 0 {
		t.Fatalf("%d requests left the process", rec.calls)
	}
}

// ── quote ───────────────────────────────────────────────────────────────────

func TestQuote_CopiesTheAcceptVerbatim(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, quote200)
	})
	q, err := c.Quote(context.Background(), profileResource, 5042002)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/quote" {
		t.Errorf("quote hit %s %s, want POST /v1/quote", rec.method, rec.path)
	}
	var sent map[string]any
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("quote body does not parse: %v", err)
	}
	if sent["resource"] != profileResource {
		t.Errorf("resource = %v, want %s", sent["resource"], profileResource)
	}
	// The sidecar requires chainId to be a JSON NUMBER (service.ts:221).
	if _, isNumber := sent["chainId"].(float64); !isNumber {
		t.Errorf("chainId is %T on the wire, must be a number", sent["chainId"])
	}
	// Verbatim, not checksummed and not lowercased: the live equality check is
	// case-insensitive (buyer.ts:199) but intentHash is not, so a re-cased payee passes
	// the check and hashes to something the sidecar never computed.
	if q.Accept.PayTo != deadPayToMixed {
		t.Fatalf("accept.payTo = %q, want the mixed-case %q byte for byte", q.Accept.PayTo, deadPayToMixed)
	}
	if q.Accept.Amount != "40000" || q.Price != "40000" {
		t.Errorf("price = %q / %q, want the atomic 40000", q.Accept.Amount, q.Price)
	}
	if q.Accept.MaxTimeoutSeconds != 300 {
		t.Errorf("maxTimeoutSeconds = %d, want 300", q.Accept.MaxTimeoutSeconds)
	}
	if q.Accept.Extra == nil || q.Accept.Extra.Name != "USD Coin" || q.Accept.Extra.Version != "2" {
		t.Errorf("accept.extra did not survive: %+v", q.Accept.Extra)
	}
}

// An unusable challenge must be named as such at the quote, not discovered as a
// MERCHANT_CHANGED at pay time, which reads like a substituting seller.
func TestQuote_RefusesAnAcceptWithNoPayee(t *testing.T) {
	body := strings.Replace(quote200, `"payTo": "0x000000000000000000000000000000000000dEaD"`, `"payTo": ""`, 1)
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, body)
	})
	_, err := c.Quote(context.Background(), profileResource, 5042002)
	he := asH3(t, err)
	if he.Code != CodeChallengeUnavailable {
		t.Fatalf("code = %s, want CHALLENGE_UNAVAILABLE", he.Code)
	}
}

func TestQuote_RefusesANonURLResourceBeforeSending(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a display-string resource must not reach the sidecar")
	})
	// The production intent shape: a display string, not a URL. The sidecar would answer
	// 400 BAD_REQUEST "resource is not a valid URL" (service.ts:230).
	if _, err := c.Quote(context.Background(), "GET /v1/company-profile", 5042002); err == nil {
		t.Fatal("a non-URL resource must be refused")
	}
	if rec.calls != 0 {
		t.Fatalf("%d requests left the process", rec.calls)
	}
}

func TestQuote_PropagatesTheUnauthenticatedEnvelope(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusUnauthorized, envelope(CodeUnauthenticated, "missing or invalid bearer token", "null", false))
	})
	_, err := c.Quote(context.Background(), profileResource, 5042002)
	he := asH3(t, err)
	if he.Code != CodeUnauthenticated {
		t.Fatalf("code = %s, want UNAUTHENTICATED", he.Code)
	}
}

// ── health and wiring ───────────────────────────────────────────────────────

// /health is checked BEFORE the /v1 auth branch (service.ts:450-459), so it must go out
// without an authorization header: boot has to be able to probe reachability before it
// trusts the token.
func TestHealth_SendsNoBearerToken(t *testing.T) {
	c, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, health200)
	})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if rec.path != "/health" || rec.method != http.MethodGet {
		t.Errorf("health hit %s %s", rec.method, rec.path)
	}
	if rec.auth != "" {
		t.Errorf("health carried authorization %q; the token must not be spent on an unauthenticated route", rec.auth)
	}
}

func TestHealth_RefusesOKFalse(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		writeH3(w, http.StatusOK, strings.Replace(health200, `"ok": true`, `"ok": false`, 1))
	})
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("ok:false must not read as healthy: wiring a payment lane to it would fail mid-flight")
	}
}

// An empty base URL is the documented default, and a non-http one is a wiring mistake
// that must surface with its own URL in the message rather than as a transport error.
func TestNew_DefaultsToLoopbackAndRefusesANonHTTPOrigin(t *testing.T) {
	if got := New("", testToken, nil).BaseURL(); got != DefaultBaseURL {
		t.Errorf("default base URL = %q, want %q", got, DefaultBaseURL)
	}
	if got := New("http://127.0.0.1:4020/", testToken, nil).BaseURL(); got != DefaultBaseURL {
		t.Errorf("a trailing slash must be trimmed, got %q", got)
	}
	for _, bad := range []string{"ftp://127.0.0.1:4020", "127.0.0.1:4020", "://"} {
		c := New(bad, testToken, nil)
		_, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
		he := asH3(t, err)
		if !strings.Contains(he.Message, bad) {
			t.Errorf("the refusal for %q must name the URL, got %q", bad, he.Message)
		}
		if !he.PreSign() {
			t.Errorf("an unreachable-by-configuration client sent nothing, so %q must classify pre-sign", bad)
		}
	}
}

// Without a token the /v1 routes are unreachable, and spending a round trip to learn
// that is worse than refusing: the sidecar answers 401 UNAUTHENTICATED anyway. /health
// still goes out, because it is the one route that needs no token.
func TestPay_RefusesWhenNoBearerTokenIsConfigured(t *testing.T) {
	var v1Calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			v1Calls++
			t.Errorf("an unauthenticated %s must not reach the sidecar", r.URL.Path)
		}
		writeH3(w, http.StatusOK, health200)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "", srv.Client())

	_, err := c.Pay(context.Background(), fixtureIntent(), fixtureToken())
	he := asH3(t, err)
	if he.Code != CodeUnauthenticated {
		t.Fatalf("code = %s, want UNAUTHENTICATED", he.Code)
	}
	if !he.PreSign() {
		t.Error("a pay that was never sent is pre-sign")
	}
	if _, err := c.Status(context.Background(), gv1PaymentID); err == nil {
		t.Error("an unauthenticated status must be refused too")
	}
	if v1Calls != 0 {
		t.Fatalf("%d /v1 requests left the process without a token", v1Calls)
	}
	if err := c.Health(context.Background()); err != nil {
		t.Errorf("health needs no token and must still work: %v", err)
	}
}

// The package must stay pure. Nothing here may import approval, funding, purchasing,
// store or policy: the bearer token and the H2 secret are spend capabilities, and this
// package exists so funding can hold them without any other package reaching a signer.
func TestPackage_HoldsNoSecretsOfItsOwn(t *testing.T) {
	// A Client carries a token because it must send one, but nothing exposes it and no
	// method returns it. If a getter appears, this test is the place the argument is had.
	ct := reflect.TypeOf(Client{})
	for f := 0; f < ct.NumField(); f++ {
		if name := ct.Field(f).Name; name == "Token" || name == "Secret" || name == "ApprovalSecret" {
			t.Errorf("Client.%s is exported; the bearer token and the H2 secret must never be readable "+
				"off this struct", name)
		}
	}
	// SignApproval borrows the secret for one call and keeps nothing.
	tok := fixtureToken()
	secret := []byte(testSecret)
	SignApproval(secret, &tok)
	if string(secret) != testSecret {
		t.Error("SignApproval mutated the caller's secret")
	}

	// Purity, structurally. A source scan rather than reflection, for the same reason
	// brain/jobs_projection_test.go scans source: the import graph cannot express "this
	// package must not GROW an edge", only what it currently has. The needle is assembled
	// from two halves so this file does not match itself.
	needle := "snapfall/daemon/" + "internal/"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(raw), needle) {
			t.Errorf("%s reaches another internal/ package. internal/h3 must import nothing from internal/: "+
				"that is what lets funding hold the bearer token and H2_APPROVAL_SECRET without any other "+
				"package acquiring a path to a signer", e.Name())
		}
	}
}
