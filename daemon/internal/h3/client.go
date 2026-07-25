package h3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one sidecar call. A pay probes the seller, signs an EIP-3009
// authorization and submits it, so the budget is generous; the caller's context is the
// real deadline and this only stops a hung socket from pinning a hold forever.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes caps a response read. The pay 200 carries the purchased resource
// payload, and the sidecar itself refuses request bodies over 1 MB (service.ts:419), so
// this is loose enough for real data and tight enough that a hostile seller's payload
// echoed through the sidecar cannot exhaust the daemon.
const maxResponseBytes = 8 << 20

// Client is the H3 sidecar lane: four calls, no state, no secrets of its own.
//
// It is safe for concurrent use. Nothing here serializes anything, deliberately: the
// sidecar owns per-nonce idempotency durably (service.ts:281-308) and the daemon owns
// exactly-once at the approval lifecycle, so a lock here would only hide which layer is
// actually responsible.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
	// cfgErr records a base URL this client can never reach. New returns no error (it is
	// wiring, called once at boot), so the refusal is carried and returned by every call
	// instead of being swallowed. Fail closed, with the reason attached.
	cfgErr *Error
}

// New builds the client.
//
// baseURL is the loopback sidecar origin, DefaultBaseURL when empty. token is
// SIDECAR_AUTH_TOKEN, sent as "Bearer "+token: one space, case-sensitive, compared
// constant-time against a >=32-byte secret (h3.ts:102-107, service.ts:51). It is never
// logged by this package. A nil hc receives a bounded default.
//
// A base URL that is not http(s), or whose host is not loopback, is not a panic and not a
// silent fallback: it is recorded and returned from every call, so the operator sees the
// wiring mistake at the first payment attempt with the URL in the message.
//
// The loopback requirement is the lane's own contract, not a preference. H3 §1.1: the
// sidecar "binds 127.0.0.1 (loopback only) ... MUST NOT bind 0.0.0.0 or any external
// interface" (service.ts:486). §1.2 chooses a bearer token OVER mTLS *because* the channel
// is loopback-only, and names mTLS "the documented upgrade path if the sidecar ever moves
// off-host (which the freeze forbids without a version bump)". §1.3 then makes the token's
// safety conditional on exactly that: "A leaked token is inert off-host." So an off-host
// origin does not merely fail to connect, it would put a spend capability on the wire
// unprotected on the first /v1 call. Refuse it here, before Health can report a lane, which
// is what keeps main.go's wirePayer from handing the token to a payer at all.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	// `orig` is what the operator actually set; `raw` is what we will call. The refusals name
	// orig, because trimming can change the string beyond recognition: "://" trims to ":", and a
	// message quoting ":" sends the reader looking for a colon they never typed.
	orig := strings.TrimSpace(baseURL)
	raw := strings.TrimRight(orig, "/")
	if raw == "" {
		raw, orig = DefaultBaseURL, DefaultBaseURL
	}
	c := &Client{baseURL: raw, token: token, hc: hc}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		c.cfgErr = refuse(CodeBadRequest, "sidecar base URL %q does not parse: %v", orig, err)
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		c.cfgErr = refuse(CodeBadRequest, "sidecar base URL %q must be http(s), got scheme %q", orig, parsed.Scheme)
	case parsed.Host == "":
		c.cfgErr = refuse(CodeBadRequest, "sidecar base URL %q has no host", orig)
	case !isLoopbackHost(parsed.Hostname()):
		c.cfgErr = refuse(CodeBadRequest,
			"sidecar base URL %q is off-host (host %q is not loopback): the H3 lane is loopback-only (§1.1) and its bearer "+
				"token is only inert off-host because of that (§1.3), so refusing to send SIDECAR_AUTH_TOKEN there. "+
				"Moving the sidecar off-host needs mTLS and an x-h3-version bump (§1.2); point SIDECAR_BASE_URL at a "+
				"127.0.0.0/8, ::1 or localhost origin, or unset it for the %s default",
			orig, parsed.Hostname(), DefaultBaseURL)
	}
	return c
}

// isLoopbackHost reports whether host (a URL hostname, so no port and no brackets) names
// this machine's loopback interface.
//
// Three spellings, all of them things an operator legitimately writes: a literal in
// 127.0.0.0/8, the IPv6 "::1", and "localhost". The name is matched textually rather than
// resolved: a DNS lookup at construction time would make the refusal depend on a resolver
// (and on /etc/hosts), and "localhost" resolving to something non-loopback is a
// host-configuration fault we cannot fix by sending the token anyway. Everything else,
// including 0.0.0.0 and any routable address, is off-host for this purpose.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// net.ParseIP handles both families; IsLoopback covers all of 127.0.0.0/8 and ::1,
	// including the IPv4-mapped "::ffff:127.0.0.1" form.
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BaseURL is the origin this client calls. Exposed for boot logging: it carries no
// secret, unlike the bearer token, which this package never returns or logs.
func (c *Client) BaseURL() string { return c.baseURL }

