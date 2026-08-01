// Package discovery is G10: matching a worker's fuzzy need against a service catalog.
//
// HONEST LABEL: the matcher is a DETERMINISTIC LEXICAL MATCHER, TF-IDF cosine
// similarity over the catalog descriptions. It is an "embedding match" in the
// mechanical sense only (needs and descriptions become vectors, similarity ranks
// them); it is NOT a learned model and must never be presented as one. The Matcher is
// the seam where a real embedding model, or the Circle Agent Marketplace's own
// search, would slot in.
//
// THE CATALOG IS A STAND-IN (PRD §6.7, qualified): no Agent Marketplace API exists in
// this repo. V2StandIn() is a committed Go snapshot of our own seller's table
// (sidecar/src/seller.ts, V2's three paid endpoints, descriptions and prices
// verbatim). A Go snapshot of a TypeScript table is a drift risk with no
// cross-language tripwire; GET /v1/catalog is proposed to Vasanth as an H3 addition,
// and if it lands this snapshot retires in favor of an HTTP Catalog client.
//
// SUGGEST, NEVER AUTHORIZE (FR-DSC-001), structurally: this package imports no money
// package and no worker package, a Match cannot name a PaymentIntent, a Grant, or a
// Lifecycle (boundary_test.go). It becomes money only by being copied into a
// PurchaseRequest and submitted through the same Purchase seam as every other intent,
// job-stamped by Brain and gated by the deterministic policy engine, regardless of how
// the service was found.
//
// SCRIPTED-MODE DETERMINISM (design pin 3): Find is a pure function, no randomness,
// no clock, no network; the catalog and DemoNeed are committed constants, except that
// V2StandIn's resource ORIGIN is overridable by env to another LOOPBACK origin
// (SellerBaseURL) so the daemon can point
// at whatever port the demo seller actually bound, matching reads only the need and the
// Description, so that override cannot move a score or a selection; ordering is
// total and explicit (score desc, then cheaper first, then lexicographic resource);
// and the demo selections are pinned by tests WITH MARGINS, so drift fails a test
// instead of flipping a take. The demo need text is part of the script, chosen so the
// honest lexical match selects the beats we film, and said so plainly.
package discovery

import (
	"context"
	"math"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"unicode"
)

// The committed demo-script needs, IN SCRIPT ORDER: profile first (the $0.04
// auto-approve beat, 0:45), market second (the $4.00 escalation and $0.06 adaptation,
// 1:10). Fuzzy language only, no merchant and no resource name. Selections are
// margin-pinned in discovery_test.go; order is the wiring's job (a slice, never a map).
const (
	DemoNeedProfile = "company profile and background of the vendor"
	DemoNeedMarket  = "vendor risk market data: competitive landscape and industry benchmark summary"
)

// Service is one catalog entry, everything a PurchaseRequest needs, and no more:
// no job, no approval state, no authority.
type Service struct {
	// Merchant is the seller HOST, not an address. It is what the deterministic policy
	// engine matches: policy.Evaluate compares it verbatim against the merchant
	// allowlist (policy.go:317, exact string), and the demo allowlist is spelled with
	// hosts (policy/demo.go:13-19). FR-PAY-003 calls for a "merchant/domain allowlist"
	// and a domain is the better security primitive, because an x402 payTo address
	// rotates while the host does not. The x402 payee ADDRESS is a different value,
	// resolved later from the seller's own 402 challenge (accept.payTo) and carried on
	// the H3 wire as Intent.PayTo, never sourced from this catalog.
	Merchant string
	// Resource is an ABSOLUTE http(s) URL, because the sidecar HTTP-probes it: a
	// non-URL yields 404 RESOURCE_NOT_FOUND ("resource is not a valid URL") on
	// /v1/pay, and the H3 golden vector already pins the absolute form
	// (H3-sidecar-api.md:73, approval/hash_test.go:18). It was a display string
	// ("GET /v1/company-profile") for as long as nothing probed it; V6 made it real.
	Resource     string
	Description  string
	AmountMicros int64
}

// SellerBaseURLEnv names the environment variable that overrides the demo seller's
// origin in V2StandIn. Set it when the paid demo API is not on its default loopback
// port (sidecar/src/seller.ts:31 reads PAID_API_PORT, default 4021); the two must
// agree or every purchase fails pre-sign with RESOURCE_NOT_FOUND.
//
// The override is LOOPBACK-ONLY, and that bound is a security property rather than a
// convenience: see the WHY LOOPBACK-ONLY note on SellerBaseURL.
const SellerBaseURLEnv = "SNAPFALL_SELLER_BASE_URL"

