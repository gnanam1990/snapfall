package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/config"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/supervisor"
	"github.com/gnanam1990/snapfall/daemon/internal/telegram"
)

func TestConfigureTelegramApprovalsIsOptionalAndFailClosed(t *testing.T) {
	for _, key := range []string{
		"SNAPFALL_TELEGRAM_BOT_TOKEN",
		"SNAPFALL_TELEGRAM_CHAT_ID",
		"SNAPFALL_DASHBOARD_URL",
	} {
		t.Setenv(key, "")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := approval.New(nil, time.Now)
	sup := supervisor.New(log, 1, time.Millisecond)
	cfg, err := telegram.LoadConfig(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := configureTelegramApprovals(cfg, life, sup, log); err != nil {
		t.Fatal(err)
	}
	if len(sup.Health()) != 0 || life.Pending != nil {
		t.Fatal("disabled Telegram configuration changed the approval lifecycle")
	}

	t.Setenv("SNAPFALL_TELEGRAM_BOT_TOKEN", "secret")
	if _, err := telegram.LoadConfig(os.LookupEnv); err == nil ||
		!strings.Contains(err.Error(), "must be configured together") {
		t.Fatalf("partial Telegram configuration = %v", err)
	}
}

func TestConfigureTelegramApprovalsRegistersMirrorWithoutChangingDecisionAuthority(t *testing.T) {
	t.Setenv("SNAPFALL_TELEGRAM_BOT_TOKEN", "123:secret")
	t.Setenv("SNAPFALL_TELEGRAM_CHAT_ID", "-10042")
	t.Setenv("SNAPFALL_DASHBOARD_URL", "http://127.0.0.1:3000")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := approval.New(nil, time.Now)
	sup := supervisor.New(log, 1, time.Millisecond)

	called := 0
	life.Pending = func(approval.Request) { called++ }
	cfg, err := telegram.LoadConfig(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := configureTelegramApprovals(cfg, life, sup, log); err != nil {
		t.Fatal(err)
	}
	health := sup.Health()
	if len(health) != 1 || health[0].Name != "telegram-approvals" {
		t.Fatalf("supervisor health = %+v", health)
	}
	life.Pending(approval.Request{ID: "apr_test", JobID: "job_test"})
	if called != 1 {
		t.Fatalf("existing pending observer calls = %d, want 1", called)
	}
}

func TestInvalidTelegramConfigurationDoesNotRecordDaemonStarted(t *testing.T) {
	t.Setenv("SNAPFALL_TELEGRAM_BOT_TOKEN", "secret")
	t.Setenv("SNAPFALL_TELEGRAM_CHAT_ID", "")
	t.Setenv("SNAPFALL_DASHBOARD_URL", "")
	cfg := config.Defaults()
	cfg.DBPath = filepath.Join(t.TempDir(), "snapfall.db")
	cfg.ManifestDir = filepath.Join("..", "..", "manifests")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := run(log, cfg, 1, false, "", "", "", "127.0.0.1:0", "")
	if err == nil || !strings.Contains(err.Error(), "must be configured together") {
		t.Fatalf("invalid Telegram startup = %v", err)
	}
	st, openErr := store.Open(context.Background(), cfg.DBPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer st.Close()
	var started int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM events WHERE kind = 'daemon.started'`,
	).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("invalid configuration recorded %d daemon.started event(s)", started)
	}
}
