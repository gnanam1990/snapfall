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
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/config"
	"github.com/gnanam1990/snapfall/daemon/internal/events"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/logging"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/purchasing"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/supervisor"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func main() {
	var (
		configPath   = flag.String("config", "snapfall.yaml", "path to the YAML config file")
		dbPath       = flag.String("db", "", "path to the local SQLite database (overrides config)")
		manifestDir  = flag.String("manifests", "", "directory of agent manifests (overrides config)")
		beats        = flag.Int("beats", 0, "stop the dummy worker after N heartbeats (0 = run until interrupted)")
		validateOnly = flag.Bool("validate", false, "validate manifests and exit, without starting the daemon")
		heartbeatMS  = flag.Int("heartbeat-ms", 0, "dummy worker heartbeat interval in ms (overrides config)")
		verbose      = flag.Bool("v", false, "debug logging (overrides config log_level)")
		ownerReq     = flag.String("owner-request", "", "one-shot serve: submit this owner request, confirm it, run the DD task to its terminal state, then exit")
		ownerJob     = flag.String("owner-job", "job_demo_1", "job ID for the one-shot owner request")
	)
	flag.Parse()

	// G1 config precedence: defaults -> YAML -> env -> flags.
	configExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configExplicit = true
		}
	})
	cfg, err := config.Load(*configPath, configExplicit)
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("config load failed", "err", err)
		os.Exit(1)
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *manifestDir != "" {
		cfg.ManifestDir = *manifestDir
	}
	if *heartbeatMS > 0 {
		cfg.HeartbeatMS = *heartbeatMS
	}
	if *verbose {
		cfg.LogLevel = "debug"
	}

	level := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "error": slog.LevelError,
	}[cfg.LogLevel]
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log, cfg, *beats, *validateOnly, *ownerReq, *ownerJob); err != nil {
		log.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg config.Config, beats int, validateOnly bool, ownerReq, ownerJob string) error {
	// Ctrl-C and SIGTERM cancel the root context; every worker unwinds from there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// G1: the org correlation ID scopes every log line from here down.
	ctx = logging.With(ctx, logging.Correlation{Org: cfg.OrgID})
	log = logging.From(ctx, log)
	dbPath, manifestDir, heartbeatMS := cfg.DBPath, cfg.ManifestDir, cfg.HeartbeatMS

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

	// ── Brain: wired, recovered, SERVED (the G8 serve step — the promise from #4). ──
	// One Brain, one Recover, one escalation pass, then it serves: tasks bound to this
	// daemon's root context, spends routed through the real policy+approval Purchaser.
	br, err := wireBrain(ctx, log, st, dbPath, cfg.OrgID)
	if err != nil {
		return err
	}

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

	// ── Supervisor ──
	sup := supervisor.New(log, 5, 200*time.Millisecond)

	// The dummy heartbeat runs as the Research role. In one-shot serve mode it is
	// INFRASTRUCTURE (the owner one-shot below is what the daemon exists to finish);
	// otherwise it stays essential, preserving the bounded --beats behavior.
	hb := &agents.HeartbeatWorker{
		Role:     agents.RoleResearch,
		Store:    st,
		Log:      log,
		Interval: time.Duration(heartbeatMS) * time.Millisecond,
		Beats:    beats,
	}
	if ownerReq != "" {
		if err := sup.Register(hb); err != nil {
			return err
		}
		// The one-shot owner flow, through the SERVED Brain: request -> confirm ->
		// async DD task (real policy decision inside) -> await terminal -> exit.
		if err := sup.RegisterEssential(workerFunc{name: "owner-oneshot", fn: func(wctx context.Context) error {
			proposal, err := br.HandleOwnerRequest(wctx, ownerJob, ownerReq)
			if err != nil {
				return fmt.Errorf("owner request: %w", err)
			}
			log.Info("scope proposed", "job", proposal.JobID, "scope", proposal.Scope, "quote_usdc", proposal.QuoteUSDC)
			if err := br.Confirm(wctx, ownerJob, "gnanam"); err != nil {
				return fmt.Errorf("owner confirm: %w", err)
			}
			log.Info("owner confirmed; DD task dispatched async")
			if err := br.AwaitTask(ownerJob); err != nil {
				return fmt.Errorf("dd task: %w", err)
			}
			js, _ := br.Job(ownerJob)
			log.Info("dd task terminal", "job", ownerJob, "stage", string(js.Stage), "revisions", js.RevisionCount)
			return nil
		}}); err != nil {
			return err
		}
	} else {
		if err := sup.RegisterEssential(hb); err != nil {
			return err
		}
	}

	// The publisher is itself a supervised worker, so a crash in outbox draining is
	// recovered on the same terms as an agent crash.
	if err := sup.Register(workerFunc{name: "outbox-publisher", fn: publisher.Run}); err != nil {
		return err
	}

	log.Info("supervisor starting", "workers", len(sup.Health()))
	sup.Start(ctx)
	sup.Wait()

	// Shutdown drain (serve pin 2): blocked tasks were woken by root-ctx cancellation and
	// any in-flight payment execution completed past its claim under the Purchaser's
	// shield — wait for those goroutines before closing the store under them.
	br.WaitTasks()

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

// wireBrain is THE single Brain wiring point in the daemon (pinned by
// TestMain_SingleBrainWiringSite): one Brain, one Recover, one escalation pass, then
// serve. Constructing a second Brain over the same store would race two replays of the
// same event log — the double-recovery hazard from #4.
func wireBrain(ctx context.Context, log *slog.Logger, st *store.Store, dbPath, orgID string) (*brain.Brain, error) {
	mem, err := brain.NewMemoryStore(filepath.Join(filepath.Dir(dbPath), "memory"))
	if err != nil {
		return nil, fmt.Errorf("opening brain memory store: %w", err)
	}
	br := brain.New(log, st, mem, funding.New())
	br.SetScoper(brain.StubScoper{})
	// The G8 adaptive DD worker with its scripted source plan: the \$0.04 profile primary
	// (auto-approves under DemoPolicy) with the \$0.06 benchmark as the cheaper fallback.
	// The AT-04 reject-and-adapt beat needs an owner surface to answer the escalation, so
	// the served one-shot exercises the auto path; the adaptive path is pinned by the
	// integration tests until the owner surface (dashboard/Telegram) lands.
	dd := worker.NewAdaptiveDD(worker.StubCompliance{}, []worker.SourceNeed{{
		Primary: worker.PurchaseRequest{
			Merchant: policy.DemoMerchantProfile, Resource: "GET /v1/company-profile",
			AmountMicros: 40_000, MaxAmountMicros: 40_000, Purpose: "company profile source",
		},
		Cheaper: &worker.PurchaseRequest{
			Merchant: policy.DemoMerchantBenchmark, Resource: "GET /v1/benchmark-summary",
			AmountMicros: 60_000, MaxAmountMicros: 60_000, Purpose: "benchmark summary (cheaper source)",
		},
	}}, 1)
	if err := br.RegisterWorker(dd); err != nil {
		return nil, err
	}
	if err := br.RegisterQAWorker(qa.Worker{}); err != nil {
		return nil, err
	}

	// G11 kill switch: replayed from the event log, gating intake, dispatch, and payment.
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		return nil, err
	}
	br.SetFreeze(reg, orgID)

	// The REAL Purchaser: policy -> approval -> (freeze-gated) execution. Money movement
	// is the F2 stub inside purchasing.execute; every decision and gate is genuine.
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	life.Freeze = reg
	br.SetPurchaser(purchasing.New(life, st, purchasing.RealClock{}, orgID, 5*time.Minute))

	// Serve pin 2: task lifetimes bound to the daemon root — SIGTERM wakes blocked tasks
	// and refuses new dispatches; in-flight executions complete past their claim.
	br.SetRootContext(ctx)

	// Serve pins 1+3: exactly one Recover (guarded in Brain too), then interrupted tasks
	// escalate — a restart after clean shutdown and after a crash are the same case.
	if err := br.Recover(); err != nil {
		return nil, err
	}
	if err := br.EscalateInterruptedTasks(ctx); err != nil {
		return nil, err
	}
	log.Info("brain serving", "jobs_recovered", br.JobCount())
	return br, nil
}