// DefaultSellerBaseURL is the loopback origin sidecar/src/seller.ts binds by default.
const DefaultSellerBaseURL = "http://127.0.0.1:4021"

// SellerBaseURL is the origin V2StandIn's resource URLs are built on: SellerBaseURLEnv
// when it names a bare LOOPBACK http(s) origin, else DefaultSellerBaseURL. A trailing
// slash is trimmed so joining can never emit a double slash, the sidecar hashes the
// resource string byte-for-byte into intentHash, so "…:4021//v1/x" is a DIFFERENT
// intent, not a cosmetic wart.
//
// This is the one impurity in an otherwise constant catalog (see the SCRIPTED-MODE
// DETERMINISM note in the package doc): it is deliberate and bounded to the origin.
// Nothing about MATCHING reads it, tokenize() only ever sees the need and the
// Description, so scores, the MinScore cut and the demo selections cannot move. The
// lexicographic Resource tie-break is likewise unmoved, because all three URLs share
// this same prefix, exactly as all three previously shared "GET /v1/".
//
// WHY LOOPBACK-ONLY (cubic P1 on this function). The policy allowlist matches
// Service.Merchant, a HOST compared by exact string (policy.go:317), while the resource
// became a URL in V6 and now carries an origin of its OWN. Unbounded, those two
// identities diverge: one env value aims the $0.04 profile purchase, which is below
// policy/demo.go's 0.10 ApprovalAboveMicros and so auto-approves with no human in the
// loop, at any host on the internet while the allowlist still reads
// "api.research-data.example" and still answers "approved". The sidecar does not close
// this, and cannot: it compares the PAYEE ADDRESS from the live 402 challenge against
// the approved intent (buyer.ts:199), but an attacker-chosen server writes that
// challenge, so the check proves only that the intent agrees with whoever answered. The
// defence has to be refusing to ask that server in the first place.
//
// Binding the URL's host to Merchant instead was the other candidate and is not
// available: the allowlisted literals are reserved-TLD hosts that deliberately never
// resolve ("api.research-data.example", "api.benchmarks.example") while the seller
// really answers on 127.0.0.1, so host equality would reject the DEFAULT origin and
// every demo purchase with it. Merchant must also stay a host (policy/demo.go,
// TestDemoPolicy_AllowlistsEveryDemoMerchant). Loopback is the trust anchor that IS
// available: the sidecar that does the probing binds 127.0.0.1 only (service.ts:486),
// the demo seller's origin is loopback too (seller.ts:226, and it is this file's
// default), this override's whole documented purpose is a PORT change, and a loopback
// origin cannot be an attacker's server unless the attacker already runs code on this
// host, in which case the env var is not their cheapest move.
//
// A rejected override falls back to the default rather than returning an error, so this
// stays the pure function the package doc promises. The fallback is not silent in
// effect: purchases then probe 127.0.0.1:4021 and fail PRE-SIGN with
// UPSTREAM_UNREACHABLE or RESOURCE_NOT_FOUND when nothing is bound there, which moves
// no money and names the misconfiguration in the failure.
func SellerBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(SellerBaseURLEnv)), "/")
	if base == "" || !isLoopbackOrigin(base) {
		return DefaultSellerBaseURL
	}
	return base
}

// isLoopbackOrigin reports whether raw is a bare http(s) origin whose host is LITERALLY
// loopback: an IP literal for which net.IP.IsLoopback holds (127.0.0.0/8, ::1), or
// "localhost". A DNS name is refused even when it would resolve to 127.0.0.1, because
// resolution needs a network and can change between the check and the purchase, while
// this decision must be pure and stable (package doc, design pin 3). That also refuses
// the near-miss shapes an attacker reaches for, "127.0.0.1.evil.example" and the
// decimal form "2130706433", since neither is an IP literal.
//
// Userinfo, a path, a query and a fragment are refused too: this value is an ORIGIN and
// gets concatenated with a verbatim seller path, and the result is hashed into
// intentHash byte-for-byte, so anything past scheme+host+port would silently rewrite the
// resource identity rather than merely relocate it.
func isLoopbackOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Opaque != "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Catalog is the discovery source seam. Today: the V2 stand-in snapshot. Later: the
// marketplace (or the sidecar's GET /v1/catalog, once proposed and landed).
type Catalog interface {
	Services(ctx context.Context) ([]Service, error)
}

// Static is a fixed in-memory catalog.
type Static []Service

