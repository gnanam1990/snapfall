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
	"strings"
	"syscall"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/agents"
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/config"
	"github.com/gnanam1990/snapfall/daemon/internal/discovery"
	"github.com/gnanam1990/snapfall/daemon/internal/events"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/logging"
	"github.com/gnanam1990/snapfall/daemon/internal/ownerapi"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/purchasing"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/supervisor"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"

	"github.com/ethereum/go-ethereum/common"
)

// discoveryFinder adapts discovery.Matcher to worker.Finder AT THE WIRING LAYER —
// worker and discovery never import each other (the G10 boundary: worker stays
// envelope-only per AT-16; discovery cannot name money or the worker).
type discoveryFinder struct{ m *discovery.Matcher }

func (f discoveryFinder) Find(ctx context.Context, need string, maxAmountMicros int64) ([]worker.Found, error) {
	ms, err := f.m.Find(ctx, need, maxAmountMicros)
	if err != nil {
		return nil, err
	}
	out := make([]worker.Found, 0, len(ms))
	for _, m := range ms {
		out = append(out, worker.Found{
			Merchant: m.Merchant, Resource: m.Resource, Description: m.Description,
			AmountMicros: m.AmountMicros, Score: m.Score,
		})
	}
	return out, nil
}

// arcTestnetChainID matches deployments/arc-testnet.json ("chainId": 5042002) and the
// indexer fixtures — the one chain this build's invoices and settlement observer read.
const arcTestnetChainID = uint64(5_042_002)

// lane binds one signing client to one contract address as a funding.Submitter —
// Funding holds the only references; nothing else can name a signer.
type lane struct {
	c  *chain.Client
	to common.Address
}

