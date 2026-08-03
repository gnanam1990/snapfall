package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// scoperTimeout bounds the hosted model call. HandleOwnerRequest is synchronous — the owner is
// waiting for the proposal — so this trades scope quality for responsiveness. It is larger than
// the local-model budget was (8s): a hosted call adds network round-trips and provider-side
// queueing on top of generation, so 12s absorbs a slow-but-responding API while still keeping the
// owner's wait bounded. A rate limit or an unreachable API does NOT consume this budget: a 429 is
// a non-200 and falls back immediately (never a retry into the owner's wait), and a refused/DNS-
// failed connection is caught by the shorter dial timeout below. A job must never fail because a
// model was slow, so the timeout ends in a fallback, not an error.
const scoperTimeout = 12 * time.Second

// scoperDialTimeout bounds connection establishment only. When the key is set but the API is
// unreachable (no internet, DNS failure, a firewall dropping packets), the operator gets the
// deterministic path in ~seconds rather than waiting out the full scoperTimeout. A refused
// connection returns even faster. The full timeout only bites once the connection is up but the
// response is slow.
const scoperDialTimeout = 3 * time.Second

// maxScopeLen bounds untrusted model output before it reaches a worker and the activity feed.
// A due-diligence scope is one sentence; anything near this cap means the model went off the
// rails, and the deterministic scope is the safer thing to ship.
const maxScopeLen = 2000

// scoperInstruction is the fixed system prompt. It says "scope text only" for a reason the money
// depends on: the quote and worker kind are NOT model-chosen (see anthropicScoper.Scope), so a
// price the model tried to set would be ignored anyway — but telling it not to keeps the output
// clean.
const scoperInstruction = "You scope a due-diligence task for an autonomous AI workforce. " +
	"Given the owner's request, reply with ONE short sentence stating the due-diligence scope. " +
	"Output only that sentence — no price, no numbered steps, no code, no instructions, no preamble."

// defaultScoperModel is the model used when ANTHROPIC_API_KEY is set but SNAPFALL_SCOPER_MODEL is
// not. Claude Haiku 4.5 is the right pick for a one-sentence framing task: cheapest and fastest of
// the current models, at roughly a tenth of a cent per call for a few hundred tokens each way
// ($1/$5 per million in/out). The scope quality is not money-sensitive — deterministic code sets
// every figure that moves funds — so paying Opus rates here would buy nothing.
const defaultScoperModel = "claude-haiku-4-5"

const anthropicBaseURL = "https://api.anthropic.com"

// LOCAL-FIRST QUALIFICATION. When the scoper is enabled (ANTHROPIC_API_KEY set), the owner's
// request text leaves the machine: it is sent to Anthropic to produce the scope framing. ONLY the
// request text crosses the wire — no keys, no addresses, no job state (see generate, requirement
// 5). This is the one place a Snapfall operator's own words leave their machine, and it is opt-in:
// with no key set, NewScoper returns the deterministic StubScoper and nothing leaves the machine,
// which is what keeps the local-first claim true by default. Documented in the README's "Honest
// limits" section.
//
// anthropicScoper produces the scope TEXT with a hosted Claude model and falls back to the
// deterministic StubScoper on any failure. The quote and worker kind are deliberately NOT
// model-chosen: they come from the fallback stub, unchanged, so the addresses.md settlement proof
// (a fixed 25.00 quote → pool 0.561 / operator 0.439) stays reproducible run to run. The model
// proposes the framing; deterministic code sets the money. That split is the point.
type anthropicScoper struct {
	fallback StubScoper
	client   *http.Client
	baseURL  string
	apiKey   string
	model    string
	timeout  time.Duration
	log      *slog.Logger
}

