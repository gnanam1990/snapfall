// V6's done-criterion, in tests: "a reserved-then-failed payment releases budget
// correctly" (design §5.3), plus the regression that protects the degraded mode every
// other reader depends on (§6 step 10).
//
// The two release paths must never blur, so each is pinned separately and each asserts the
// thing the other must NOT do:
//
//   - PRE-SIGN (the sidecar refused before signing anything): the hold comes back in the
//     same call, pre_sign true, and /v1/status is never consulted.
//   - POST-SIGN (a signed authorization may still settle): NOTHING is released on the
//     failure alone. The hold stands until a status poll reads a terminal state, which is
//     the boot reconciler's job, not this package's.
//
// The fake Payer is the instrument: it records every call, so these tests assert on what
// left the process (or did not), never merely on what the outcome struct says.
package purchasing

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/budget"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The sidecar's own test vectors (sidecar/src/service-test.ts:113-156), reused so a Go fake
// and the real process are exercised against the same payee, asset and chain id. payTestPayTo
// is deliberately MIXED case: it is copied verbatim through the whole pipeline, because the
// sidecar's payee comparison is case-insensitive while intentHash is not.
const (
	payTestPayTo       = "0x000000000000000000000000000000000000dEaD"
	payTestAsset       = "0x0000000000000000000000000000000000000001"
	payTestNetwork     = "eip155:5042002"
	payTestChainID     = int64(5042002)
	payTestResourceURL = "http://127.0.0.1:4021/v1/company-profile"
	// The $0.04 profile beat. Atomic USDC and micros are the same number (6dp).
	payTestAmountAtomic = "40000"
	payTestAmountMicros = int64(40_000)
	// The HMAC key the sidecar would verify. A test value only, and deliberately not spelled
	// with the environment variable's name: AT-15 pins which packages may name that.
	payTestApprovalSecret = "test-approval-secret-0123456789ab"
	payTestJobID          = "job_104"
)

// ── the fake payment lane ────────────────────────────────────────────────────

// fakePayCall is one observed /v1/pay body: exactly what the daemon would have put on the
// wire, which is what lets these tests check the two naming conventions crossing.
type fakePayCall struct {
	Intent h3.WireIntent
	Token  h3.ApprovalToken
}

// fakePayer is a funding.Payer that reaches no network and records everything.
type fakePayer struct {
	mu       sync.Mutex
	pays     []fakePayCall
	quotes   int
	statuses int

	// quoteAmount prices the challenge ("" = payTestAmountAtomic).
	quoteAmount string
	// deliver makes Pay answer the way the sidecar answers a completed payment, DERIVING
	// the receipt from the body it was handed, so a test never has to guess an intentHash.
	deliver bool
	// paidAtomic overrides amountPaid ("" = payTestAmountAtomic), payee overrides
	// receipt.payee ("" = the wire's own payee, i.e. no divergence).
	paidAtomic string
	payee      string
	// unresolvedState makes Pay answer 200 with a state that is neither committed nor
	// failed (SIGNED / RECONCILING): money may have moved and nobody knows yet.
	unresolvedState string
	// payErr, when set, is returned by Pay instead of a result.
	payErr error
	// status is what Status answers, standing in for the sidecar's durable record.
	status h3.StatusResult
}

// The fake must satisfy the real lane contract, or these tests prove nothing about it.
var _ funding.Payer = (*fakePayer)(nil)

func (p *fakePayer) Quote(_ context.Context, resource string, _ int64) (h3.Quote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotes++
	amount := nonEmpty(p.quoteAmount, payTestAmountAtomic)
	return h3.Quote{
		Resource: resource,
		Network:  payTestNetwork,
		Accept: h3.AcceptOption{
			Scheme: "exact", Network: payTestNetwork, Amount: amount,
			Asset: payTestAsset, PayTo: payTestPayTo, MaxTimeoutSeconds: 60,
		},
		Price: amount, PriceDisplay: "0.040000",
	}, nil
}

