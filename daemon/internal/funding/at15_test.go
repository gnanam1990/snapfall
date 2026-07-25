// AT-15's caller-authorization half (docs/WORK-SPLIT.md:31), one layer past
// boundary_test.go.
//
// boundary_test.go proves that nothing is RECORDED without an approval-minted Grant.
// These prove that nothing is SENT: no wire intent derived, no authorization minted, no
// round trip taken. The distinction is not academic. A refusal that happened AFTER the
// HTTP call would look identical from the caller's side and would already have spent the
// money, so the assertion that matters is the fake payer's call COUNT.
//
// The second half is confinement: the two sidecar secrets are capabilities (one spends and
// re-reads every already-paid resource, one mints the authorization the sidecar trusts), so
// a source scan pins which packages may even name them, and the import graph pins that a
// worker cannot reach the client that uses them.
package funding_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// The sidecar's own test vectors (sidecar/src/service-test.ts:113-156), reused verbatim so
// a Go fake and the real process are exercised with the same secret, chain id and payee.
const (
	testApprovalSecret = "test-h2-approval-secret-0123456789"
	testChainID        = int64(5042002)
	testNetwork        = "eip155:5042002"
	// The seller's defaults. testPayTo is deliberately MIXED case: it is copied verbatim
	// because the sidecar's equality check is case-insensitive while the intent hash is not.
	testPayTo       = "0x000000000000000000000000000000000000dEaD"
	testAsset       = "0x0000000000000000000000000000000000000001"
	testResourceURL = "http://127.0.0.1:4021/v1/company-profile"
	// The $0.04 profile beat: 40000 atomic USDC is 40_000 micros, the same number.
	testAmountAtomic = "40000"
	testAmountMicros = int64(40_000)
)

// recordingPayer is a fake Payer that records every call and reaches no network.
//
// The recording IS the test instrument: these tests assert on how many pay calls happened,
// not merely on what the Agent returned.
type recordingPayer struct {
	mu           sync.Mutex
	pays         []payCall
	quotes       int
	quoteChainID int64
	statuses     int
	result       h3.PayResult // returned by Pay when err is nil
	err          error        // returned by Pay instead of result
}

// payCall is one observed /v1/pay body: exactly what the daemon would have put on the wire.
type payCall struct {
	Intent h3.WireIntent
	Token  h3.ApprovalToken
}

// The fake must satisfy the real lane contract, or these tests prove nothing about it.
var _ funding.Payer = (*recordingPayer)(nil)

func (p *recordingPayer) Quote(_ context.Context, resource string, chainID int64) (h3.Quote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotes++
	p.quoteChainID = chainID
	return h3.Quote{
		Resource: resource,
		Network:  testNetwork,
		Accept: h3.AcceptOption{
			Scheme: "exact", Network: testNetwork, Amount: testAmountAtomic,
			Asset: testAsset, PayTo: testPayTo, MaxTimeoutSeconds: 60,
		},
		Price: testAmountAtomic, PriceDisplay: "0.040000",
	}, nil
}

func (p *recordingPayer) Pay(_ context.Context, in h3.WireIntent, tok h3.ApprovalToken) (h3.PayResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pays = append(p.pays, payCall{Intent: in, Token: tok})
	if p.err != nil {
		return h3.PayResult{}, p.err
	}
	return p.result, nil
}

func (p *recordingPayer) Status(_ context.Context, _ string) (h3.StatusResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses++
	return h3.StatusResult{}, nil
}

// counts reports how many times each lane call was made.
func (p *recordingPayer) counts() (pays, quotes, statuses int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pays), p.quotes, p.statuses
}

// lastPay returns the most recent observed pay body, failing the test when none exists.
func (p *recordingPayer) lastPay(t *testing.T) payCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pays) == 0 {
		t.Fatal("the payer received no pay call at all")
	}
	return p.pays[len(p.pays)-1]
}

