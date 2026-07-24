package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
)

const defaultAPIBase = "https://api.telegram.org"

type inlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type replyMarkup struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type sendMessageRequest struct {
	ChatID                string      `json:"chat_id"`
	Text                  string      `json:"text"`
	DisableWebPagePreview bool        `json:"disable_web_page_preview"`
	ReplyMarkup           replyMarkup `json:"reply_markup"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notifier is a supervised, bounded outbound Telegram notification worker.
type Notifier struct {
	cfg     Config
	client  *http.Client
	apiBase string
	log     *slog.Logger
	queue   chan approval.Request
}

// New constructs a production Telegram notifier.
func New(cfg Config, log *slog.Logger) *Notifier {
	return newNotifier(cfg, &http.Client{Timeout: 10 * time.Second}, defaultAPIBase, log)
}

func newNotifier(cfg Config, client *http.Client, apiBase string, log *slog.Logger) *Notifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		cfg: cfg, client: client, apiBase: strings.TrimRight(apiBase, "/"),
		log: log, queue: make(chan approval.Request, 64),
	}
}

// Name implements supervisor.Worker.
func (*Notifier) Name() string { return "telegram-approvals" }

// Enqueue schedules one approval mirror without blocking the money path.
func (n *Notifier) Enqueue(req approval.Request) bool {
	select {
	case n.queue <- req:
		return true
	default:
		return false
	}
}

// Run drains approval notifications until daemon shutdown. Telegram outages are
// surfaced in logs but never crash or block the authoritative dashboard/H2 path.
func (n *Notifier) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-n.queue:
			if err := n.Send(ctx, req); err != nil {
				n.log.Warn("telegram approval notification failed",
					"request_id", req.ID, "job_id", req.JobID, "err", err)
			}
		}
	}
}

// Send mirrors one pending request to Telegram with dashboard decision deep links.
func (n *Notifier) Send(ctx context.Context, req approval.Request) error {
	if !n.cfg.Enabled() {
		return fmt.Errorf("telegram notifier is not configured")
	}
	payload := sendMessageRequest{
		ChatID:                n.cfg.ChatID,
		Text:                  approvalText(req),
		DisableWebPagePreview: true,
		ReplyMarkup: replyMarkup{InlineKeyboard: [][]inlineButton{
			{
				{Text: "Approve", URL: decisionURL(n.cfg.DashboardURL, req.ID, "approve")},
				{Text: "Reject", URL: decisionURL(n.cfg.DashboardURL, req.ID, "reject")},
				{Text: "Request cheaper", URL: decisionURL(n.cfg.DashboardURL, req.ID, "request_alternative")},
			},
			{{Text: "Review full request", URL: decisionURL(n.cfg.DashboardURL, req.ID, "")}},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := n.apiBase + "/bot" + url.PathEscape(n.cfg.BotToken) + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	response, err := n.client.Do(request)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("sendMessage canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("sendMessage timed out")
		default:
			// net/http errors commonly include the complete request URL. Telegram puts
			// the bot token in that URL, so the underlying error must never reach logs.
			return fmt.Errorf("sendMessage transport unavailable")
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("read sendMessage response: %w", err)
	}
	var result telegramResponse
	_ = json.Unmarshal(body, &result)
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		description := strings.TrimSpace(result.Description)
		if description == "" {
			description = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("sendMessage failed (%d): %s", response.StatusCode, description)
	}
	return nil
}

func approvalText(req approval.Request) string {
	return fmt.Sprintf(
		"Approval needed\n\n%s USDC · %s\nMerchant: %s\nPurpose: %s\nJob: %s\nExpires: %s\nIntent hash: %s\n\nTelegram opens the intent-bound dashboard record; the decision is recorded through H2.",
		policy.FormatUSDC(req.Intent.AmountMicros),
		oneLine(req.Intent.Resource, 512),
		oneLine(req.Intent.Merchant, 256),
		oneLine(req.Intent.Purpose, 512),
		oneLine(req.JobID, 128),
		req.ExpiresAt.UTC().Format(time.RFC3339),
		req.IntentHash,
	)
}

func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func decisionURL(base, requestID, decision string) string {
	target, _ := url.Parse(base)
	target.Path = strings.TrimRight(target.Path, "/") + "/approvals"
	target.RawQuery = ""
	target.Fragment = ""
	query := target.Query()
	query.Set("requestId", requestID)
	if decision != "" {
		query.Set("decision", decision)
	}
	target.RawQuery = query.Encode()
	return target.String()
}