func (p *fakePayer) Pay(_ context.Context, in h3.WireIntent, tok h3.ApprovalToken) (h3.PayResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pays = append(p.pays, fakePayCall{Intent: in, Token: tok})
	if p.payErr != nil {
		return h3.PayResult{}, p.payErr
	}
	if p.unresolvedState != "" {
		return h3.PayResult{
			PaymentID:  h3.PaymentID(tok.IntentHash, in.Nonce),
			State:      p.unresolvedState,
			IntentHash: tok.IntentHash,
		}, nil
	}
	if !p.deliver {
		return h3.PayResult{}, nil
	}
	paid := nonEmpty(p.paidAtomic, payTestAmountAtomic)
	return h3.PayResult{
		PaymentID:  h3.PaymentID(tok.IntentHash, in.Nonce),
		State:      h3.StateDelivered,
		AmountPaid: paid,
		IntentHash: tok.IntentHash,
		Receipt: &h3.Receipt{
			Resource: in.Resource, Amount: paid, Asset: in.Asset, Network: in.Network,
			Payer: "0x00000000000000000000000000000000000000a1",
			// The seller echoes the payee with ITS own casing; by default the same address
			// the challenge advertised.
			Payee: nonEmpty(p.payee, in.Merchant), AuthNonce: h3.AuthNonce(tok.IntentHash),
			Settlement: "NOT_BROADCAST",
		},
		Data: []byte(`{"ok":true}`),
	}, nil
}

func (p *fakePayer) Status(_ context.Context, _ string) (h3.StatusResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses++
	return p.status, nil
}

// counts reports how many times each lane call was made.
func (p *fakePayer) counts() (pays, quotes, statuses int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pays), p.quotes, p.statuses
}

// lastPay returns the most recent observed pay body, failing the test when none exists.
func (p *fakePayer) lastPay(t *testing.T) fakePayCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pays) == 0 {
		t.Fatal("the payer received no pay call at all")
	}
	return p.pays[len(p.pays)-1]
}

// ── the fixture: the REAL pipeline, with the ledger live ─────────────────────

// payFixture is the whole spine: one store, one clock driving both the lifecycle and the
// purchaser, a live reservation ledger wired into all three approval hooks, and a funding
// agent whose only lane is the fake payer.
type payFixture struct {
	p     *Purchaser
	life  *approval.Lifecycle
	led   *budget.Ledger
	fund  *funding.Agent
	clock *fakeClock
	st    *store.Store
}

// newPayFixture builds the pipeline. A nil payer wires the lane with SetPayer(nil, nil, 0),
// which is the degraded mode step 10 pins.
func newPayFixture(t *testing.T, payer *fakePayer) *payFixture {
	t.Helper()
	st, fc, led := newLedgerFixture(t)

	life := approval.New(st, fc.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	// The three V6 hooks, all live: the ledger IS the spend truth here, not a constant zero.
	life.Spend = led.Spend
	life.JobGate = led.JobGate
	life.Reserve = reserveHook(led)
	life.OrgGate = led.OrgGate

	fund := funding.New()
	if payer == nil {
		fund.SetPayer(nil, nil, 0)
	} else {
		fund.SetPayer(payer, []byte(payTestApprovalSecret), payTestChainID)
	}

	return &payFixture{
		p:     New(life, st, fund, led, fc, "org_demo", 5*time.Minute),
		life:  life,
		led:   led,
		fund:  fund,
		clock: fc,
		st:    st,
	}
}

// newLedgerFixture opens a store and a ledger over one fake clock, for the tests that need
// the ledger alone (Sweep and the fold) and no pipeline at all.
func newLedgerFixture(t *testing.T) (*store.Store, *fakeClock, *budget.Ledger) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "pay.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fc := newFakeClock()
	return st, fc, budget.New(st, fc.Now)
}

// reserveHook is the Intent-to-Hold translation the WIRING layer owns: package budget
// imports neither approval nor funding on purpose, so the translation cannot live there.
// It is reproduced here (rather than faked) so these tests exercise the same durable
// ordering production does: the hold lands before the request is visible, and it carries
// the precomputed payment id the reconciler needs.
func reserveHook(led *budget.Ledger) func(context.Context, approval.Intent, string) error {
	return func(ctx context.Context, in approval.Intent, requestID string) error {
		hash := approval.WireHash(in)
		return led.Reserve(ctx, budget.Hold{
			RequestID: requestID, IntentID: in.IntentID, JobID: in.JobID, OrgID: in.OrgID,
			// H3 §7 step 2 holds intent.maxAmount, which intake has made equal to the
			// evaluated amount.
			ReserveMicros: in.MaxAmountMicros,
			PaymentID:     h3.PaymentID(hash, in.Nonce),
			Nonce:         in.Nonce,
			Merchant:      in.Merchant,
			PayTo:         in.PayTo,
			ExpiresAt:     in.ExpiresAt,
		})
	}
}