// mintPayableGrant is mintRealGrant (boundary_test.go:22) for a PAYABLE intent: the same
// real lifecycle drive, plus the H3 wire fields a quote would have supplied (H3 §7 step 1)
// and the equal reserve ceiling the payment door requires. There is still no shortcut: a
// Grant can only come out of an approved lifecycle flow.
func mintPayableGrant(t *testing.T) approval.Grant {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "payable.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	clock := func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) }
	l := approval.New(st, clock)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }

	// $0.04 at an allowlisted merchant, below the 0.10 auto-approve threshold.
	in := approval.Intent{
		IntentID: "pi_payable",
		OrgID:    "org_demo",
		JobID:    "job_x",
		TaskID:   "task_1",
		AgentID:  "due-diligence",
		// Merchant/Resource stay POLICY-shaped: a host the allowlist matches by exact
		// string, and a display resource. The wire values live in PayTo/ResourceURL.
		Merchant:        policy.DemoMerchantProfile,
		Resource:        "GET /v1/company-profile",
		AmountMicros:    testAmountMicros,
		MaxAmountMicros: testAmountMicros, // the hash-bound ceiling equals the evaluated amount
		Purpose:         "company profile",
		Nonce:           "0x" + strings.Repeat("ef", 32),
		ExpiresAt:       clock().Add(5 * time.Minute),
		PayTo:           testPayTo,
		ResourceURL:     testResourceURL,
		Asset:           testAsset,
		Network:         testNetwork,
	}
	res, err := l.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Decision.Outcome != policy.AutoApprove {
		t.Fatalf("intent did not auto-approve (outcome %s), so no grant can be minted", res.Decision.Outcome)
	}
	var g approval.Grant
	// Execute against the BOUND intent (Submit stamped PolicyVersion on it) or the AT-05
	// hash check refuses it.
	if err := l.Execute(ctx, res.Request.Intent, res.Request.ID, func(_ context.Context, grant approval.Grant) error {
		g = grant
		return nil
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if g.Empty() {
		t.Fatal("captured grant is empty, so the lifecycle did not mint it")
	}
	return g
}

// deliveredResult is the sidecar's 200 for a completed payment on this grant, with every
// derived value computed the way the sidecar computes it.
func deliveredResult(g approval.Grant) h3.PayResult {
	in := g.Intent()
	hash := approval.WireHash(in)
	return h3.PayResult{
		PaymentID:  h3.PaymentID(hash, in.Nonce),
		State:      h3.StateDelivered,
		AmountPaid: testAmountAtomic,
		IntentHash: hash,
		Receipt: &h3.Receipt{
			Resource: in.ResourceURL, Amount: testAmountAtomic, Asset: in.Asset,
			Network: in.Network, Payer: "0x00000000000000000000000000000000000000a1",
			// The seller echoes the payee with ITS own casing; here, the same mixed-case
			// address it advertised.
			Payee: testPayTo, AuthNonce: h3.AuthNonce(hash), Settlement: "NOT_BROADCAST",
		},
		Data: []byte(`{"ok":true}`),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The WORK-SPLIT reading of AT-15: a non-Brain caller is rejected
// ─────────────────────────────────────────────────────────────────────────────

// A forged (zero-value) Grant is refused BEFORE anything reaches the payer.
// TestFunding_RefusesForgedEmptyGrant proves nothing is recorded; this proves nothing is
// sent, so a future refactor cannot move the refusal to after the HTTP call.
func TestAT15_ForgedGrantNeverReachesThePayer(t *testing.T) {
	agent := funding.New()
	payer := &recordingPayer{result: h3.PayResult{State: h3.StateDelivered, AmountPaid: testAmountAtomic}}
	agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)

	out, err := agent.ExecutePayment(context.Background(), approval.Grant{})
	if err == nil {
		t.Fatal("an empty forged grant was accepted by the payment door")
	}
	if out.Submitted {
		t.Errorf("a refused payment reported Submitted=true: %+v", out)
	}
	pays, _, statuses := payer.counts()
	if pays != 0 {
		t.Fatalf("the forged grant produced %d pay call(s): the refusal must happen before any wire value is derived or sent", pays)
	}
	if statuses != 0 {
		t.Errorf("the forged grant produced %d status call(s); it must touch the sidecar not at all", statuses)
	}
	if got := len(agent.Executed()); got != 0 {
		t.Fatalf("the refused grant was recorded: %d instruction(s)", got)
	}
}

// A caller replaying the SAME legitimate Grant pays exactly ONCE. The sidecar's durable
// nonce idempotency would also catch this, and that is precisely why the test exists: the
// daemon must not outsource exactly-once to the process it is paying through.
func TestAT15_ReplayedGrantPaysOnce(t *testing.T) {
	ctx := context.Background()
	g := mintPayableGrant(t)
	agent := funding.New()
	payer := &recordingPayer{result: deliveredResult(g)}
	agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)

	out, err := agent.ExecutePayment(ctx, g)
	if err != nil {
		t.Fatalf("first payment: %v", err)
	}
	if !out.Submitted || out.State != h3.StateDelivered || out.PaidMicros != testAmountMicros {
		t.Fatalf("first payment outcome = %+v, want a submitted DELIVERED payment at %d micros", out, testAmountMicros)
	}
	if out.PaymentID != h3.PaymentID(approval.WireHash(g.Intent()), g.Intent().Nonce) {
		t.Errorf("PaymentID %q is not the deterministic id derived from the grant", out.PaymentID)
	}
	if out.ReceiptHash == "" || out.ReceiptHash != out.AuthNonce {
		t.Errorf("ReceiptHash %q must be the AuthNonce %q: H3 emits no receiptHash and this is Billing's join key",
			out.ReceiptHash, out.AuthNonce)
	}
	if out.PayeeDiverged {
		t.Errorf("payee %q was read as diverging from the approved payee, but it differs only in casing", out.Payee)
	}
	if out.Code != "" || out.PreSign {
		t.Errorf("a successful payment carried a failure classification: code %q, pre-sign %v", out.Code, out.PreSign)
	}

	if _, err := agent.ExecutePayment(ctx, g); err == nil {
		t.Fatal("replaying the same grant was accepted a second time")
	}
	if pays, _, _ := payer.counts(); pays != 1 {
		t.Fatalf("the payer saw %d pay calls for one grant, want exactly 1", pays)
	}
}

// Every value on the wire is READ FROM the grant. This is the property the whole boundary
// rests on: if any of these could come from somewhere else, the door would accept a payee,
// a price or a resource the owner never approved.
func TestAT15_EveryPaidValueIsReadFromTheGrant(t *testing.T) {
	g := mintPayableGrant(t)
	in := g.Intent()
	agent := funding.New()
	payer := &recordingPayer{result: deliveredResult(g)}
	agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)
	if _, err := agent.ExecutePayment(context.Background(), g); err != nil {
		t.Fatalf("payment: %v", err)
	}
	call := payer.lastPay(t)

	// The two conventions cross exactly once: the wire carries the payee ADDRESS and the
	// absolute URL, never the policy-facing host or the display resource. A host in the
	// merchant slot is MERCHANT_CHANGED; a display string in the resource slot is
	// RESOURCE_NOT_FOUND.
	if call.Intent.Merchant != in.PayTo {
		t.Errorf("wire merchant = %q, want the approved payTo %q", call.Intent.Merchant, in.PayTo)
	}
	if call.Intent.Merchant == in.Merchant {
		t.Errorf("wire merchant carried the policy HOST %q; the sidecar compares it to accept.payTo", in.Merchant)
	}
	if call.Intent.Resource != in.ResourceURL {
		t.Errorf("wire resource = %q, want the approved absolute URL %q", call.Intent.Resource, in.ResourceURL)
	}
	if call.Intent.Asset != in.Asset || call.Intent.Network != in.Network {
		t.Errorf("wire asset/network = %q/%q, want the quote-bound %q/%q",
			call.Intent.Asset, call.Intent.Network, in.Asset, in.Network)
	}
	if call.Intent.Amount != testAmountAtomic || call.Intent.MaxAmount != testAmountAtomic {
		t.Errorf("wire amount/maxAmount = %q/%q, want %q for both: the ceiling is the evaluated amount",
			call.Intent.Amount, call.Intent.MaxAmount, testAmountAtomic)
	}
	if call.Intent.Nonce != in.Nonce || call.Intent.IntentID != in.IntentID || call.Intent.PolicyVersion != in.PolicyVersion {
		t.Errorf("wire identity drifted from the grant: %+v", call.Intent)
	}

	// SECOND precision, formatted exactly as approval.CanonicalWire formats it. A
	// fractional second is INTENT_HASH_MISMATCH on every single pay, which is the most
	// likely first-run failure of the whole lane.
	if strings.Contains(call.Intent.ExpiresAt, ".") {
		t.Errorf("wire expiresAt %q carries sub-second precision", call.Intent.ExpiresAt)
	}
	if want := in.ExpiresAt.UTC().Format(time.RFC3339); call.Intent.ExpiresAt != want {
		t.Errorf("wire expiresAt = %q, want %q (the value the daemon hashed)", call.Intent.ExpiresAt, want)
	}

	// The token binds the daemon's OWN wire hash over that intent, and the three equalities
	// the sidecar checks between token and intent.
	if want := approval.WireHash(in); call.Token.IntentHash != want {
		t.Errorf("approvalToken.intentHash = %q, want the daemon's wire hash %q", call.Token.IntentHash, want)
	}
	if call.Token.ApprovedAmount != call.Intent.Amount {
		t.Errorf("approvalToken.approvedAmount %q != intent.amount %q", call.Token.ApprovedAmount, call.Intent.Amount)
	}
	if call.Token.ExpiresAt != call.Intent.ExpiresAt {
		t.Errorf("approvalToken.expiresAt %q != intent.expiresAt %q", call.Token.ExpiresAt, call.Intent.ExpiresAt)
	}
	if call.Token.Decision != call.Intent.Decision {
		t.Errorf("approvalToken.decision %q != intent.decision %q", call.Token.Decision, call.Intent.Decision)
	}
	if call.Intent.Decision != h3.DecisionAutoApprove && call.Intent.Decision != h3.DecisionHumanApproved {
		t.Errorf("wire decision %q: the treasury signs only for AUTO_APPROVE or HUMAN_APPROVED", call.Intent.Decision)
	}

	// And the signature is the sidecar's own HMAC over those four fields, lowercase hex.
	want := call.Token
	h3.SignApproval([]byte(testApprovalSecret), &want)
	if call.Token.Signature == "" {
		t.Fatal("the approval token carried no signature")
	}
	if call.Token.Signature != want.Signature {
		t.Errorf("approvalToken.signature = %q, want %q (HMAC over intentHash|decision|approvedAmount|expiresAt)",
			call.Token.Signature, want.Signature)
	}
}

