// Package discovery is G10: matching a worker's fuzzy need against a service catalog.
//
// HONEST LABEL: the matcher is a DETERMINISTIC LEXICAL MATCHER — TF-IDF cosine
// similarity over the catalog descriptions. It is an "embedding match" in the
// mechanical sense only (needs and descriptions become vectors, similarity ranks
// them); it is NOT a learned model and must never be presented as one. The Matcher is
// the seam where a real embedding model — or the Circle Agent Marketplace's own
// search — would slot in.
//
// THE CATALOG IS A STAND-IN (PRD §6.7, qualified): no Agent Marketplace API exists in
// this repo. V2StandIn() is a committed Go snapshot of our own seller's table
// (sidecar/src/seller.ts — V2's three paid endpoints, descriptions and prices
// verbatim). A Go snapshot of a TypeScript table is a drift risk with no
// cross-language tripwire; GET /v1/catalog is proposed to Vasanth as an H3 addition,
// and if it lands this snapshot retires in favor of an HTTP Catalog client.
//
// SUGGEST, NEVER AUTHORIZE (FR-DSC-001), structurally: this package imports no money
// package and no worker package — a Match cannot name a PaymentIntent, a Grant, or a
// Lifecycle (boundary_test.go). It becomes money only by being copied into a
// PurchaseRequest and submitted through the same Purchase seam as every other intent,
// job-stamped by Brain and gated by the deterministic policy engine, regardless of how
// the service was found.
//
// SCRIPTED-MODE DETERMINISM (design pin 3): Find is a pure function — no randomness,
// no clock, no network; the catalog and DemoNeed are committed constants; ordering is
// total and explicit (score desc, then cheaper first, then lexicographic resource);
// and the demo selections are pinned by tests WITH MARGINS, so drift fails a test
// instead of flipping a take. The demo need text is part of the script — chosen so the
// honest lexical match selects the beats we film, and said so plainly.
package discovery

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

// The committed demo-script needs, IN SCRIPT ORDER: profile first (the $0.04
// auto-approve beat, 0:45), market second (the $4.00 escalation and $0.06 adaptation,
// 1:10). Fuzzy language only — no merchant and no resource name. Selections are
// margin-pinned in discovery_test.go; order is the wiring's job (a slice, never a map).
const (
	DemoNeedProfile = "company profile and background of the vendor"
	DemoNeedMarket  = "vendor risk market data: competitive landscape and industry benchmark summary"
)

// Service is one catalog entry — everything a PurchaseRequest needs, and no more:
// no job, no approval state, no authority.
type Service struct {
	Merchant     string
	Resource     string
	Description  string
	AmountMicros int64
}

// Catalog is the discovery source seam. Today: the V2 stand-in snapshot. Later: the
// marketplace (or the sidecar's GET /v1/catalog, once proposed and landed).
type Catalog interface {
	Services(ctx context.Context) ([]Service, error)
}

// Static is a fixed in-memory catalog.
type Static []Service

func (s Static) Services(context.Context) ([]Service, error) { return s, nil }

// V2StandIn is the committed snapshot of sidecar/src/seller.ts — V2's three paid
// endpoints, descriptions and prices verbatim, merchants matching the demo policy
// allowlist. See the package doc for why this is a stand-in and how it retires.
func V2StandIn() Static {
	return Static{
		{
			Merchant: "api.research-data.example", Resource: "GET /v1/company-profile",
			Description: "Competitor company profile", AmountMicros: 40_000,
		},
		{
			Merchant: "api.research-data.example", Resource: "GET /v1/premium-dataset",
			Description: "Premium market dataset (full competitive landscape)", AmountMicros: 4_000_000,
		},
		{
			Merchant: "api.benchmarks.example", Resource: "GET /v1/benchmark-summary",
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
// service — no match is a first-class honest outcome (the worker's source-abandoned
// path), not an error.
type Matcher struct {
	catalog  Catalog
	MinScore float64
}

// NewMatcher builds a matcher with the default threshold.
func NewMatcher(c Catalog) *Matcher { return &Matcher{catalog: c, MinScore: 0.15} }

// Find ranks services matching need, most similar first. maxAmountMicros > 0 filters
// to services at or under the cap — a filter only: IDF is computed over the FULL
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
	// need terms taking df=0's maximum — they match nothing but still dilute the need's
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
