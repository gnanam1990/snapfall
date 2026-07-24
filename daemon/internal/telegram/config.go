// Package telegram mirrors pending approval requests to the owner's Telegram chat.
// Telegram never records a decision itself: every action deep-links to the dashboard,
// which renders the current request and posts its displayed intent hash through H2.
package telegram

import (
	"fmt"
	"net/url"
	"strings"
)

const defaultDashboardURL = "http://127.0.0.1:3000"

// Config is the optional Telegram approval-notification configuration.
type Config struct {
	BotToken     string
	ChatID       string
	DashboardURL string
}

// LoadConfig reads Telegram settings through an injected environment lookup.
func LoadConfig(lookup func(string) (string, bool)) (Config, error) {
	value := func(key string) string {
		raw, _ := lookup(key)
		return strings.TrimSpace(raw)
	}
	cfg := Config{
		BotToken:     value("SNAPFALL_TELEGRAM_BOT_TOKEN"),
		ChatID:       value("SNAPFALL_TELEGRAM_CHAT_ID"),
		DashboardURL: value("SNAPFALL_DASHBOARD_URL"),
	}
	if cfg.DashboardURL == "" {
		cfg.DashboardURL = defaultDashboardURL
	}
	if cfg.BotToken == "" && cfg.ChatID == "" {
		return cfg, nil
	}
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return Config{}, fmt.Errorf(
			"SNAPFALL_TELEGRAM_BOT_TOKEN and SNAPFALL_TELEGRAM_CHAT_ID must be configured together",
		)
	}
	dashboard, err := url.Parse(cfg.DashboardURL)
	if err != nil || dashboard.Host == "" || (dashboard.Scheme != "http" && dashboard.Scheme != "https") {
		return Config{}, fmt.Errorf("SNAPFALL_DASHBOARD_URL must be an absolute http(s) URL")
	}
	if dashboard.User != nil {
		return Config{}, fmt.Errorf("SNAPFALL_DASHBOARD_URL must not contain credentials")
	}
	if dashboard.RawQuery != "" || dashboard.Fragment != "" {
		return Config{}, fmt.Errorf("SNAPFALL_DASHBOARD_URL must not contain a query or fragment")
	}
	return cfg, nil
}

// Enabled reports whether the optional Telegram notifier should run.
func (c Config) Enabled() bool { return c.BotToken != "" && c.ChatID != "" }