// The release rule travels with the failure, in both directions, because the two paths
// must never blur: a pre-sign failure releases the hold in full immediately, and a
// post-sign one leaves it standing until a status poll resolves it (a signed authorization
// is a bearer instrument that may still settle).
func TestAT15_FailureCarriesTheReleaseClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		status  int
		preSign bool
	}{
		// Raised inside the sidecar's pre-sign checks, strictly before its write-ahead
		// record exists: nothing was signed.
		{"merchant substituted pre-sign", h3.CodeMerchantChanged, 409, true},
		{"seller rejected the payment", h3.CodePaymentRejected, 402, false},
		{"facilitator failed after signing", h3.CodeFacilitatorError, 502, false},
		// Nothing says a signature happened, but a payment may be in flight against this
		// very reservation, so it is deliberately NOT released on the error alone.
		{"a payment is already in flight", h3.CodePaymentInProgress, 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := mintPayableGrant(t)
			agent := funding.New()
			payer := &recordingPayer{err: &h3.Error{HTTPStatus: tc.status, Code: tc.code, Message: "captured envelope"}}
			agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)

			out, err := agent.ExecutePayment(context.Background(), g)
			if err == nil {
				t.Fatalf("a %s response was reported as success", tc.code)
			}
			// The caller's release rule reads the ERROR, so it must survive as an *h3.Error.
			var he *h3.Error
			if !errors.As(err, &he) {
				t.Fatalf("the sidecar error did not survive as *h3.Error (%v), so the caller cannot classify it", err)
			}
			if he.PreSign() != tc.preSign || out.PreSign != tc.preSign {
				t.Errorf("%s: pre-sign is error=%v outcome=%v, want %v", tc.code, he.PreSign(), out.PreSign, tc.preSign)
			}
			if out.Code != tc.code {
				t.Errorf("outcome code = %q, want %q from the envelope (never from the HTTP status)", out.Code, tc.code)
			}
			if !out.Submitted || out.PaymentID == "" {
				t.Errorf("a failed attempt must still report that it was sent and under which payment id: %+v", out)
			}
			if out.PaidMicros != 0 || out.State != "" {
				t.Errorf("a failed attempt reported progress: %+v", out)
			}
		})
	}
}