func (s Static) Services(context.Context) ([]Service, error) { return s, nil }

// V2StandIn is the committed snapshot of sidecar/src/seller.ts, V2's three paid
// endpoints, descriptions and prices verbatim, merchants matching the demo policy
// allowlist. See the package doc for why this is a stand-in and how it retires.
//
// V6: the Resource values are now absolute URLs on SellerBaseURL(), because the sidecar
// probes them for a 402 challenge before it will sign anything (a display string is a
// pre-sign 404 RESOURCE_NOT_FOUND). The PATHS are verbatim from seller.ts:56-72.
//
// Merchant deliberately stays a HOST and is NOT rebuilt from the URL: policy/demo.go's
// allowlist holds these two literals (DemoMerchantProfile "api.research-data.example",
// DemoMerchantBenchmark "api.benchmarks.example") and policy.Evaluate matches them by
// exact string, so deriving "127.0.0.1:4021" from the origin here would silently deny
// every demo purchase on the merchant rule. The URL origin and the allowlisted host are
// two different facts about one seller, and only the host is a policy input.
func V2StandIn() Static {
	base := SellerBaseURL()
	return Static{
		{
			Merchant: "api.research-data.example", Resource: base + "/v1/company-profile",
			Description: "Competitor company profile", AmountMicros: 40_000,
		},
		{
			Merchant: "api.research-data.example", Resource: base + "/v1/premium-dataset",
			Description: "Premium market dataset (full competitive landscape)", AmountMicros: 4_000_000,
		},
		{
			Merchant: "api.benchmarks.example", Resource: base + "/v1/benchmark-summary",
			Description: "Coding-assistant benchmark summary", AmountMicros: 60_000,
		},
	}
}

// Match is one ranked suggestion.
type Match struct {
	Service
	Score float64
}

// Matcher ranks catalog services against a need. MinScore drops below-threshold
// matches so an unrelated need finds NOTHING rather than buying the least-irrelevant
// service, no match is a first-class honest outcome (the worker's source-abandoned
// path), not an error.
type Matcher struct {
	catalog  Catalog
	MinScore float64
}

// NewMatcher builds a matcher with the default threshold.
func NewMatcher(c Catalog) *Matcher { return &Matcher{catalog: c, MinScore: 0.15} }

// Find ranks services matching need, most similar first. maxAmountMicros > 0 filters
// to services at or under the cap, a filter only: IDF is computed over the FULL
// catalog, so a cap never re-scores a candidate. An empty result is not an error.
func (m *Matcher) Find(ctx context.Context, need string, maxAmountMicros int64) ([]Match, error) {
	services, err := m.catalog.Services(ctx)
	if err != nil {
		return nil, err
	}
	needTF := termFreq(tokenize(need))
	if len(needTF) == 0 {
		return nil, nil
	}

	// Document frequency over the whole catalog; idf(t) = ln(1 + N/df(t)), with unseen
	// need terms taking df=0's maximum, they match nothing but still dilute the need's
	// norm, so a mostly-garbage need scores honestly low.
	docs := make([]map[string]int, len(services))
	df := map[string]int{}
	for i, s := range services {
		docs[i] = termFreq(tokenize(s.Description))
		for t := range docs[i] {
			df[t]++
		}
	}
	n := float64(len(services))
	idf := func(t string) float64 { return math.Log(1 + n/float64(max(df[t], 1))) }

	var out []Match
	for i, s := range services {
		if maxAmountMicros > 0 && s.AmountMicros > maxAmountMicros {
			continue
		}
		score := cosine(needTF, docs[i], idf)
		if score < m.MinScore {
			continue
		}
		out = append(out, Match{Service: s, Score: score})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		if out[a].AmountMicros != out[b].AmountMicros {
			return out[a].AmountMicros < out[b].AmountMicros
		}
		return out[a].Resource < out[b].Resource
	})
	return out, nil
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func termFreq(tokens []string) map[string]int {
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	return tf
}

// cosine is the TF-IDF cosine similarity of the need against one description.
func cosine(need, doc map[string]int, idf func(string) float64) float64 {
	var dot, needNorm, docNorm float64
	for t, c := range need {
		w := float64(c) * idf(t)
		needNorm += w * w
		if dc, ok := doc[t]; ok {
			dot += w * float64(dc) * idf(t)
		}
	}
	for t, c := range doc {
		w := float64(c) * idf(t)
		docNorm += w * w
	}
	if dot == 0 || needNorm == 0 || docNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(needNorm) * math.Sqrt(docNorm))
}