// payIntent is a Brain-stamped purchase of the $0.04 profile at an allowlisted merchant,
// with an ABSOLUTE resource URL (what discovery now produces, and what the sidecar probes).
func payIntent(amountMicros int64) brain.PurchaseIntent {
	return brain.PurchaseIntent{
		JobID: payTestJobID, AgentKind: "due-diligence",
		Merchant: policy.DemoMerchantProfile, Resource: payTestResourceURL,
		AmountMicros: amountMicros, MaxAmountMicros: amountMicros, Purpose: "company profile",
	}
}

// ── event readers ────────────────────────────────────────────────────────────

// eventPayload is every payload key these tests read, across every kind. Unknown keys are
// ignored by encoding/json, which is what lets one struct serve them all. Money is read as
// int64 here and never through a map[string]any (a JSON number decodes to float64, and
// money never touches a float in this codebase).
type eventPayload struct {
	RequestID       string `json:"request_id"`
	JobID           string `json:"job_id"`
	Merchant        string `json:"merchant"`
	PayTo           string `json:"pay_to"`
	ApprovedPayTo   string `json:"approved_pay_to"`
	Resource        string `json:"resource"`
	AmountMicros    int64  `json:"amount_micros"`
	ReservedMicros  int64  `json:"reserved_micros"`
	ReleasedMicros  int64  `json:"released_micros"`
	CommittedMicros int64  `json:"committed_micros"`
	PaymentID       string `json:"payment_id"`
	ReceiptHash     string `json:"receipt_hash"`
	AuthNonce       string `json:"auth_nonce"`
	Payee           string `json:"payee"`
	ReceiptPayee    string `json:"receipt_payee"`
	State           string `json:"state"`
	Code            string `json:"code"`
	Reason          string `json:"reason"`
	PreSign         bool   `json:"pre_sign"`
	Note            string `json:"note"`
}

// payloadsOf returns every payload of kind in durable seq order.
func payloadsOf(t *testing.T, st *store.Store, kind string) []eventPayload {
	t.Helper()
	var out []eventPayload
	for _, raw := range rawPayloadsOf(t, st, kind) {
		var p eventPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("decoding a %s payload: %v", kind, err)
		}
		out = append(out, p)
	}
	return out
}