// Health checks GET /health, the one route that needs NO bearer token
// (service.ts:450-452, checked before the /v1/ auth branch). Boot wiring uses it to
// decide whether a payment lane exists at all: no healthy sidecar means purchases stop
// honestly at the pending-settlement event instead of failing mid-flight.
func (c *Client) Health(ctx context.Context) error {
	var out struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, &out, false); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("H3 /health returned 200 with ok=false (service %q); treating the sidecar as unavailable", out.Service)
	}
	return nil
}

type quoteRequest struct {
	Resource string `json:"resource"`
	ChainID  int64  `json:"chainId"`
}

// Quote prices a resource: POST /v1/quote (H3 §2.1). Read-only, no key touched, no
// reservation, no side effect. It must run BEFORE intake, because the quoted price
// becomes the evaluated amount and accept.payTo / accept.asset / accept.network are
// hash-bound into the intent (H3 §7 step 1).
//
// chainID is the numeric chain id; the sidecar requires a JSON number and derives
// eip155:${chainId} from it (service.ts:221, buyer.ts:179).
//
// A 200 whose accept is missing its payee, asset, network or amount is refused here as
// CHALLENGE_UNAVAILABLE. Copying an empty payTo into an intent would produce
// MERCHANT_CHANGED at pay time, which reads like a substituting seller instead of an
// unusable challenge.
func (c *Client) Quote(ctx context.Context, resource string, chainID int64) (Quote, error) {
	if parsed, err := url.Parse(resource); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Quote{}, refuse(CodeBadRequest,
			"quote resource %q must be an absolute http(s) URL; the sidecar HTTP-probes it (buyer.ts:154-162)", resource)
	}
	var out Quote
	if err := c.do(ctx, http.MethodPost, "/v1/quote", quoteRequest{Resource: resource, ChainID: chainID}, &out, true); err != nil {
		return Quote{}, err
	}
	for _, f := range []struct{ name, value string }{
		{"accept.amount", out.Accept.Amount},
		{"accept.asset", out.Accept.Asset},
		{"accept.payTo", out.Accept.PayTo},
		{"accept.network", out.Accept.Network},
	} {
		if strings.TrimSpace(f.value) == "" {
			return Quote{}, &Error{Code: CodeChallengeUnavailable, Retriable: true, Message: fmt.Sprintf(
				"quote for %s returned 200 with an empty %s; the challenge is unusable, so no intent can be built from it",
				resource, f.name)}
		}
	}
	if !isCanonicalAtomic(out.Accept.Amount) {
		return Quote{}, &Error{Code: CodeChallengeUnavailable, Retriable: true, Message: fmt.Sprintf(
			"quote for %s priced at %q, which is not a canonical atomic USDC integer", resource, out.Accept.Amount)}
	}
	return out, nil
}

type payRequest struct {
	Intent        WireIntent    `json:"intent"`
	ApprovalToken ApprovalToken `json:"approvalToken"`
}

// Pay executes an owner-approved intent: POST /v1/pay (H3 §2.2). Idempotent by
// intent.Nonce, durably, on the sidecar side.
//
// The caller MUST classify the error, not the outcome: on a non-nil error, use
// errors.As to reach *Error and read PreSign. A PreSign error means nothing was signed
// and the reservation is released in full. Anything else means the hold STANDS until
// Status resolves it, and a transport error or an unparseable body is deliberately NOT
// an *Error at all, precisely so it cannot be mistaken for a safe release: the request
// may have reached the sidecar and signed.
func (c *Client) Pay(ctx context.Context, in WireIntent, tok ApprovalToken) (PayResult, error) {
	if bad := validatePayable(in, tok); bad != nil {
		return PayResult{}, bad
	}
	var out PayResult
	if err := c.do(ctx, http.MethodPost, "/v1/pay", payRequest{Intent: in, ApprovalToken: tok}, &out, true); err != nil {
		return PayResult{}, err
	}
	if out.State == "" {
		// A 200 with no state is an outcome we cannot classify. Not an *Error, so the
		// caller keeps its hold and reconciles through Status.
		return PayResult{}, fmt.Errorf("H3 pay for intent %s returned 200 without a state; "+
			"the outcome is unknown, reconcile via status on the precomputed paymentId", in.IntentID)
	}
	return out, nil
}

// Status reads a payment's state: GET /v1/status/{paymentId} (H3 §2.3). Safe to poll.
//
// Pass the caller's OWN precomputed PaymentID(intentHash, nonce), never an id read out
// of an error envelope: the in-flight-lock branch returns paymentId:null
// (service.ts:302-306). A 404 whose Code is PAYMENT_NOT_FOUND means the sidecar holds no
// durable record, so nothing was ever signed and the hold is safe to release. A 404
// whose Code is BAD_REQUEST is route-not-found (service.ts:478) and means nothing of the
// kind, which is why classification is on Code and never on HTTP status.
func (c *Client) Status(ctx context.Context, paymentID string) (StatusResult, error) {
	if strings.TrimSpace(paymentID) == "" {
		return StatusResult{}, refuse(CodeBadRequest, "status requires a paymentId; pass the precomputed PaymentID(intentHash, nonce)")
	}
	var out StatusResult
	if err := c.do(ctx, http.MethodGet, "/v1/status/"+url.PathEscape(paymentID), nil, &out, true); err != nil {
		return StatusResult{}, err
	}
	if out.State == "" {
		return StatusResult{}, fmt.Errorf("H3 status for %s returned 200 without a state; refusing to classify a payment "+
			"whose state the sidecar did not report", paymentID)
	}
	return out, nil
}

