// Package approval is the G7 approval lifecycle (FR-APR-001..005, SEC-006, AT-03/04/05).
//
// This file owns the two canonicalizations and the hashes over them:
//
//   - InternalHash: the daemon's approval binding. Covers EVERY field of Intent by
//     construction (reflection over the struct), so a field cannot be excluded by
//     forgetting to list it. Completeness is additionally PROVEN by
//     TestAT05_EveryFieldIsBound, which mutates each field and asserts invalidation.
//
//   - WireHash: Vasanth's H3 §3.3 canonicalization, EXACTLY the 14 fixed fields, keys
//     lexicographic, no whitespace, keccak256 over utf8. Byte-compatibility notes:
//     JSON string values are encoded with HTML escaping DISABLED, because JS
//     JSON.stringify does not escape & < > and Go's encoding/json does by default.
//
// The two hashes are different objects on purpose; hash_test.go carries adversarial
// vectors demonstrating exactly where they diverge (fields outside the 14 collide on
// the wire hash), that divergence is surfaced to the H3 review, not papered over.
package approval

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

// Intent is everything the owner approves. It carries the policy engine's input fields,
// G7's time bound, the AT-04 provenance link, and the H3 wire fields (zero-valued until
// the Funding agent populates them from a quote).
//
// EVERY exported field participates in InternalHash, adding a field here automatically
// adds it to the binding, and the reflection test in hash_test.go automatically covers it.
type Intent struct {
	IntentID string
	OrgID    string
	JobID    string
	TaskID   string
	AgentID  string
	// Kind is the intent's action class ("" = payment, mirroring policy.KindPayment).
	// Advances enter through SubmitAdvance ONLY, pre-marked HumanApprovalRequired,
	// never evaluated (Evaluate's rule 0 denies the kind by law if misrouted).
	// CanonicalInternal reflects over every field, so Kind binds into the hash.
	Kind string
	// Merchant is the merchant HOST ("api.research-data.example"), and it is what the
	// policy allowlist matches by exact string (policy.go:317, FR-PAY-003 "merchant/domain
	// allowlist"). A domain is the better security primitive here because an x402 payTo
	// address rotates while the host does not. It is NOT what the H3 wire's `merchant` key
	// carries, see PayTo.
	Merchant string
	// Resource is the human/display form of what is being bought ("GET /v1/company-profile"),
	// the value discovery produces and the owner surface renders. It is NOT what the H3
	// wire's `resource` key carries, see ResourceURL.
	Resource      string
	AmountMicros  int64
	Purpose       string
	Nonce         string
	PolicyVersion string
	// ExpiresAt bounds the approval's validity (SEC-006). G7 owns time; the policy
	// engine never sees this field.
	ExpiresAt time.Time
	// ChainRef names the on-chain entity this intent acts on (the bytes32 vault job
	// id for an advance), "" for intents with no chain half. Hash-bound internally
	// (CanonicalInternal reflects every field); classified OFF the H3 wire.
	ChainRef string
	// AlternativeTo links this intent to the request-alternative decision that spawned
	// it ("" = not an alternative). Gives the activity feed (F3) explicit causality for
	// the "owner rejects, worker adapts" story instead of inferring it from ordering.
	AlternativeTo string
	// Wire fields (H3 §3.1), populated by Funding from the quote at integration time.
	Asset           string
	Network         string
	MaxAmountMicros int64
	// PayTo is the x402 payee ADDRESS from the quote's accept.payTo, resolved from
	// Merchant (a HOST, which is what the policy allowlist matches: policy.go:317).
	// The H3 wire `merchant` key carries THIS, not Merchant: the sidecar compares it
	// case-insensitively against accept.payTo (buyer.ts:199) and a host fails
	// MERCHANT_CHANGED. Copy accept.payTo VERBATIM, never re-cased or checksummed -
	// because the live comparison is case-insensitive but intentHash is not, so a
	// re-cased address passes the check and hashes to something the sidecar never
	// computed. Hash-bound automatically (CanonicalInternal reflects every field), which
	// AT-05 requires: the owner approved a specific payee.
	PayTo string
	// ResourceURL is the absolute http(s) URL the sidecar probes, resolved from Resource
	// (a "GET /v1/x" display string). The H3 wire `resource` key carries THIS: a
	// non-URL yields 404 RESOURCE_NOT_FOUND "resource is not a valid URL". Hash-bound
	// internally for the same AT-05 reason as PayTo, the owner approved a specific
	// thing to buy, and funding must not be able to substitute the target outside the
	// hash the approvalToken is signed over.
	ResourceURL string
}

// keccak256 is the Ethereum-style legacy Keccak (what viem's keccak256 computes),
// NOT standard SHA3-256.
func keccak256(data []byte) string {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// jsonString encodes s as a JSON string WITHOUT HTML escaping, matching JS
// JSON.stringify byte-for-byte for the characters both languages must escape.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// A Go string always encodes; this is unreachable, but never silently.
		panic(fmt.Sprintf("encoding %q: %v", s, err))
	}
	out := string(bytes.TrimRight(buf.Bytes(), "\n"))
	// Go escapes U+2028/U+2029 unconditionally; JS JSON.stringify emits them literally.
	// Normalize to the JS behavior so the byte-compat claim holds for every valid JSON
	// string. A blind ReplaceAll is WRONG (review fix): for the six literal input chars
	// `\u2028`, the encoder produces `\\u2028`, and replacing the trailing `\u2028` there
	// would corrupt an escaped backslash into a lone one. Only an ENCODER-GENERATED escape
	// is converted - distinguished by an ODD run of preceding backslashes.
	return normalizeLineSeparators(out)
}