// A grant that never went through a quote (no payee address, no absolute URL, no
// hash-bound ceiling) is refused HERE, before anything is sent, and the refusal is
// classified pre-sign so the caller releases the whole reservation. mintRealGrant is
// exactly that shape, which is why it is reused rather than hand-built.
func TestAT15_AnUnquotedGrantIsRefusedBeforeAnythingIsSent(t *testing.T) {
	g := mintRealGrant(t)
	agent := funding.New()
	payer := &recordingPayer{}
	agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)

	out, err := agent.ExecutePayment(context.Background(), g)
	if err == nil {
		t.Fatal("an intent with no quoted payee, resource URL or reserve ceiling was sent to the sidecar")
	}
	var he *h3.Error
	if !errors.As(err, &he) {
		t.Fatalf("a local refusal must still be classifiable as *h3.Error, got %v", err)
	}
	if !he.PreSign() {
		t.Errorf("a refusal raised before any request left the process must read as pre-sign: %v", he)
	}
	if he.HTTPStatus != 0 {
		t.Errorf("a local refusal carried HTTP status %d; 0 is what says nothing was sent", he.HTTPStatus)
	}
	if out.Submitted {
		t.Errorf("a locally refused payment claimed it was submitted: %+v", out)
	}
	if pays, _, _ := payer.counts(); pays != 0 {
		t.Fatalf("the underspecified payment reached the payer %d time(s)", pays)
	}
}