// do performs one request and decodes a 200 body into out (nil to discard).
//
// The error taxonomy is the load-bearing part, because the caller's release rule reads
// it:
//   - a decodable non-2xx envelope becomes *Error, and PreSign classifies it;
//   - a refusal raised before the request leaves the process becomes *Error with
//     HTTPStatus 0 and a pre-sign code, because nothing was sent;
//   - a transport failure, a short read, or a body that will not parse becomes a PLAIN
//     error, never *Error. The outcome is unknown there: the sidecar may have received
//     the request and signed, so the caller must keep its hold rather than release it.
func (c *Client) do(ctx context.Context, method, path string, body, out any, auth bool) error {
	if c.cfgErr != nil {
		return c.cfgErr
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return refuse(CodeBadRequest, "encoding the H3 %s %s body: %v", method, path, err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return refuse(CodeBadRequest, "building the H3 %s %s request: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json; charset=utf-8")
	}
	req.Header.Set("accept", "application/json")
	if auth {
		if c.token == "" {
			return refuse(CodeUnauthenticated,
				"no SIDECAR_AUTH_TOKEN configured: refusing to call %s unauthenticated (every /v1 route requires the bearer token)", path)
		}
		req.Header.Set("authorization", "Bearer "+c.token)
	}

	res, err := c.hc.Do(req)
	if err != nil {
		// Plain error on purpose: see the taxonomy above. The request may have arrived.
		return fmt.Errorf("H3 %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading the H3 %s %s response (HTTP %d): %w", method, path, res.StatusCode, err)
	}
	if res.StatusCode != http.StatusOK {
		return decodeEnvelope(res.StatusCode, raw, method, path)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding the H3 %s %s 200 body: %w (body: %s)", method, path, err, snippet(raw))
	}
	return nil
}

// decodeEnvelope turns a non-2xx response into *Error.
//
// paymentId is read out of the envelope by NOBODY, here or anywhere else: the in-flight
// branch may carry null (service.ts:302-306), so the field is not even modelled.
//
// A body that does not parse, parses with no code, or carries INTERNAL, yields a PLAIN
// error rather than an *Error. That matters: PreSign treats an unknown code as pre-sign
// (correct for the frozen enum, where only two codes are post-sign), and an
// unclassifiable response must not inherit that reading. Returning a non-*Error makes
// the caller's errors.As fail, which keeps the hold.
func decodeEnvelope(status int, raw []byte, method, path string) error {
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retriable bool   `json:"retriable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error.Code == "" {
		return fmt.Errorf("H3 %s %s returned HTTP %d with no usable error envelope (body: %s); "+
			"the outcome is unclassifiable, so this is deliberately not an *h3.Error and must not be read as pre-sign",
			method, path, status, snippet(raw))
	}
	// INTERNAL is the sidecar admitting it does not know what happened, and it is the ONE
	// code whose position relative to the write-ahead upsert at service.ts:372 is not
	// provable. mapBuyerError's fallback (service.ts:133) raises it for anything thrown that
	// is neither a PolicyViolation nor a PaymentFailed, and that fallback is reached from
	// BOTH call sites: :352, pre-sign with "still no record persisted", and :391, post-sign,
	// after the record was written SIGNED and then upserted FAILED or RECONCILING. The
	// top-level handler catch (:482) can emit it too.
	//
	// So it is neither pre-sign nor a member of postSign: it is UNCLASSIFIABLE, exactly like
	// the unparseable body above, and it gets the same PLAIN error. The caller's errors.As
	// fails, PreSign is never consulted, the hold STANDS, and a status poll on the
	// precomputed paymentId settles it (PAYMENT_NOT_FOUND for the :352 case, FAILED or
	// RECONCILING for the :391 case). Review finding: reading INTERNAL as pre-sign would
	// release a reservation that may already back a signed authorization, which is the
	// bearer-instrument double-spend H3 §7 step 4 forbids.
	if env.Error.Code == CodeInternal {
		return fmt.Errorf("H3 %s %s returned HTTP %d %s: %s; the sidecar could not classify its own failure and it can be "+
			"raised either side of the durable write, so this is deliberately not an *h3.Error: the hold stands until a "+
			"status poll on the precomputed paymentId resolves it",
			method, path, status, env.Error.Code, snippet([]byte(env.Error.Message)))
	}
	return &Error{
		HTTPStatus: status,
		Code:       env.Error.Code,
		Message:    env.Error.Message,
		Retriable:  env.Error.Retriable,
	}
}

// snippet bounds an untrusted body before it reaches a log line.
func snippet(raw []byte) string {
	const max = 240
	s := strings.TrimSpace(string(raw))
	if len(s) > max {
		return s[:max] + "..."
	}
	if s == "" {
		return "<empty>"
	}
	return s
}
