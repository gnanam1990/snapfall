package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testScoper(baseURL string, timeout time.Duration) *anthropicScoper {
	return &anthropicScoper{
		fallback: StubScoper{},
		client:   &http.Client{Timeout: timeout, Transport: &http.Transport{}},
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   "test-key",
		model:    "test-model",
		timeout:  timeout,
	}
}

// textResponse builds a minimal Messages API response carrying one text block.
func textResponse(text string) anthropicResponse {
	return anthropicResponse{
		Content:    []anthropicContentBlock{{Type: "text", Text: text}},
		StopReason: "end_turn",
	}
}

// The keyless default must be the deterministic stub, byte-identical to today — the demo,
// tests and spine_run depend on it and a missing key must not change a single character. Absent
// key is also what keeps the local-first claim true.
func TestNewScoperDefaultsToStubWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	s := NewScoper(nil)
	if _, ok := s.(StubScoper); !ok {
		t.Fatalf("NewScoper without key = %T, want StubScoper", s)
	}
	got, gq, gk, _ := s.Scope(context.Background(), "assess Acme Corp")
	want, wq, wk, _ := StubScoper{}.Scope(context.Background(), "assess Acme Corp")
	if got != want || gq != wq || gk != wk {
		t.Errorf("keyless scope = (%q,%q,%q), want stub (%q,%q,%q)", got, gq, gk, want, wq, wk)
	}
}

func TestNewScoperSelectsAnthropicWithKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("SNAPFALL_SCOPER_MODEL", "")
	s := NewScoper(nil)
	as, ok := s.(*anthropicScoper)
	if !ok {
		t.Fatalf("ANTHROPIC_API_KEY set should select the Anthropic scoper, got %T", s)
	}
	if as.model != defaultScoperModel {
		t.Errorf("model = %q, want default %q when SNAPFALL_SCOPER_MODEL unset", as.model, defaultScoperModel)
	}
}

func TestNewScoperHonorsModelOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("SNAPFALL_SCOPER_MODEL", "claude-opus-4-8")
	as, ok := NewScoper(nil).(*anthropicScoper)
	if !ok {
		t.Fatal("ANTHROPIC_API_KEY set should select the Anthropic scoper")
	}
	if as.model != "claude-opus-4-8" {
		t.Errorf("model = %q, want the SNAPFALL_SCOPER_MODEL override", as.model)
	}
}

// The model sets the scope TEXT; the quote and worker kind stay the stub's, so the settlement
// proof stays reproducible. Also asserts ONLY the fixed instruction and the owner's request text
// crossed the wire — no keys, no addresses, no job state (requirement 5). The api key is auth on
// the header, and the header is not part of the body.
func TestAnthropicScoperTextOnlyMoneyDeterministic(t *testing.T) {
	var gotBody []byte
	var gotKeyHeader, gotVersionHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotKeyHeader = r.Header.Get("x-api-key")
		gotVersionHeader = r.Header.Get("anthropic-version")
		_ = json.NewEncoder(w).Encode(textResponse("  Scope the customer's regulatory exposure.\n"))
	}))
	defer srv.Close()

	s := testScoper(srv.URL, 2*time.Second)
	scope, quote, kind, err := s.Scope(context.Background(), "check Acme regulatory risk")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "Scope the customer's regulatory exposure." {
		t.Errorf("scope = %q, want trimmed single line", scope)
	}
	if quote != "25.00" || kind != "due-diligence" {
		t.Errorf("quote/kind = %q/%q, want 25.00/due-diligence (money is not model-chosen)", quote, kind)
	}
	if gotKeyHeader != "test-key" {
		t.Errorf("x-api-key header = %q, want the key set on auth (not a message)", gotKeyHeader)
	}
	if gotVersionHeader != "2023-06-01" {
		t.Errorf("anthropic-version header = %q, want 2023-06-01", gotVersionHeader)
	}

	var sent anthropicRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.System != scoperInstruction {
		t.Errorf("system carried more than the fixed instruction: %q", sent.System)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" || sent.Messages[0].Content != "check Acme regulatory risk" {
		t.Errorf("body must carry exactly the owner's request as the sole user message, got %+v", sent.Messages)
	}
	// The key must never appear in the request body.
	if strings.Contains(string(gotBody), "test-key") {
		t.Errorf("api key leaked into the request body: %s", gotBody)
	}
}

// Every model problem falls back to the deterministic scope with a nil error — a job never
// fails because the model misbehaved. Includes 429 (rate limit): it is a non-200 and falls
// back immediately, without a retry into the owner's wait.
func TestAnthropicScoperFallsBackOnFailures(t *testing.T) {
	big := strings.Repeat("x", maxScopeLen+1)
	cases := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"non-200", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"rate-limited", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }},
		{"overloaded", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(529) }},
		{"empty", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(textResponse("   ")) }},
		{"no-text-block", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(anthropicResponse{Content: nil, StopReason: "end_turn"})
		}},
		{"over-length", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(textResponse(big)) }},
		{"garbage", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json") }},
	}
	stub, _, _, _ := StubScoper{}.Scope(context.Background(), "assess Acme")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.h)
			defer srv.Close()
			// A rate limit must fall back immediately, not spend the timeout on a retry.
			start := time.Now()
			scope, quote, kind, err := testScoper(srv.URL, 8*time.Second).Scope(context.Background(), "assess Acme")
			if err != nil {
				t.Fatalf("must not error, got %v", err)
			}
			if scope != stub || quote != "25.00" || kind != "due-diligence" {
				t.Errorf("%s: got (%q,%q,%q), want deterministic fallback", c.name, scope, quote, kind)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("%s: fallback took %v — an error response must not retry into the owner's wait", c.name, elapsed)
			}
		})
	}
}

// Key set but the API unreachable: the dial is refused and the fallback is near-instant, NOT
// after the full 12s timeout. An operator who set the key but is offline must not wait per job.
func TestAnthropicScoperFailsFastWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down := srv.URL
	srv.Close() // nothing listens on this URL now → connection refused

	s := testScoper(down, scoperTimeout) // full timeout, but refusal should beat it easily
	start := time.Now()
	scope, _, _, err := s.Scope(context.Background(), "assess Acme")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("must not error, got %v", err)
	}
	stub, _, _, _ := StubScoper{}.Scope(context.Background(), "assess Acme")
	if scope != stub {
		t.Errorf("want deterministic fallback, got %q", scope)
	}
	if elapsed > 2*time.Second {
		t.Errorf("fallback took %v — a refused connection must be near-instant, not near the 12s timeout", elapsed)
	}
}

// A slow-but-alive API is the only case that waits: it falls back at the timeout.
func TestAnthropicScoperTimeoutFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(textResponse("too late"))
	}))
	defer srv.Close()

	s := testScoper(srv.URL, 50*time.Millisecond) // short timeout keeps the test fast
	start := time.Now()
	scope, _, _, err := s.Scope(context.Background(), "assess Acme")
	if err != nil {
		t.Fatalf("must not error, got %v", err)
	}
	stub, _, _, _ := StubScoper{}.Scope(context.Background(), "assess Acme")
	if scope != stub {
		t.Errorf("timeout should fall back to stub, got %q", scope)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout fallback took too long")
	}
}