// rawPayloadsOf returns the payload JSON of every event of kind, in durable seq order.
func rawPayloadsOf(t *testing.T, st *store.Store, kind string) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT payload_json FROM events WHERE kind = ? ORDER BY seq`, kind)
	if err != nil {
		t.Fatalf("reading %s events: %v", kind, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scanning a %s event: %v", kind, err)
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating %s events: %v", kind, err)
	}
	return out
}

// one returns the single payload of kind, failing the test on any other count. Money
// events are exactly-once facts, so "how many" is always part of the assertion.
func one(t *testing.T, st *store.Store, kind string) eventPayload {
	t.Helper()
	got := payloadsOf(t, st, kind)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 %s event, got %d", kind, len(got))
	}
	return got[0]
}

// none asserts that no event of kind exists.
func none(t *testing.T, st *store.Store, kind string) {
	t.Helper()
	if got := payloadsOf(t, st, kind); len(got) != 0 {
		t.Fatalf("want no %s event, got %d: %+v", kind, len(got), got)
	}
}

// ── §5.3: the two release paths ──────────────────────────────────────────────

// POST-SIGN. The sidecar answers the captured PAYMENT_REJECTED envelope (402, retriable
// false), which H3 §2.2 classifies as post-signature: the authorization was signed and may
// still settle. Asserted, in order:
//
//  1. after intake the hold exists at the full reserved figure (the ledger IS the spend
//     truth now, so this is also the proof rule 1 went live);
//  2. the 402 alone releases NOTHING, and the purchase path does not poll status either;
//  3. the reconcile step, reading FAILED from the sidecar's durable record, brings the
//     budget back to zero;
//  4. exactly ONE budget.released event exists, for the full figure, with pre_sign FALSE.
func TestV6_ReservedThenPostSignFailureReleasesAfterReconcile(t *testing.T) {
	ctx := context.Background()
	payer := &fakePayer{
		payErr: &h3.Error{
			HTTPStatus: 402, Code: h3.CodePaymentRejected, Retriable: false,
			Message: "the facilitator rejected the payment authorization",
		},
		status: h3.StatusResult{State: h3.StateFailed, Reason: "payment rejected"},
	}
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "payment-failed" {
		t.Fatalf("a rejected payment reported %+v; want denied/payment-failed", out)
	}
	if out.Receipt != nil || out.Data != nil {
		t.Fatal("a rejected payment produced a receipt or data: nothing may be fabricated for money that did not arrive")
	}
	if out.RequestID == "" {
		t.Fatal("the denied outcome names no request id, so no reconciler could ever find its hold")
	}

	// 1. the hold stands at the reserved figure.
	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != payTestAmountMicros {
		t.Fatalf("job committed spend is %d micros after intake, want the %d held", got, payTestAmountMicros)
	}
	// 2. nothing was released, and status was NOT consulted from the payment path.
	none(t, f.st, budget.KindReleased)
	if pays, _, statuses := payer.counts(); pays != 1 || statuses != 0 {
		t.Fatalf("the payer saw %d pay(s) and %d status poll(s); want exactly one pay and no poll "+
			"(a post-sign failure is resolved by the boot reconciler, not inside the purchase)", pays, statuses)
	}

	// 3. the reconcile step. This is the BOOT reconciler's work (design §2.5 path 2, wired
	// in cmd/snapfall): take the hold's own PRECOMPUTED payment id, poll it, then act on
	// what the sidecar's durable record says.
	//
	// Sweep first, because it is what re-reads the durable execution claims: this hold has
	// one (approval.Execute's write-ahead payment.executing), which is exactly why Sweep
	// must leave it alone and why it belongs in the reconciler's set instead.
	if n, err := f.led.Sweep(ctx, f.clock.Now()); err != nil || n != 0 {
		t.Fatalf("Sweep released %d hold(s) (err %v); a claimed hold may already be a signed bearer instrument", n, err)
	}
	held := f.led.Unresolved()
	if len(held) != 1 {
		t.Fatalf("the reconciler's set holds %d reservation(s), want the one claimed-but-unresolved hold", len(held))
	}
	wantPaymentID := h3.PaymentID(payer.lastPay(t).Token.IntentHash, payer.lastPay(t).Intent.Nonce)
	if held[0].PaymentID != wantPaymentID {
		t.Fatalf("the hold carries payment id %q, want the precomputed %q: the reconciler can only reach /v1/status "+
			"through this value, since a PAYMENT_IN_PROGRESS envelope may carry none", held[0].PaymentID, wantPaymentID)
	}
	// FAILED is neither committed nor unresolved, so nothing settled and the release is
	// now provable.
	sres, err := payer.Status(ctx, held[0].PaymentID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if h3.Committed(sres.State) || h3.Unresolved(sres.State) {
		t.Fatalf("fixture is wrong: %s must be a terminal non-settling state for this test to mean anything", sres.State)
	}
	if err := f.led.Release(ctx, out.RequestID, "reconciled from /v1/status: "+sres.Reason, h3.CodePaymentRejected, false); err != nil {
		t.Fatalf("release after reconcile: %v", err)
	}

	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != 0 {
		t.Fatalf("job committed spend is %d micros after the reconcile released the hold, want 0", got)
	}
	// 4. one release, full figure, and NOT pre-sign: the log must not claim we knew nothing
	// was signed, because we did not.
	rel := one(t, f.st, budget.KindReleased)
	if rel.RequestID != out.RequestID || rel.ReleasedMicros != payTestAmountMicros {
		t.Fatalf("release recorded %+v; want request %s at %d micros", rel, out.RequestID, payTestAmountMicros)
	}
	if rel.PreSign {
		t.Error("the post-sign release was recorded as pre_sign: that would claim proof the daemon never had")
	}
	if rel.Code != h3.CodePaymentRejected {
		t.Errorf("release code %q, want the H3 code %q that caused it", rel.Code, h3.CodePaymentRejected)
	}
}

// PRE-SIGN. MERCHANT_CHANGED (409) is raised inside the sidecar's probe, strictly before
// its write-ahead record exists, so nothing was signed. The hold comes back in the SAME
// call, recorded pre_sign, and /v1/status is never consulted. The counterpart to the test
// above: the two release paths must not blur.
func TestV6_PreSignRejectionReleasesImmediately(t *testing.T) {
	ctx := context.Background()
	payer := &fakePayer{
		payErr: &h3.Error{
			HTTPStatus: 409, Code: h3.CodeMerchantChanged, Retriable: false,
			Message: "intent.merchant does not match the live accept.payTo",
		},
	}
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "payment-failed" {
		t.Fatalf("a pre-sign refusal reported %+v; want denied/payment-failed", out)
	}
	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != 0 {
		t.Fatalf("job committed spend is %d micros after a provably unsigned payment, want 0 (the hold must be back)", got)
	}
	rel := one(t, f.st, budget.KindReleased)
	if !rel.PreSign {
		t.Error("the pre-sign release was not recorded as pre_sign, which is the only evidence the release was safe")
	}
	if rel.ReleasedMicros != payTestAmountMicros || rel.Code != h3.CodeMerchantChanged {
		t.Fatalf("release recorded %+v; want %d micros and code %s", rel, payTestAmountMicros, h3.CodeMerchantChanged)
	}
	if _, _, statuses := payer.counts(); statuses != 0 {
		t.Fatalf("a pre-sign refusal polled /v1/status %d time(s): there is no durable record to poll, and polling one "+
			"would make a provable release look conditional", statuses)
	}
	// The hold is CLOSED, so it is neither sweepable nor in the reconciler's set: a released
	// hold must never be released a second time. (Sweep is what re-reads the durable
	// execution claims, so calling it here also refreshes what Unresolved can see.)
	if n, err := f.led.Sweep(ctx, f.clock.Now()); err != nil || n != 0 {
		t.Fatalf("Sweep released %d hold(s) (err %v) for a hold that was already released", n, err)
	}
	if held := f.led.Unresolved(); len(held) != 0 {
		t.Fatalf("a released hold is still in the reconciler's set: %+v", held)
	}
}

// The DELIVERED path: the budget is committed at the SIDECAR's figure, the receipt carries
// the deterministic join key, and the wire body shows the two naming conventions crossing
// exactly once (the payee ADDRESS on `merchant`, the absolute URL on `resource`).
func TestV6_DeliveredPaymentCommitsAtTheSidecarsFigure(t *testing.T) {
	ctx := context.Background()
	payer := &fakePayer{deliver: true}
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "delivered" || out.Decision != string(policy.AutoApprove) {
		t.Fatalf("a completed payment reported %+v; want AUTO_APPROVE/delivered", out)
	}
	if out.Receipt == nil {
		t.Fatal("a delivered purchase carries no receipt, so Billing has nothing to join on")
	}

	call := payer.lastPay(t)
	if call.Intent.Merchant != payTestPayTo {
		t.Errorf("the wire carried merchant %q, want the payee ADDRESS %q verbatim (a HOST fails MERCHANT_CHANGED)",
			call.Intent.Merchant, payTestPayTo)
	}
	if call.Intent.Resource != payTestResourceURL {
		t.Errorf("the wire carried resource %q, want the absolute URL %q (a display string fails RESOURCE_NOT_FOUND)",
			call.Intent.Resource, payTestResourceURL)
	}
	if call.Intent.Amount != payTestAmountAtomic || call.Intent.MaxAmount != payTestAmountAtomic {
		t.Errorf("the wire carried amount %q / maxAmount %q, want both at the quoted %q",
			call.Intent.Amount, call.Intent.MaxAmount, payTestAmountAtomic)
	}

	wantPaymentID := h3.PaymentID(call.Token.IntentHash, call.Intent.Nonce)
	wantReceiptHash := h3.AuthNonce(call.Token.IntentHash)
	if out.Receipt.PaymentID != wantPaymentID {
		t.Errorf("receipt payment id %q, want the precomputed %q", out.Receipt.PaymentID, wantPaymentID)
	}
	if out.Receipt.ReceiptHash != wantReceiptHash {
		t.Errorf("receipt hash %q, want the AuthNonce %q (H3 emits no receiptHash; this is Billing's join key)",
			out.Receipt.ReceiptHash, wantReceiptHash)
	}
	if out.Receipt.AmountAtomic != payTestAmountAtomic {
		t.Errorf("receipt amount %q, want the sidecar's %q", out.Receipt.AmountAtomic, payTestAmountAtomic)
	}

	com := one(t, f.st, budget.KindCommitted)
	if com.CommittedMicros != payTestAmountMicros || com.ReleasedMicros != 0 {
		t.Fatalf("commit recorded %+v; want %d committed and 0 released (paid == reserved here)", com, payTestAmountMicros)
	}
	if com.PaymentID != wantPaymentID || com.ReceiptHash != wantReceiptHash || com.State != h3.StateDelivered {
		t.Fatalf("commit recorded %+v; want the precomputed ids and state %s", com, h3.StateDelivered)
	}
	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != payTestAmountMicros {
		t.Fatalf("job committed spend is %d micros after a settled purchase, want %d: committed spend counts", got, payTestAmountMicros)
	}

	del := one(t, f.st, kindDelivered)
	if del.AmountMicros != payTestAmountMicros || del.PayTo != payTestPayTo || del.Resource != payTestResourceURL {
		t.Fatalf("the delivered record is %+v; want the paid figure, the payee and the probed URL", del)
	}
	// The honest stop must NOT also fire for a real purchase.
	none(t, f.st, kindPendingSettlement)
	none(t, f.st, kindPayeeDivergence)
}

// A seller that echoes a DIFFERENT payee than the one the owner approved is recorded, not
// refused: the money already moved, and failing the purchase afterwards would be a lie in
// the other direction (V6 §3.4).
func TestV6_PayeeDivergenceIsRecordedNotRefused(t *testing.T) {
	ctx := context.Background()
	const otherPayee = "0x000000000000000000000000000000000000bEEF"
	payer := &fakePayer{deliver: true, payee: otherPayee}
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "delivered" {
		t.Fatalf("a diverging payee failed a payment that already moved money: %+v", out)
	}
	div := one(t, f.st, kindPayeeDivergence)
	if div.ApprovedPayTo != payTestPayTo || div.ReceiptPayee != otherPayee {
		t.Fatalf("the divergence record is %+v; want approved %s versus returned %s", div, payTestPayTo, otherPayee)
	}
	// Case is NOT divergence: the same address in different casing must not raise one, or
	// every EIP-55 checksummed echo would.
	f2 := newPayFixture(t, &fakePayer{deliver: true, payee: "0x000000000000000000000000000000000000dead"})
	if _, err := f2.p.Decide(ctx, payIntent(payTestAmountMicros)); err != nil {
		t.Fatalf("Decide (re-cased payee): %v", err)
	}
	none(t, f2.st, kindPayeeDivergence)
}

// A 200 whose state is neither committed nor failed (RECONCILING, which nothing in the
// sidecar ever advances) is the one outcome machinery must NOT resolve: the money may have
// moved. So nothing is committed, nothing is released, no receipt is fabricated, and the
// hold stays for the owner to decide (V6 §2.5 path 3, H3's bearer-instrument rule).
func TestV6_UnresolvedPaymentKeepsTheHoldAndFabricatesNothing(t *testing.T) {
	ctx := context.Background()
	payer := &fakePayer{unresolvedState: h3.StateReconciling}
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "approved-pending-integration" || out.Code != "payment-unresolved" {
		t.Fatalf("an unresolved payment reported %+v; want approved-pending-integration/payment-unresolved", out)
	}
	if out.Receipt != nil || out.Data != nil {
		t.Fatal("an unresolved payment produced a receipt or data: nothing is proven bought yet")
	}
	none(t, f.st, budget.KindCommitted)
	none(t, f.st, budget.KindReleased)
	none(t, f.st, kindDelivered)
	none(t, f.st, kindPendingSettlement)

	unres := one(t, f.st, kindUnresolved)
	if unres.State != h3.StateReconciling || unres.PaymentID == "" {
		t.Fatalf("the unresolved record is %+v; want state %s and the precomputed payment id", unres, h3.StateReconciling)
	}
	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != payTestAmountMicros {
		t.Fatalf("job committed spend is %d micros while the payment's outcome is unknown, want the %d still held",
			got, payTestAmountMicros)
	}
}

// ── §5.3: the sweeper and the fold ───────────────────────────────────────────

// FR-PAY-007's structural half. A hold whose expiry passed with NO execution claim is
// provably dead (Execute refuses expired intents unconditionally), so Sweep reclaims it
// with no external check. A hold WITH a claim may already be signed, so Sweep must never
// touch it, however old it is.
func TestV6_ExpiredUnattemptedHoldIsSwept(t *testing.T) {
	ctx := context.Background()
	st, fc, led := newLedgerFixture(t)

	expiry := fc.Now().Add(time.Minute)
	unattempted := budget.Hold{
		RequestID: "apr_unattempted", IntentID: "pi_1", JobID: payTestJobID, OrgID: "org_demo",
		ReserveMicros: payTestAmountMicros, PaymentID: "pay_unattempted", Nonce: "0x01",
		Merchant: policy.DemoMerchantProfile, PayTo: payTestPayTo, ExpiresAt: expiry,
	}
	claimed := budget.Hold{
		RequestID: "apr_claimed", IntentID: "pi_2", JobID: payTestJobID, OrgID: "org_demo",
		ReserveMicros: 60_000, PaymentID: "pay_claimed", Nonce: "0x02",
		Merchant: policy.DemoMerchantProfile, PayTo: payTestPayTo, ExpiresAt: expiry,
	}
	for _, h := range []budget.Hold{unattempted, claimed} {
		if err := led.Reserve(ctx, h); err != nil {
			t.Fatalf("Reserve(%s): %v", h.RequestID, err)
		}
	}
	// The lifecycle's write-ahead claim for the second hold: the ONLY evidence that a hold
	// may already be signed, and the reason Sweep leaves it alone.
	if _, err := st.Append(ctx, store.Event{
		TS: fc.Now(), Kind: "payment.executing", EntityID: payTestJobID, Actor: "approval",
		Payload: map[string]any{"request_id": claimed.RequestID, "intent_hash": "0xdeadbeef"},
	}); err != nil {
		t.Fatalf("appending the execution claim: %v", err)
	}

	fc.Advance(2 * time.Minute) // past both expiries

	n, err := led.Sweep(ctx, fc.Now())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("Sweep released %d holds, want exactly 1 (the unattempted one)", n)
	}
	if got := led.Spend(payTestJobID).JobCommittedMicros; got != claimed.ReserveMicros {
		t.Fatalf("job committed spend is %d micros after the sweep, want the claimed hold's %d still standing",
			got, claimed.ReserveMicros)
	}
	rel := one(t, st, budget.KindReleased)
	if rel.RequestID != unattempted.RequestID {
		t.Fatalf("the sweep released %q, want the unattempted %q: a claimed hold may already be a signed bearer instrument",
			rel.RequestID, unattempted.RequestID)
	}
	if rel.Code != budget.CodeExpiredUnattempted || !rel.PreSign {
		t.Fatalf("the sweep release is %+v; want code %s with pre_sign true", rel, budget.CodeExpiredUnattempted)
	}
}

// The hold is a FOLDED FACT, not memory: discard the ledger, build a new one over the same
// store, Recover, and the spend figures are identical.
func TestV6_LedgerSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	st, fc, led := newLedgerFixture(t)

	if err := led.Reserve(ctx, budget.Hold{
		RequestID: "apr_restart", IntentID: "pi_1", JobID: payTestJobID, OrgID: "org_demo",
		ReserveMicros: payTestAmountMicros, PaymentID: "pay_restart", Nonce: "0x01",
		Merchant: policy.DemoMerchantProfile, PayTo: payTestPayTo, ExpiresAt: fc.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	before := led.Spend(payTestJobID)
	if before.JobCommittedMicros != payTestAmountMicros {
		t.Fatalf("the live ledger reports %d micros held, want %d", before.JobCommittedMicros, payTestAmountMicros)
	}

	fresh := budget.New(st, fc.Now)
	if err := fresh.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	after := fresh.Spend(payTestJobID)
	if after.JobCommittedMicros != before.JobCommittedMicros || after.DailySpentMicros != before.DailySpentMicros {
		t.Fatalf("after a restart the ledger reports %+v, want the folded %+v: the hold is a durable fact", after, before)
	}
}

// ── §6 step 10: the honest stop, which four other readers depend on ──────────

// With NO payer wired, an approved purchase must still run every gate, still append
// purchase.pending_settlement with the IDENTICAL payload keys, and still report
// "approved-pending-integration" with no data and no receipt.
//
// This is the regression that stops the V6 rewire from quietly deleting the degraded mode:
// integration/g8_adaptive_test.go reads amount_micros out of these events to pin the demo's
// purchase sequence, billing keys its expense gap on the kind, and the dashboard activity
// feed renders it.
func TestV6_NoPayerStopsAtPendingSettlement(t *testing.T) {
	ctx := context.Background()
	f := newPayFixture(t, nil) // SetPayer(nil, nil, 0)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Decision != string(policy.AutoApprove) || out.Status != "approved-pending-integration" {
		t.Fatalf("the honest stop reported %+v; want AUTO_APPROVE/approved-pending-integration", out)
	}
	if out.Data != nil || out.Receipt != nil {
		t.Fatal("no data or receipt may be fabricated for a purchase whose money never moved")
	}
	none(t, f.st, kindDelivered)

	// The payload keys are the contract, so assert the SET and not merely the values.
	raw := rawPayloadsOf(t, f.st, kindPendingSettlement)
	if len(raw) != 1 {
		t.Fatalf("want exactly 1 %s event, got %d", kindPendingSettlement, len(raw))
	}
	keys := map[string]any{}
	if err := json.Unmarshal([]byte(raw[0]), &keys); err != nil {
		t.Fatalf("decoding the pending-settlement payload: %v", err)
	}
	for _, want := range []string{"job_id", "merchant", "amount_micros", "note"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("the pending-settlement payload lost key %q, which downstream readers join on", want)
		}
	}
	if len(keys) != 4 {
		t.Errorf("the pending-settlement payload has %d keys (%v), want exactly the four frozen ones", len(keys), keys)
	}
	p := payloadsOf(t, f.st, kindPendingSettlement)[0]
	if p.JobID != payTestJobID || p.Merchant != policy.DemoMerchantProfile || p.AmountMicros != payTestAmountMicros {
		t.Fatalf("the pending-settlement payload is %+v; want the job, the allowlisted HOST and the approved figure", p)
	}
	if p.Note != "approved; money movement pending F2 sidecar client" {
		t.Errorf("the pending-settlement note changed to %q", p.Note)
	}
	// The unwired lane is PROOF nothing was signed, so the hold does not stay pinned to a
	// payment that can never happen.
	if got := f.led.Spend(payTestJobID).JobCommittedMicros; got != 0 {
		t.Errorf("the honest stop left %d micros held against the job; nothing was sent, so nothing may stay held", got)
	}
}

// A quote above the amount the worker asked for is refused BEFORE intake: no approval, no
// hold, no owner prompt, and above all no silent overpay. The worker's ceiling is real (its
// AT-04 adaptation re-queries strictly below a rejected price).
func TestV6_QuotedPriceAboveTheRequestedCapIsRefusedBeforeIntake(t *testing.T) {
	ctx := context.Background()
	payer := &fakePayer{quoteAmount: "60000"} // the seller wants 0.06 for a 0.04 request
	f := newPayFixture(t, payer)

	out, err := f.p.Decide(ctx, payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "price-above-requested-cap" {
		t.Fatalf("an over-cap quote reported %+v; want denied/price-above-requested-cap", out)
	}
	none(t, f.st, "approval.requested")
	none(t, f.st, budget.KindReserved)
	if pays, quotes, _ := payer.counts(); pays != 0 || quotes != 1 {
		t.Fatalf("the payer saw %d pay(s) and %d quote(s); want one quote and no payment", pays, quotes)
	}
}
