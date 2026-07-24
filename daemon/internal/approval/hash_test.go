package approval

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func vectorIntent() Intent {
	return Intent{
		IntentID:        "pi_9f3c1a2b",
		OrgID:           "org_demo",
		JobID:           "job_104",
		TaskID:          "task_research_01",
		AgentID:         "market-researcher",
		Merchant:        "0x1111111111111111111111111111111111111111",
		Resource:        "http://127.0.0.1:4021/v1/company-profile",
		AmountMicros:    40_000,
		Purpose:         "Competitor profile for job_104",
		Nonce:           "0x" + strings.Repeat("ab", 32),
		PolicyVersion:   "pol_7",
		ExpiresAt:       time.Date(2026, 7, 22, 10, 5, 0, 0, time.UTC),
		AlternativeTo:   "",
		Asset:           "0x2222222222222222222222222222222222222222",
		Network:         "eip155:5042002",
		MaxAmountMicros: 40_000,
	}
}

// mutateField changes field f of the intent to a different, valid value.
func mutateField(in Intent, name string) Intent {
	v := reflect.ValueOf(&in).Elem()
	f := v.FieldByName(name)
	switch f.Kind() {
	case reflect.String:
		f.SetString(f.String() + "-mutated")
	case reflect.Int64:
		f.SetInt(f.Int() + 1)
	case reflect.Struct:
		if t, ok := f.Interface().(time.Time); ok {
			f.Set(reflect.ValueOf(t.Add(time.Second)))
			break
		}
		panic("unknown struct field " + name)
	default:
		panic("unhandled kind for field " + name)
	}
	return in
}

// ─────────────────────────────────────────────────────────────────────────
// The completeness pin (Step-3 pin 1) — structural, like AT-16
// ─────────────────────────────────────────────────────────────────────────

// EVERY field of Intent is bound by InternalHash: mutate each field one at a time and
// assert the hash changes. A future field added to Intent is covered automatically —
// an unhashed field cannot slip in silently.
func TestAT05_EveryFieldIsBound(t *testing.T) {
	base := vectorIntent()
	baseHash := InternalHash(base)

	typ := reflect.TypeOf(base)
	if typ.NumField() == 0 {
		t.Fatal("empty Intent — the test proves nothing")
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			mutated := mutateField(base, name)
			if InternalHash(mutated) == baseHash {
				t.Fatalf("field %s is NOT bound by the internal hash — it can change post-approval without invalidating (the AT-05 hole)", name)
			}
		})
	}
}