// NewScoper selects the scoper from the environment.
//
//   - ANTHROPIC_API_KEY unset ⇒ StubScoper, silently. The demo, the tests and spine_run depend on
//     determinism, and an absent key must not break the build, the spine, or the local-first claim.
//   - set ⇒ a Claude-backed scoper (model from SNAPFALL_SCOPER_MODEL, default claude-haiku-4-5)
//     that still falls back to the stub on any error.
//
// Opt-in, not opt-out: the local-first default is the deterministic path.
func NewScoper(log *slog.Logger) Scoper {
	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if apiKey == "" {
		return StubScoper{}
	}
	model := strings.TrimSpace(os.Getenv("SNAPFALL_SCOPER_MODEL"))
	if model == "" {
		model = defaultScoperModel
	}
	return &anthropicScoper{
		fallback: StubScoper{},
		client:   newScoperHTTPClient(),
		baseURL:  anthropicBaseURL,
		apiKey:   apiKey,
		model:    model,
		timeout:  scoperTimeout,
		log:      log,
	}
}

// newScoperHTTPClient builds a client whose connection-establishment is bounded by the short
// dial timeout so an unreachable API fails fast, while the overall request stays bounded by the
// per-call context timeout.
func newScoperHTTPClient() *http.Client {
	return &http.Client{
		Timeout: scoperTimeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: scoperDialTimeout}).DialContext,
			TLSHandshakeTimeout: scoperDialTimeout,
		},
	}
}

// Scope implements Scoper. It never propagates a model failure: chat.go fails the whole job on a
// scoper error, so every model problem — timeout, refused/unreachable connection, rate limit
// (429), non-200, malformed/empty/over-length response — is turned into the deterministic scope
// with a nil error. The only error it returns is the stub's own rejection of an empty request,
// which is genuinely invalid input, not a model problem.
func (s *anthropicScoper) Scope(ctx context.Context, request string) (string, string, string, error) {
	stubScope, quote, kind, err := s.fallback.Scope(ctx, request)
	if err != nil {
		return "", "", "", err
	}
	scope, gerr := s.generate(ctx, request)
	if gerr != nil {
		if s.log != nil {
			// One WARN per fallback so an operator who set the key but hit an error (rate limit,
			// unreachable, slow) sees why scopes are deterministic — without failing any job.
			s.log.Warn("scoper: deterministic fallback", "reason", gerr.Error(), "model", s.model)
		}
		return stubScope, quote, kind, nil
	}
	// The model set only the scope text. quote and kind are the stub's, unchanged.
	return scope, quote, kind, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// generate calls Anthropic's Messages API and returns the cleaned scope, or an error the caller
// turns into a fallback. Only the owner's request text crosses to the model (requirement 5): the
// system prompt is the fixed instruction, the sole user message is the request verbatim, and
// Scope's signature only ever receives `request` — no keys, no addresses, no job state. The API
// key is auth on the header, never a message.
func (s *anthropicScoper) generate(ctx context.Context, request string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	body, err := json.Marshal(anthropicRequest{
		Model:     s.model,
		MaxTokens: 128,
		System:    scoperInstruction,
		Messages:  []anthropicMessage{{Role: "user", Content: request}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	// A refused/unreachable API makes Do return quickly (dial timeout), giving the operator the
	// deterministic path in seconds. A 429 or 529 comes back as a non-200 below and falls back
	// immediately — never a retry, which would push the rate-limit delay onto the owner's wait.
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic status %d", resp.StatusCode)
	}
	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	scope := strings.TrimSpace(firstText(out.Content))
	if scope == "" {
		return "", fmt.Errorf("empty scope from model")
	}
	if len(scope) > maxScopeLen {
		return "", fmt.Errorf("scope %d chars over %d-char bound", len(scope), maxScopeLen)
	}
	// Collapse to a single line. The scope is a one-line framing; newlines would let a model
	// smuggle a second "instruction" line into the activity feed and the worker report, and
	// nothing downstream needs the structure.
	return strings.Join(strings.Fields(scope), " "), nil
}

// firstText returns the text of the first text block, ignoring any non-text blocks a future model
// might return.
func firstText(blocks []anthropicContentBlock) string {
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}