// The payment door's signature is pinned exactly like the advance door's: it takes the
// Grant, and it takes NOTHING else that could name a payee, an amount or a resource.
func TestAT15_PaymentDoorDemandsAGrantAndNothingElse(t *testing.T) {
	m, ok := reflect.TypeOf(&funding.Agent{}).MethodByName("ExecutePayment")
	if !ok {
		t.Fatal("ExecutePayment missing")
	}
	grantType := reflect.TypeOf(approval.Grant{})
	found := false
	for i := 1; i < m.Type.NumIn(); i++ {
		if m.Type.In(i) == grantType {
			found = true
		}
	}
	if !found {
		t.Fatal("ExecutePayment does not take approval.Grant: the payment door lost the Grant requirement")
	}
	// Receiver, context.Context, approval.Grant. Anything more is a caller-supplied term.
	if m.Type.NumIn() != 3 {
		t.Fatalf("ExecutePayment takes %d params: the contract is (ctx, approval.Grant), so no caller can supply a payee, "+
			"an amount or a resource alongside the grant", m.Type.NumIn())
	}
}

// The degraded mode the whole pipeline depends on: with no payer wired, an approved
// payment still consumes its approval exactly once, moves no money, and reports the honest
// stop (Submitted=false with a NIL error) so the caller records
// purchase.pending_settlement instead of fabricating a purchase.
func TestFunding_NoPayerIsTheHonestStop(t *testing.T) {
	g := mintPayableGrant(t)
	agent := funding.New()

	out, err := agent.ExecutePayment(context.Background(), g)
	if err != nil {
		t.Fatalf("an unwired payment lane must not be an error, it is the honest stop: %v", err)
	}
	if out.Submitted || out.State != "" || out.PaidMicros != 0 {
		t.Fatalf("nothing was wired, yet the outcome claims progress: %+v", out)
	}
	if got := len(agent.Executed()); got != 1 {
		t.Fatalf("the instruction record shows %d movements, want 1 (the approval is still consumed exactly once)", got)
	}
}