// normalizeLineSeparators converts an encoder-emitted `\u2028`/`\u2029` escape to the
// literal rune (matching JS), while leaving a `\u2028` that is really an escaped
// backslash followed by the text "u2028" untouched. Parity of the backslash run is the
// discriminator: JSON escapes literal backslashes as pairs, so a real `\u` escape is the
// odd one out.
func normalizeLineSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '\\' {
			j++
		}
		run := j - i
		rest := s[j:]
		if run%2 == 1 && (strings.HasPrefix(rest, "u2028") || strings.HasPrefix(rest, "u2029")) {
			// Odd run: the last backslash introduces a real \uXXXX escape. Emit the
			// escaped-backslash pairs verbatim, then the literal separator rune.
			b.WriteString(strings.Repeat(`\`, run-1))
			if rest[4] == '8' {
				b.WriteRune('\u2028')
			} else {
				b.WriteRune('\u2029')
			}
			i = j + 5
			continue
		}
		// Even run (all escaped backslashes) or not a separator escape: verbatim.
		b.WriteString(s[i:j])
		i = j
	}
	return b.String()
}

// canonicalValue renders one Intent field value deterministically.
func canonicalValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return jsonString(v.String())
	case reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Struct:
		if t, ok := v.Interface().(time.Time); ok {
			return jsonString(t.UTC().Format(time.RFC3339Nano))
		}
	}
	panic(fmt.Sprintf("unhashable Intent field kind %s, extend canonicalValue deliberately", v.Kind()))
}

// CanonicalInternal renders the FULL intent: every exported field, keys sorted
// lexicographically, no whitespace. Reflection makes omission impossible.
func CanonicalInternal(i Intent) string {
	v := reflect.ValueOf(i)
	t := v.Type()

	names := make([]string, 0, t.NumField())
	values := make(map[string]string, t.NumField())
	for f := 0; f < t.NumField(); f++ {
		name := t.Field(f).Name
		names = append(names, name)
		values[name] = canonicalValue(v.Field(f))
	}
	sort.Strings(names)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for idx, name := range names {
		if idx > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(jsonString(name))
		buf.WriteByte(':')
		buf.WriteString(values[name])
	}
	buf.WriteByte('}')
	return buf.String()
}

// InternalHash is the daemon-side approval binding (FR-APR-003: "bound to the exact
// intent hash"). keccak256 over the full-intent canonical JSON.
func InternalHash(i Intent) string {
	return keccak256([]byte(CanonicalInternal(i)))
}

// wireFields is H3 §3.3's EXACT field set, in its exact lexicographic order.
// `decision` and `createdAt` are excluded there by design; note OrgID and
// AlternativeTo are simply absent from the wire vocabulary, hash_test.go
// demonstrates the resulting collisions adversarially.
//
// Two keys are deliberately NOT fed by the same-named Intent field, because the daemon
// and the sidecar mean different things by those words (V6 §1.G):
//
//   - `merchant` carries Intent.PayTo (the x402 payee ADDRESS the sidecar compares to
//     accept.payTo, buyer.ts:199), NOT Intent.Merchant (the HOST the policy allowlist
//     matches, policy.go:317). Sending the host fails 409 MERCHANT_CHANGED.
//   - `resource` carries Intent.ResourceURL (the absolute URL the sidecar HTTP-probes),
//     NOT Intent.Resource (a "GET /v1/x" display string, which fails 404
//     RESOURCE_NOT_FOUND "resource is not a valid URL").
//
// Both source fields are still bound by InternalHash (CanonicalInternal reflects every
// field), so the owner's decision binds to the payee and the target URL as well as to
// the host and the display string. Merchant/Resource are OFF the wire hash, which
// hash_test.go's adversarial matrix classifies and pins.
func wireFields(i Intent) [][2]string {
	return [][2]string{
		{"agentId", jsonString(i.AgentID)},
		{"amount", jsonString(strconv.FormatInt(i.AmountMicros, 10))},
		{"asset", jsonString(i.Asset)},
		{"expiresAt", jsonString(i.ExpiresAt.UTC().Format(time.RFC3339))},
		{"intentId", jsonString(i.IntentID)},
		{"jobId", jsonString(i.JobID)},
		{"maxAmount", jsonString(strconv.FormatInt(i.MaxAmountMicros, 10))},
		{"merchant", jsonString(i.PayTo)},
		{"network", jsonString(i.Network)},
		{"nonce", jsonString(i.Nonce)},
		{"policyVersion", jsonString(i.PolicyVersion)},
		{"purpose", jsonString(i.Purpose)},
		{"resource", jsonString(i.ResourceURL)},
		{"taskId", jsonString(i.TaskID)},
	}
}

// CanonicalWire renders the H3 §3.3 canonical JSON: the 14 fields, sorted keys,
// no whitespace, string values verbatim (amounts as atomic decimal strings).
func CanonicalWire(i Intent) string {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for idx, kv := range wireFields(i) {
		if idx > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(jsonString(kv[0]))
		buf.WriteByte(':')
		buf.WriteString(kv[1])
	}
	buf.WriteByte('}')
	return buf.String()
}

// WireHash is H3's intentHash: keccak256 over the wire-canonical JSON. This is the
// value the approvalToken carries and the sidecar recomputes.
func WireHash(i Intent) string {
	return keccak256([]byte(CanonicalWire(i)))
}