// The internal hash is deterministic and input-independent of field mutation order.
func TestInternalHash_Deterministic(t *testing.T) {
	a, b := vectorIntent(), vectorIntent()
	if InternalHash(a) != InternalHash(b) {
		t.Fatal("same intent, different hashes")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Adversarial vectors: internal hash vs H3 wire hash (Step-4 addition 1)
// ─────────────────────────────────────────────────────────────────────────

// wireCoveredFields maps Intent fields to whether H3 §3.3's 14-field set covers them.
var wireCoveredFields = map[string]bool{
	"IntentID": true, "JobID": true, "TaskID": true, "AgentID": true,
	"Merchant": true, "Resource": true, "AmountMicros": true, "Purpose": true,
	"Nonce": true, "PolicyVersion": true, "ExpiresAt": true,
	"Asset": true, "Network": true, "MaxAmountMicros": true,
	// NOT on the wire:
	"OrgID":         false,
	"AlternativeTo": false,
	// Kind is DELIBERATELY off the wire hash: H3 §3.3's 14-field set is the frozen
	// sidecar payment contract, and advances never cross that wire — an advance-kind
	// intent's Grant drives FloatPool.requestAdvance, not a sidecar /v1/pay. The
	// INTERNAL hash covers Kind (CanonicalInternal reflects every field), so a decision
	// binds to the kind (AT-05); two intents differing only in Kind share a wire hash,
	// which is irrelevant because only payment-kind intents are ever wire-hashed.
	"Kind": false,
}

// The adversarial matrix: for EVERY Intent field, pin whether mutating it changes the
// wire hash. Fields inside the 14 must diverge it; fields outside MUST collide — and
// each collision is a documented finding for the H3 review, not an accident.
func TestWireHash_AdversarialFieldMatrix(t *testing.T) {
	base := vectorIntent()
	baseWire := WireHash(base)

	typ := reflect.TypeOf(base)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		covered, known := wireCoveredFields[name]
		if !known {
			t.Fatalf("field %s is not classified in wireCoveredFields — classify it deliberately before merging", name)
		}
		t.Run(name, func(t *testing.T) {
			mutated := mutateField(base, name)
			changed := WireHash(mutated) != baseWire
			if covered && !changed {
				t.Fatalf("field %s should be inside H3's 14-field hash but mutating it did NOT change the wire hash", name)
			}
			if !covered && changed {
				t.Fatalf("field %s is outside H3's 14 fields yet changed the wire hash — the wire canonicalization is wrong", name)
			}
		})
	}
}

// The two named collisions, stated as explicit adversarial pairs — two DIFFERENT
// intents, identical wire hash. These are the H3-review findings:
//
//  1. OrgID: two orgs' otherwise-identical intents are indistinguishable to any
//     sidecar-side dedup or replay check keyed on the wire hash. Single-org demo: no
//     exposure today; multi-org: the wire contract must add it or scope hashes per org.
//  2. AlternativeTo: the AT-04 provenance link is invisible on the wire, so the wire
//     hash cannot distinguish an original from its replacement if nonce/terms matched.
//     (In practice a fresh nonce always separates them — the finding is structural.)
func TestWireHash_KnownCollisions(t *testing.T) {
	base := vectorIntent()

	orgSwap := base
	orgSwap.OrgID = "org_other"
	if WireHash(orgSwap) != WireHash(base) {
		t.Fatal("expected OrgID to be outside the wire hash (H3 finding #1)")
	}
	if InternalHash(orgSwap) == InternalHash(base) {
		t.Fatal("the internal hash must distinguish orgs even when the wire hash cannot")
	}

	linked := base
	linked.AlternativeTo = "apr_deadbeef1234"
	if WireHash(linked) != WireHash(base) {
		t.Fatal("expected AlternativeTo to be outside the wire hash (H3 finding #2)")
	}
	if InternalHash(linked) == InternalHash(base) {
		t.Fatal("the internal hash must bind the provenance link")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Canonicalization byte-compat with JS (the SetEscapeHTML hazard)
// ─────────────────────────────────────────────────────────────────────────

// A resource URL with a query string must canonicalize with a literal '&' — Go's
// default JSON encoding would emit & and silently diverge from JS JSON.stringify.
func TestCanonicalWire_DoesNotHTMLEscape(t *testing.T) {
	in := vectorIntent()
	in.Resource = "http://127.0.0.1:4021/v1/data?a=1&b=<2>"

	c := CanonicalWire(in)
	if !strings.Contains(c, `"http://127.0.0.1:4021/v1/data?a=1&b=<2>"`) {
		t.Fatalf("canonical JSON HTML-escaped the URL — diverges from JS JSON.stringify:\n%s", c)
	}
	// The ESCAPED forms must be absent (Go's default encoder would emit \u0026 etc.).
	for _, esc := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(c, esc) {
			t.Fatalf("found HTML escape %s in canonical JSON:\n%s", esc, c)
		}
	}
}

// Review batch: Go's encoder escapes U+2028/U+2029 even with SetEscapeHTML(false);
// JS JSON.stringify emits them literally. The canonicalization must emit the LITERAL
// characters or the wire hash diverges on any purpose string containing them.
// (The separators are written via escapes HERE so the test itself is unambiguous;
// the assertion is that the canonical output carries the raw characters.)
func TestCanonicalWire_LineSeparatorsMatchJS(t *testing.T) {
	sep := "\u2028line two\u2029" // Go source escapes -> literal chars in the string
	in := vectorIntent()
	in.Purpose = "line one" + sep + "end"

	c := CanonicalWire(in)
	if !strings.Contains(c, "line one"+sep+"end") {
		t.Fatalf("U+2028/U+2029 not emitted literally — diverges from JS JSON.stringify:\n%q", c)
	}
	for _, esc := range []string{`\u2028`, `\u2029`} {
		if strings.Contains(c, esc) {
			t.Fatalf("found %s escape in canonical JSON:\n%q", esc, c)
		}
	}
	// The internal canonicalization uses the same encoder — same property.
	ci := CanonicalInternal(in)
	if strings.Contains(ci, `\u2028`) || strings.Contains(ci, `\u2029`) {
		t.Fatalf("internal canonical form still escapes line separators:\n%q", ci)
	}
}

// Review fix (Anandan #2, the second vector): a purpose containing the LITERAL six
// characters backslash-u-2-0-2-8 (NOT the U+2028 rune) must survive canonicalization
// intact. Go's encoder renders the backslash as `\\`, producing `\\u2028`; the old blind
// ReplaceAll rewrote the trailing escape into a raw separator, diverging from JS
// JSON.stringify("...\\u2028..."). Written with Go escapes so the vector is unambiguous.
func TestCanonicalWire_LiteralBackslashUEscapePreserved(t *testing.T) {
	const literalSeq = "\\u2028" // six chars: backslash, u, 2, 0, 2, 8 — verified below
	if len(literalSeq) != 6 || literalSeq[0] != '\\' || literalSeq[1] != 'u' {
		t.Fatalf("vector is not the six literal chars: %q (len %d)", literalSeq, len(literalSeq))
	}
	in := vectorIntent()
	in.Purpose = "before" + literalSeq + "after"

	c := CanonicalWire(in)
	// JS escapes the backslash: the canonical form carries `\\u2028` (escaped backslash +
	// literal u2028), never a raw U+2028 rune.
	if !strings.Contains(c, `before\\u2028after`) {
		t.Fatalf("literal backslash-u2028 not preserved as \\\\u2028 (JS-incompatible):\n%q", c)
	}
	if strings.ContainsRune(c, '\u2028') {
		t.Fatalf("a raw U+2028 rune leaked from a literal-escape input — corruption:\n%q", c)
	}

	// The literal escape and the real rune are distinct inputs and MUST hash differently.
	rune2028 := vectorIntent()
	rune2028.Purpose = "before\u2028after"
	if WireHash(in) == WireHash(rune2028) {
		t.Fatal("literal backslash-u2028 and the real U+2028 rune hash identically — lossy")
	}
}

// The wire canonical form matches H3 §3.3's shape exactly: 14 keys, lexicographic,
// no whitespace. Golden vector printed for cross-checking against Vasanth's JS side.
func TestCanonicalWire_ShapeAndGoldenVector(t *testing.T) {
	in := vectorIntent()
	c := CanonicalWire(in)

	want := `{"agentId":"market-researcher","amount":"40000","asset":"0x2222222222222222222222222222222222222222",` +
		`"expiresAt":"2026-07-22T10:05:00Z","intentId":"pi_9f3c1a2b","jobId":"job_104","maxAmount":"40000",` +
		`"merchant":"0x1111111111111111111111111111111111111111","network":"eip155:5042002",` +
		`"nonce":"0x` + strings.Repeat("ab", 32) + `","policyVersion":"pol_7",` +
		`"purpose":"Competitor profile for job_104","resource":"http://127.0.0.1:4021/v1/company-profile",` +
		`"taskId":"task_research_01"}`

	if c != want {
		t.Fatalf("wire canonical form diverged from H3 §3.3:\n got: %s\nwant: %s", c, want)
	}

	// The golden pair for the cross-language test: JS must produce this exact hash from
	// the same intent (keccak256 over utf8 of the canonical string).
	fmt.Printf("GOLDEN VECTOR canonical=%s\nGOLDEN VECTOR wireHash=%s\n", c, WireHash(in))
}
