package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func telegramRequest() approval.Request {
	return approval.Request{
		ID: "apr_05779ad27ff0", JobID: "job_demo_1",
		IntentHash: "0x" + strings.Repeat("ab", 32),
		Intent: approval.Intent{
			JobID: "job_demo_1", Merchant: "api.research.example",
			Resource: "GET /v1/premium", AmountMicros: 4_000_000,
			Purpose: "premium market dataset",
		},
		State:     approval.StatePending,
		ExpiresAt: time.Date(2026, 7, 24, 10, 5, 0, 0, time.UTC),
	}
}

func TestLoadConfigIsOptionalButRefusesPartialOrUnsafeConfiguration(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}
	}
	cfg, err := LoadConfig(lookup(nil))
	if err != nil || cfg.Enabled() || cfg.DashboardURL != defaultDashboardURL {
		t.Fatalf("disabled config = %+v err=%v", cfg, err)
	}
	for _, values := range []map[string]string{
		{"SNAPFALL_TELEGRAM_BOT_TOKEN": "secret"},
		{"SNAPFALL_TELEGRAM_CHAT_ID": "123"},
		{
			"SNAPFALL_TELEGRAM_BOT_TOKEN": "secret",
			"SNAPFALL_TELEGRAM_CHAT_ID":   "123",
			"SNAPFALL_DASHBOARD_URL":      "javascript:alert(1)",
		},
		{
			"SNAPFALL_TELEGRAM_BOT_TOKEN": "secret",
			"SNAPFALL_TELEGRAM_CHAT_ID":   "123",
			"SNAPFALL_DASHBOARD_URL":      "https://snapfall.example/?token=secret",
		},
		{
			"SNAPFALL_TELEGRAM_BOT_TOKEN": "secret",
			"SNAPFALL_TELEGRAM_CHAT_ID":   "123",
			"SNAPFALL_DASHBOARD_URL":      "http://:3000",
		},
	} {
		if _, err := LoadConfig(lookup(values)); err == nil {
			t.Fatalf("configuration %+v must be refused", values)
		}
	}
	cfg, err = LoadConfig(lookup(map[string]string{
		"SNAPFALL_TELEGRAM_BOT_TOKEN": "secret",
		"SNAPFALL_TELEGRAM_CHAT_ID":   "123",
		"SNAPFALL_DASHBOARD_URL":      "https://snapfall.example/app",
	}))
	if err != nil || !cfg.Enabled() {
		t.Fatalf("enabled config = %+v err=%v", cfg, err)
	}
}

