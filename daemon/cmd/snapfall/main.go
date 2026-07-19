// Command snapfall is the Snapfall local daemon (PRD §6.3, Appendix B).
//
// Day-1 scope: apply the schema, load and validate the four agent manifests, start the
// supervisor with one dummy worker, and drain the transactional outbox onto the typed bus.
// Orchestrator, action broker, policy engine, treasury signer, and chain indexer are not
// here yet — they are the rest of workstream B.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/agents"
	"github.com/gnanam1990/snapfall/daemon/internal/events"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/supervisor"
)

func main() {
	var (
		dbPath       = flag.String("db", "snapfall.db", "path to the local SQLite database")
		manifestDir  = flag.String("manifests", "manifests", "directory of agent manifests")
		beats        = flag.Int("beats", 0, "stop the dummy worker after N heartbeats (0 = run until interrupted)")
		validateOnly = flag.Bool("validate", false, "validate manifests and exit, without starting the daemon")
		heartbeatMS  = flag.Int("heartbeat-ms", 1000, "dummy worker heartbeat interval in milliseconds")
		verbose      = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log, *dbPath, *manifestDir, *beats, *heartbeatMS, *validateOnly); err != nil {
		log.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, dbPath, manifestDir string, beats, heartbeatMS int, validateOnly bool) error {
	// Ctrl-C and SIGTERM cancel the root context; every worker unwinds from there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Manifests (FR-ORG-006). Validation is fatal by design: an unsafe permission set
	//    must never reach an activated workforce. ──
	manifests, findings, err := agents.Load(manifestDir)
	for _, f := range findings {
		if f.Fatal {
			log.Error("manifest rejected", "role", string(f.Role), "code", f.Code, "detail", f.Message, "path", f.Path)
		} else {
			log.Warn("manifest warning", "role", string(f.Role), "code", f.Code, "detail", f.Message, "path", f.Path)
		}
	}
	if err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}
	log.Info("manifests validated", "count", len(manifests), "dir", manifestDir, "warnings", len(findings))
	for _, m := range manifests {
		log.Info("agent",
			"role", string(m.Role),
			"model", m.Model.Provider+"/"+m.Model.Name,
			"budget_usdc", m.BudgetUSDC,
			"can_sign", m.CanSignPayments,
			"can_borrow", m.CanRequestAdv,
			"escalates_to", m.EscalatesTo)
	}
	if validateOnly {
		return nil
	}

	// ── Local state ──
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	mode, err := st.JournalMode(ctx)
	if err != nil {
		return fmt.Errorf("reading journal mode: %w", err)
	}
	if mode != "wal" {
		// NFR-007 names WAL explicitly; silently running in rollback-journal mode would
		// weaken the durability guarantee the event log depends on.
		return fmt.Errorf("expected WAL journal mode, got %q", mode)
	}
	existing, err := st.EventCount(ctx)
	if err != nil {
		return err
	}
	log.Info("store ready", "path", dbPath, "journal_mode", mode, "existing_events", existing)

	// ── Typed bus + outbox publisher ──
	bus := events.NewBus()
	bus.SubscribeAll(func(_ context.Context, m events.Message) error {
		log.Debug("event published", "topic", m.Topic, "bytes", len(m.Payload))
		return nil
	})
	bus.Subscribe(events.KindWorkerHeartbeat, func(_ context.Context, m events.Message) error {
		log.Info("heartbeat observed on bus", "payload", string(m.Payload))
		return nil
	})

	publisher := events.NewPublisher(st, bus, log, 100*time.Millisecond)

	if _, err := st.Append(ctx, store.Event{
		Kind:     events.KindDaemonStarted,
		EntityID: "daemon",
		Actor:    "supervisor",
		Payload:  map[string]any{"manifests": len(manifests), "pid": os.Getpid()},
	}); err != nil {
		return err
	}

	// ── Supervisor + one dummy worker ──
	sup := supervisor.New(log, 5, 200*time.Millisecond)

	// The dummy worker runs as the Research role because that is the role the real
	// worker loop lands on first (it is the one that spends money).
	if err := sup.RegisterEssential(&agents.HeartbeatWorker{
		Role:     agents.RoleResearch,
		Store:    st,
		Log:      log,
		Interval: time.Duration(heartbeatMS) * time.Millisecond,
		Beats:    beats,
	}); err != nil {
		return err
	}

	// The publisher is itself a supervised worker, so a crash in outbox draining is
	// recovered on the same terms as an agent crash.
	if err := sup.Register(workerFunc{name: "outbox-publisher", fn: publisher.Run}); err != nil {
		return err
	}

	log.Info("supervisor starting", "workers", len(sup.Health()))
	sup.Start(ctx)
	sup.Wait()

	// One last drain so events emitted just before shutdown are not stranded unpublished.
	drained, err := publisher.Drain(context.WithoutCancel(ctx))
	if err != nil {
		log.Warn("final outbox drain failed", "err", err)
	}

	total, _ := st.EventCount(context.WithoutCancel(ctx))
	log.Info("daemon stopped", "events_total", total, "final_drain", drained)
	for _, h := range sup.Health() {
		log.Info("worker final state", "worker", h.Name, "state", string(h.State), "restarts", h.Restarts, "last_err", h.LastErr)
	}
	return nil
}

// workerFunc adapts a plain function to supervisor.Worker.
type workerFunc struct {
	name string
	fn   func(context.Context) error
}

func (w workerFunc) Name() string { return w.name }
func (w workerFunc) Run(ctx context.Context) error {
	err := w.fn(ctx)
	// A publisher that exits because its context ended is a clean stop, not a crash.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