func (l lane) Submit(ctx context.Context, calldata []byte) (funding.ChainOutcome, error) {
	r, err := l.c.Submit(ctx, l.to, calldata)
	if err != nil {
		return funding.ChainOutcome{}, err
	}
	return funding.ChainOutcome{Submitted: true, TxHash: r.TxHash, Block: r.Block, GasUsed: r.GasUsed, Reverted: r.Reverted}, nil
}

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
		ownerVault   = flag.String("owner-vault", "", "bytes32 on-chain vault job id to bind to the one-shot job (the owner-side identity binding; empty = no chain identity)")
		apiAddr      = flag.String("api-addr", "127.0.0.1:4010", "H2 owner API bind address (loopback only unless SNAPFALL_OWNER_TOKEN is set)")
		deployment   = flag.String("deployment", "", "chain deployment config (deployments/arc-testnet.json); empty = no chain lanes, flows stop loudly at *.pending_chain")
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

	if err := run(log, cfg, *beats, *validateOnly, *ownerReq, *ownerJob, *ownerVault, *apiAddr, *deployment); err != nil {
		log.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg config.Config, beats int, validateOnly bool, ownerReq, ownerJob, ownerVault, apiAddr, deployment string) error {
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
	br, life, err := wireBrain(ctx, log, st, dbPath, cfg.OrgID, deployment)
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
			if ownerVault != "" {
				// The owner binds the job to its on-chain identity at request time — the
				// explicit demo-phase producer for vault_job_id (on-chain job creation
				// automating this binding remains open, and is said so at standup).
				if err := br.BindVaultJob(wctx, ownerJob, ownerVault); err != nil {
					return fmt.Errorf("vault binding: %w", err)
				}
				log.Info("job bound to on-chain identity", "job", ownerJob, "vault_job_id", ownerVault)
			}
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

	// The H2 owner API (docs/handshakes/H2-owner-api.md): the SSE stream + the approval
	// endpoints — the surface the reject-and-adapt beat is decided through.
	api := ownerapi.New(life, st, log)
	// H2 §4.1: the owner-request invoice trigger routes through Brain's single site.
	api.Generate = func(gctx context.Context, jobID string) (billing.Record, error) {
		return br.GenerateInvoice(gctx, jobID, "owner-request")
	}
	// The customer surface — the settlement principal's half of the fall: per-job
	// accept credentials, enforced per request on every customer route. The chain call
	// itself stops honestly at settlement.pending_chain until the deployment lands.
	api.MintAccept = br.MintAcceptCredential
	api.VerifyAccept = br.VerifyAcceptCredential
	api.Accept = br.AcceptDelivery
	api.JobState = func(jobID string) (string, bool) {
		js, ok := br.Job(jobID)
		if !ok {
			return "", false
		}
		return string(js.Stage), true
	}
	// The snap's owner-initiated trigger: the proposal lands pending in this same
	// inbox; the owner's approval mints the Grant and Funding stops honestly at
	// advance.pending_chain until the deployment lands.
	api.ProposeAdvance = br.ProposeAdvance
	api.WorkerCatalog = []ownerapi.WorkerManifest{{
		ID:            worker.BuildMonitorKind,
		Name:          "Build Monitor",
		Category:      "Engineering operations",
		Description:   "Watches committed repository milestones and reports completion evidence to Brain.",
		Permissions:   []string{"Read-only repo", "No payments", "No shell"},
		ChecklistPath: ".snapfall/milestone.json",
	}}
	api.HireWorker = buildMonitorHire(br)
	api.ListWorkerActivations = buildMonitorActivations(br)

	// The funding-observed advance trigger. HONEST STATE: never fired for real — no
	// deployment, no JobFunded rows, nothing writes vault ids — but it runs so funding
	// day needs no daemon change. Seeded-row tested in the brain package.
	if err := sup.Register(workerFunc{name: "funding-observer", fn: func(wctx context.Context) error {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-wctx.Done():
				return nil
			case <-tick.C:
				if n, err := br.ObserveFundingOnce(wctx); err != nil {
					log.Warn("funding observation failed", "err", err)
				} else if n > 0 {
					log.Info("funding-observed advance proposals", "count", n)
				}
			}
		}
	}}); err != nil {
		return err
	}
	if err := sup.Register(workerFunc{name: "owner-api", fn: func(wctx context.Context) error {
		return api.Run(wctx, apiAddr)
	}}); err != nil {
		return err
	}

	// H2 §4: the settlement-observed invoice trigger. HONEST STATE: this worker has
	// never had anything to observe — no deployment means no JobSettled rows and nothing
	// writes jobs' vault ids (the chain gap) — but it runs so settlement day needs no
	// daemon change. Proven against seeded rows in the brain tests.
	if err := sup.Register(workerFunc{name: "settlement-observer", fn: func(wctx context.Context) error {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-wctx.Done():
				return nil
			case <-tick.C:
				if n, err := br.ObserveSettlementsOnce(wctx); err != nil {
					log.Warn("settlement observation failed", "err", err)
				} else if n > 0 {
					log.Info("settlement-observed invoices generated", "count", n)
				}
			}
		}
	}}); err != nil {
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
func buildMonitorHire(br *brain.Brain) func(context.Context, ownerapi.HireWorkerRequest) (ownerapi.HireWorkerResult, error) {
	return func(hctx context.Context, req ownerapi.HireWorkerRequest) (ownerapi.HireWorkerResult, error) {
		if req.ManifestID != worker.BuildMonitorKind {
			return ownerapi.HireWorkerResult{}, fmt.Errorf("unknown worker manifest %q", req.ManifestID)
		}
		repository := strings.TrimSpace(req.Repository)
		cycle, err := br.EnsureMilestone(hctx, brain.Milestone{
			StandingInstructionID: "hire:" + repository,
			Number:                1,
			Repository:            repository,
			QuoteUSDC:             req.QuoteUSDC,
		})
		if err != nil {
			return ownerapi.HireWorkerResult{}, err
		}
		job, ok := br.Job(cycle.JobID)
		if !ok {
			return ownerapi.HireWorkerResult{}, fmt.Errorf("milestone %s disappeared after opening", cycle.JobID)
		}
		if job.Stage == brain.StageScoped {
			if err := br.Confirm(hctx, cycle.JobID, req.By); err != nil {
				return ownerapi.HireWorkerResult{}, err
			}
		} else if job.Stage == brain.StageConfirmed || job.Stage == brain.StageAssigned {
			if err := br.ResumeMilestone(hctx, cycle.JobID); err != nil {
				return ownerapi.HireWorkerResult{}, err
			}
		}
		if job.Stage == brain.StageScoped || job.Stage == brain.StageConfirmed || job.Stage == brain.StageAssigned {
			job, ok = br.Job(cycle.JobID)
			if !ok {
				return ownerapi.HireWorkerResult{}, fmt.Errorf("milestone %s disappeared after activation", cycle.JobID)
			}
		}
		return ownerapi.HireWorkerResult{
			JobID: cycle.JobID, VaultJobID: cycle.VaultJobID, State: string(job.Stage),
		}, nil
	}
}

func buildMonitorActivations(br *brain.Brain) func(context.Context) ([]ownerapi.WorkerActivation, error) {
	return func(context.Context) ([]ownerapi.WorkerActivation, error) {
		milestones, err := br.Milestones()
		if err != nil {
			return nil, err
		}
		activations := make([]ownerapi.WorkerActivation, 0, len(milestones))
		for _, milestone := range milestones {
			if !strings.HasPrefix(milestone.StandingInstructionID, "hire:") {
				continue
			}
			activations = append(activations, ownerapi.WorkerActivation{
				ManifestID: worker.BuildMonitorKind,
				Repository: milestone.Repository,
				QuoteUSDC:  milestone.QuoteUSDC,
				JobID:      milestone.JobID,
				VaultJobID: milestone.VaultJobID,
				State:      string(milestone.Stage),
			})
		}
		return activations, nil
	}
}

func wireBrain(ctx context.Context, log *slog.Logger, st *store.Store, dbPath, orgID, deployment string) (*brain.Brain, *approval.Lifecycle, error) {
	mem, err := brain.NewMemoryStore(filepath.Join(filepath.Dir(dbPath), "memory"))
	if err != nil {
		return nil, nil, fmt.Errorf("opening brain memory store: %w", err)
	}
	fund := funding.New()
	br := brain.New(log, st, mem, fund)
	br.SetScoper(brain.StubScoper{})
	// The G8 adaptive DD worker with its scripted source plan: the \$0.04 profile primary
	// (auto-approves under DemoPolicy) with the \$0.06 benchmark as the cheaper fallback.
	// G10: the DD worker DISCOVERS its sources by description — no merchant or resource
	// name reaches it (the scripted plan type no longer exists; discovery is the only
	// path the binary can run). Needs are a SLICE in demo-script order: the profile
	// ($0.04, auto-approves — the 0:45 beat) before the market need ($4.00 escalates,
	// a cost reason re-queries discovery for the $0.06 benchmark — the 1:10 beats).
	dd := worker.NewDiscoveryDD(worker.StubCompliance{},
		discoveryFinder{discovery.NewMatcher(discovery.V2StandIn())},
		[]string{discovery.DemoNeedProfile, discovery.DemoNeedMarket}, 1)
	if err := br.RegisterWorker(dd); err != nil {
		return nil, nil, err
	}
	if err := br.RegisterWorker(worker.NewBuildMonitor(worker.GitChecklistSource{})); err != nil {
		return nil, nil, err
	}
	if err := br.RegisterQAWorker(qa.Worker{}); err != nil {
		return nil, nil, err
	}

	// G11 kill switch: replayed from the event log, gating intake, dispatch, and payment.
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		return nil, nil, err
	}
	br.SetFreeze(reg, orgID)

	// The REAL Purchaser: policy -> approval -> (freeze-gated) execution. Money movement
	// is the F2 stub inside purchasing.execute; every decision and gate is genuine.
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	life.Freeze = reg
	br.SetPurchaser(purchasing.New(life, st, purchasing.RealClock{}, orgID, 5*time.Minute))

	// G12 Billing: the read-side invoice formatter over the shared store's chain rows
	// (arc-testnet chainId 5042002, deployments/arc-testnet.json). Brain alone holds the
	// pointer; GenerateInvoice is its single pinned invocation site.
	br.SetBilling(billing.New(st, arcTestnetChainID, nil))

	// The advance's human-authorized path (the snap's daemon half): proposals enter
	// the approval lifecycle pre-marked for the owner, execution flows through the
	// same Grant gates as payments, and Funding stops honestly at advance.pending_chain.
	adv := advancing.New(life, st, fund, log, orgID, 10*time.Minute)
	br.SetAdvanceFlow(adv)

	// The chain lanes — wired only when a deployment config is given. POSTURE, stated:
	// a configured deployment with a MISSING key wires no lane, and the affected flow
	// stops LOUDLY at *.pending_chain (a labeled pending record, never a fabricated
	// success) — the keys are env-only, format-validated, and never logged. The
	// treasury key signs requestAdvance; the customer key signs acceptDelivery
	// (SC-JV-005 — daemon-custodial for the demo, stated openly).
	if deployment != "" {
		dep, err := chaincfg.Load(deployment, os.LookupEnv)
		if err != nil {
			return nil, nil, fmt.Errorf("chain deployment: %w", err)
		}
		fpAddr := common.HexToAddress(dep.Contracts.FloatPool.Address)
		jvAddr := common.HexToAddress(dep.Contracts.JobVault.Address)
		var advanceLane, settleLane funding.Submitter
		var reader *chain.Client
		var operator common.Address
		if treasury, err := chain.NewFromEnv("TREASURY_PRIVATE_KEY", dep.Network.RPCURL, dep.Network.ChainID); err != nil {
			log.Warn("treasury chain lane NOT wired — advances stop at advance.pending_chain", "reason", err)
		} else {
			advanceLane, reader = lane{treasury, fpAddr}, treasury
			operator = treasury.Address()
			log.Info("treasury chain lane wired", "signer", treasury.Address().Hex(), "floatPool", fpAddr.Hex())
		}
		if customer, err := chain.NewFromEnv("SNAPFALL_CUSTOMER_PRIVATE_KEY", dep.Network.RPCURL, dep.Network.ChainID); err != nil {
			log.Warn("customer chain lane NOT wired — settlements stop at settlement.pending_chain", "reason", err)
		} else {
			settleLane = lane{customer, jvAddr}
			if reader == nil {
				reader = customer
			}
			log.Info("customer chain lane wired (demo-custodial)", "signer", customer.Address().Hex(), "jobVault", jvAddr.Hex())
		}
		fund.SetChain(advanceLane, settleLane)
		if reader != nil {
			oracle := chain.Oracle{Reader: reader, FloatPool: fpAddr, JobVault: jvAddr, Org: operator}
			adv.SetOracle(oracle)
			if operator != (common.Address{}) {
				br.SetMilestoneOracle(oracle)
			}
			// The chain is authoritative for a bound job's quote: read jobEconomics'
			// customerPayment so Brain's local quote matches the chain by construction
			// (no stub-quote divergence on camera; the reconciler stays quiet on funded).
			jv := jvAddr
			br.SetQuoteOracle(func(ctx context.Context, vaultJobID string) (string, bool) {
				id, err := chain.JobID32(vaultJobID)
				if err != nil {
					return "", false
				}
				ret, err := reader.CallView(ctx, jv, chain.CalldataJobEconomics(id))
				if err != nil {
					return "", false
				}
				_, payment, _, err := chain.DecodeJobEconomics(ret)
				if err != nil || payment.Sign() == 0 || !payment.IsInt64() {
					return "", false
				}
				return policy.FormatUSDC(payment.Int64()), true
			})
		}
	}

	// Restore the approval ledger, then surface any advance interrupted by the last
	// shutdown — approved-but-unexecuted advances are NEVER auto-executed on boot
	// (AT-09's crash posture); the owner is told and re-proposes.
	if err := life.Recover(ctx); err != nil {
		return nil, nil, err
	}
	if n, err := adv.EscalateInterrupted(ctx); err != nil {
		return nil, nil, err
	} else if n > 0 {
		log.Warn("advances interrupted by restart escalated to the owner", "count", n)
	}

	// Serve pin 2: task lifetimes bound to the daemon root — SIGTERM wakes blocked tasks
	// and refuses new dispatches; in-flight executions complete past their claim.
	br.SetRootContext(ctx)

	// Serve pins 1+3: exactly one Recover (guarded in Brain too), then interrupted tasks
	// escalate — a restart after clean shutdown and after a crash are the same case.
	if err := br.Recover(); err != nil {
		return nil, nil, err
	}
	if err := br.EscalateInterruptedTasks(ctx); err != nil {
		return nil, nil, err
	}
	log.Info("brain serving", "jobs_recovered", br.JobCount())
	return br, life, nil
}