func TestSendMirrorsApprovalWithDashboardDecisionLinks(t *testing.T) {
	var got sendMessageRequest
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("content-type") != "application/json" {
			t.Fatalf("request = %s content-type=%q", r.Method, r.Header.Get("content-type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	cfg := Config{
		BotToken: "123:secret", ChatID: "-10042",
		DashboardURL: "https://snapfall.example/owner",
	}
	notifier := newNotifier(cfg, server.Client(), server.URL, slog.Default())
	req := telegramRequest()
	if err := notifier.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if path != "/bot123:secret/sendMessage" {
		t.Fatalf("Telegram path = %q", path)
	}
	if got.ChatID != "-10042" || !got.DisableWebPagePreview {
		t.Fatalf("sendMessage payload = %+v", got)
	}
	for _, want := range []string{
		"4.000000 USDC", req.Intent.Resource, req.Intent.Merchant,
		req.Intent.Purpose, req.JobID, req.IntentHash,
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("message does not contain %q:\n%s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, cfg.BotToken) {
		t.Fatal("bot token leaked into the message body")
	}

	buttons := got.ReplyMarkup.InlineKeyboard
	if len(buttons) != 2 || len(buttons[0]) != 3 || len(buttons[1]) != 1 {
		t.Fatalf("inline keyboard = %+v", buttons)
	}
	for index, decision := range []string{"approve", "reject", "request_alternative"} {
		target := buttons[0][index].URL
		if !strings.Contains(target, "requestId="+req.ID) || !strings.Contains(target, "decision="+decision) {
			t.Fatalf("%s link = %q", decision, target)
		}
	}
	if strings.Contains(buttons[1][0].URL, "decision=") {
		t.Fatalf("review link preselects a decision: %q", buttons[1][0].URL)
	}
}

func TestRunDrainsQueuedApprovalAndStopsCleanly(t *testing.T) {
	delivered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delivered <- struct{}{}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	notifier := newNotifier(Config{
		BotToken: "token", ChatID: "42", DashboardURL: "http://127.0.0.1:3000",
	}, server.Client(), server.URL, slog.Default())
	if !notifier.Enqueue(telegramRequest()) {
		t.Fatal("first notification must fit the bounded queue")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- notifier.Run(ctx) }()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("queued approval was not delivered")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("notifier did not stop on cancellation")
	}
}

func TestRecoveredApprovalsBeyondLegacyQueueCapacityAreAllDelivered(t *testing.T) {
	const total = 65
	delivered := make(chan struct{}, total)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delivered <- struct{}{}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	notifier := newNotifier(Config{
		BotToken: "token", ChatID: "42", DashboardURL: "http://127.0.0.1:3000",
	}, server.Client(), server.URL, slog.Default())
	for index := 0; index < total; index++ {
		req := telegramRequest()
		req.ID = fmt.Sprintf("apr_recovered_%02d", index)
		if !notifier.Enqueue(req) {
			t.Fatalf("recovered approval %d was dropped before notifier startup", index+1)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- notifier.Run(ctx) }()
	for count := 0; count < total; count++ {
		select {
		case <-delivered:
		case <-time.After(2 * time.Second):
			t.Fatalf("delivered %d/%d recovered approvals", count, total)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPendingApprovalLifecycleMirrorsToTelegram(t *testing.T) {
	delivered := make(chan sendMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message sendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Fatal(err)
		}
		delivered <- message
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "telegram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	notifier := newNotifier(Config{
		BotToken: "token", ChatID: "42", DashboardURL: "http://127.0.0.1:3000",
	}, server.Client(), server.URL, slog.Default())
	life.Pending = func(req approval.Request) {
		if !notifier.Enqueue(req) {
			t.Error("pending approval did not fit notifier queue")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- notifier.Run(ctx) }()

	result, err := life.Submit(context.Background(), approval.Intent{
		IntentID: "pi_telegram", OrgID: "org_demo", JobID: "job_telegram",
		AgentID: "due-diligence", Merchant: policy.DemoMerchantPremium,
		Resource: "GET /v1/premium-dataset", AmountMicros: 4_000_000,
		MaxAmountMicros: 4_000_000, Purpose: "premium",
		Nonce: "0x" + strings.Repeat("71", 32), ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Request == nil || result.Request.State != approval.StatePending {
		t.Fatalf("approval result = %+v", result)
	}
	select {
	case message := <-delivered:
		if !strings.Contains(message.Text, result.Request.ID) &&
			!strings.Contains(message.ReplyMarkup.InlineKeyboard[0][0].URL, result.Request.ID) {
			t.Fatalf("Telegram mirror does not identify request %s: %+v", result.Request.ID, message)
		}
	case <-time.After(time.Second):
		t.Fatal("pending approval was not mirrored to Telegram")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSendSurfacesTelegramFailureWithoutEchoingCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized"}`)
	}))
	defer server.Close()
	const token = "secret-token"
	notifier := newNotifier(Config{
		BotToken: token, ChatID: "42", DashboardURL: "http://127.0.0.1:3000",
	}, server.Client(), server.URL, slog.Default())
	err := notifier.Send(context.Background(), telegramRequest())
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), token) {
		t.Fatalf("failure = %v", err)
	}

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed for " + req.URL.String())
	})
	notifier = newNotifier(Config{
		BotToken: token, ChatID: "42", DashboardURL: "http://127.0.0.1:3000",
	}, &http.Client{Transport: transport}, "https://api.telegram.org", slog.Default())
	err = notifier.Send(context.Background(), telegramRequest())
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "transport unavailable") {
		t.Fatalf("transport failure = %v", err)
	}
}