// Quote is the classified READ door: no Grant, no key, no money. It passes the WIRED chain
// id (the sidecar prices against eip155:${chainId}), and with no payer it answers with an
// empty challenge and no error, which is the caller's signal to keep the stub shape.
func TestAT15_QuoteIsAReadDoorOnTheWiredChain(t *testing.T) {
	ctx := context.Background()

	unwired := funding.New()
	q, err := unwired.Quote(ctx, testResourceURL)
	if err != nil {
		t.Fatalf("an unwired quote must not error, it must answer with no challenge: %v", err)
	}
	if q.Accept.PayTo != "" {
		t.Errorf("an unwired quote produced a payee %q; an empty payTo is the caller's only safe discriminator", q.Accept.PayTo)
	}

	agent := funding.New()
	payer := &recordingPayer{}
	agent.SetPayer(payer, []byte(testApprovalSecret), testChainID)
	q, err = agent.Quote(ctx, testResourceURL)
	if err != nil {
		t.Fatalf("wired quote: %v", err)
	}
	if q.Accept.PayTo != testPayTo {
		t.Errorf("quote payTo = %q, want the seller's %q verbatim (re-casing it would diverge the intent hash)", q.Accept.PayTo, testPayTo)
	}
	pays, quotes, _ := payer.counts()
	if quotes != 1 || pays != 0 {
		t.Fatalf("quote made %d quote call(s) and %d pay call(s); a read door must never pay", quotes, pays)
	}
	if payer.quoteChainID != testChainID {
		t.Errorf("quote used chain id %d, want the wired %d", payer.quoteChainID, testChainID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Confinement of the two sidecar capabilities
// ─────────────────────────────────────────────────────────────────────────────

// A SOURCE scan, not reflection, because the transitive import graph cannot express this:
// brain imports funding, so brain reaches internal/h3 no matter what we do. What can be
// pinned is which packages may NAME the two variables, and which may READ them from the
// environment. Precedent: brain/jobs_projection_test.go:74-101 scans source for the same
// reason.
func TestAT15_OnlyFundingAndWiringNameTheSidecarSecrets(t *testing.T) {
	secrets := []string{"SIDECAR_AUTH_TOKEN", "H2_APPROVAL_SECRET"}

	// Packages allowed to NAME them. internal/h3 is on the list deliberately: its package
	// doc explains what each secret is and one refusal message names the missing token, but
	// it HOLDS neither (both arrive as call arguments) and it never reads the environment,
	// which the second half of this test enforces.
	namers := []string{
		filepath.Join("cmd", "snapfall"),     // the wiring layer: the only reader
		filepath.Join("internal", "funding"), // the holder, and this test
		filepath.Join("internal", "h3"),      // documents both, holds neither
	}
	// Only the wiring layer may pull the VALUES out of the environment. A package that can
	// do that holds the capability regardless of what the docs claim.
	readers := []string{filepath.Join("cmd", "snapfall")}
	envAccess := []string{"Getenv", "LookupEnv", "Setenv"}

	root := filepath.Join("..", "..") // the daemon module root
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		dir := filepath.Dir(rel)
		body := string(src)
		for _, name := range secrets {
			if !strings.Contains(body, name) {
				continue
			}
			if !underAny(dir, namers) {
				offenders = append(offenders, fmt.Sprintf("%s names %s", rel, name))
			}
			if underAny(dir, readers) {
				continue
			}
			for i, line := range strings.Split(body, "\n") {
				if !strings.Contains(line, name) {
					continue
				}
				for _, access := range envAccess {
					if strings.Contains(line, access) {
						offenders = append(offenders, fmt.Sprintf("%s:%d reads %s from the environment", rel, i+1, name))
						break
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the daemon tree: %v", err)
	}
	for _, o := range offenders {
		t.Errorf("sidecar capability leaked: %s. The bearer token spends AND re-reads every already-paid resource, and the "+
			"approval secret mints the authorization the sidecar trusts: the wiring layer reads them, internal/funding holds "+
			"them, nothing else may name them (AT-15)", o)
	}
}

// underAny reports whether dir is one of the listed directories or sits inside one.
func underAny(dir string, allowed []string) bool {
	for _, a := range allowed {
		if dir == a || strings.HasPrefix(dir, a+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AT-16's law extended to the payment lane: a worker cannot even NAME the sidecar client,
// so no prompt injection can reach the bearer token through one.
func TestAT15_WorkerCannotReachTheSidecarClient(t *testing.T) {
	const (
		modulePrefix = "github.com/gnanam1990/snapfall/daemon/internal/"
		worker       = modulePrefix + "worker"
		client       = modulePrefix + "h3"
		holder       = modulePrefix + "funding"
	)
	if deps := listDeps(t, worker); contains(deps, client) {
		t.Fatal("internal/worker can reach internal/h3: a worker that can name the sidecar client is one prompt injection " +
			"away from the bearer token (AT-16's law, applied to the payment lane)")
	}
	// Sanity on the checker itself. funding DOES reach h3 (it holds the lane), so a broken
	// go list invocation would make the assertion above pass vacuously.
	if deps := listDeps(t, holder); !contains(deps, client) {
		t.Fatal("checker broken: internal/funding must reach internal/h3, but go list does not show it")
	}
}

// listDeps returns the full transitive dependency set of pkg (brain/at16_law_test.go:56's
// checker, reused). It interrogates the compiler's own graph rather than trusting a scan.
func listDeps(t *testing.T, pkg string) map[string]struct{} {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v (this law is checked by the Go toolchain; CI is its gate)", pkg, err)
	}
	deps := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[strings.TrimSpace(line)] = struct{}{}
	}
	return deps
}

func contains(deps map[string]struct{}, pkg string) bool {
	_, ok := deps[pkg]
	return ok
}
