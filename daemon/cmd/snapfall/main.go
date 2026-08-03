// Command snapfall is the Snapfall local daemon (PRD §6.3, Appendix B).
//
// Day-1 scope: apply the schema, load and validate the four agent manifests, start the
// supervisor with one dummy worker, and drain the transactional outbox onto the typed bus.
// Orchestrator, action broker, policy engine, treasury signer, and chain indexer are not
// here yet, they are the rest of workstream B.
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
	"sync"
	"syscall"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/agents"
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/budget"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/config"
	"github.com/gnanam1990/snapfall/daemon/internal/discovery"
	"github.com/gnanam1990/snapfall/daemon/internal/events"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/h3"
	"github.com/gnanam1990/snapfall/daemon/internal/logging"
	"github.com/gnanam1990/snapfall/daemon/internal/ownerapi"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/purchasing"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/supervisor"
	"github.com/gnanam1990/snapfall/daemon/internal/telegram"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"

	"github.com/ethereum/go-ethereum/common"
)

// discoveryFinder adapts discovery.Matcher to worker.Finder AT THE WIRING LAYER -
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
// indexer fixtures, the one chain this build's invoices and settlement observer read.
const arcTestnetChainID = uint64(5_042_002)

// The H3 payment sidecar's environment. It is read HERE and nowhere else in the daemon:
// AT-15's confinement is that the WIRING layer reads these values, internal/funding holds
// them, and no other package may even name them (pinned by
// funding.TestAT15_OnlyFundingAndWiringNameTheSidecarSecrets).
const (
	// sidecarTokenEnv carries the bearer token every /v1 route requires. It is a SPEND
	// capability, and because the sidecar's idempotent-replay lookup precedes its approval
	// check, it is also a READ capability over every already-paid resource. Never logged,
	// never returned, never written to the event log.
	sidecarTokenEnv = "SIDECAR_AUTH_TOKEN"
	// approvalSecretEnv carries the HMAC key the sidecar verifies the approvalToken with
	// (H3 §3.4). Holding it is not the power to authorize: funding's payment door derives
	// every signed field from an approval-minted Grant.
	approvalSecretEnv = "H2_APPROVAL_SECRET"
	// sidecarBaseURLEnv overrides the loopback sidecar origin. NOT a secret: it is logged.
	sidecarBaseURLEnv = "SIDECAR_BASE_URL"
	// sidecarTokenMinBytes mirrors the sidecar's own contract (H3 §1.2: a >=32-byte random
	// secret, compared constant-time). A shorter token would 401 every /v1 call, so the
	// lane is refused at boot with a message instead of at the first payment with a 401.
	sidecarTokenMinBytes = 32
	// sidecarHealthTimeout bounds the boot probe. /health needs no token and answers
	// immediately; this only stops a hung socket from stalling startup for the client's
	// full 30-second budget.
	sidecarHealthTimeout = 5 * time.Second
)

// The reclaim cadence (V6 §2.5). Both scans are over the folded ledger in memory, so the
// cost is the count of open holds, not a query.
const (
	// budgetSweepInterval reclaims holds whose approval window elapsed with no execution
	// claim. A minute is far below the 5-minute approval window, so a hold that dies while
	// the daemon is up is reclaimed inside the same demo.
	budgetSweepInterval = time.Minute
	// budgetReconcileInterval re-asks the sidecar about every claimed-but-unresolved hold.
	budgetReconcileInterval = 5 * time.Minute
	// reconcileDeadline is how long a post-sign hold may stay unresolved before the owner is
	// told. Comfortably longer than any demo, and deliberately NOT a release timeout:
	// nothing in the sidecar ever leaves RECONCILING, and a signed authorization is a bearer
	// instrument that may still settle (H3 §7 step 4).
	reconcileDeadline = 24 * time.Hour
)

// statusPoller reads one payment's durable state from the H3 sidecar. *h3.Client satisfies
// it; the interface exists so the reclaim paths name a READ capability rather than the whole
// payment client, and so a daemon with no sidecar can carry a plain nil.
type statusPoller interface {
	Status(ctx context.Context, paymentID string) (h3.StatusResult, error)
}

// bootLanes is what wireBrain hands back to the serve loop: the money-side wiring the
// supervised reclaimer needs, and the chain-budget alarm the owner's vault binding triggers.
// Every field is optional except led, which always exists (a ledger needs nothing but the
// store and a clock).
type bootLanes struct {
	// led is the reservation ledger, the daemon's only cumulative spend truth.
	led *budget.Ledger
	// payer is the sidecar status lane, nil when no H3 lane is wired. With nil, an
	// unresolved hold simply stands: no sidecar answer means no release is provable.
	payer statusPoller
	// chain is the §1.C budget divergence alarm, nil with no chain deployment configured.
	chain *chainBudget
}

// lane binds one signing client to one contract address as a funding.Submitter -
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

// chainBudget is V6 §1.C's alarm. Two independent 6.00 figures govern one job's spending:
// the chain's maxOperatingBudget (seed-demo's -budget, enforced by JobVault) and the daemon's
// policy.DemoPolicy().JobBudgetMicros (enforced by policy rule 1), and NOTHING reconciles
// them. V6 deliberately leaves them unreconciled: making the daemon budget per-job means
// changing Lifecycle.Policy's signature and choosing what an unknown job budget means, which
// is a policy-engine task with its own case table.
//
// What it does instead is make the divergence LOUD. A purchase the policy engine allows can
// still revert OverBudget on chain, and the difference between learning that at boot and
// learning it on camera is this alarm. Same posture as billing's projection divergence: raise
// the alert, pick no winner.
//
// It exists because this is the ONE place the daemon reads jobEconomics, whose third return
// value (maxBudget) used to be discarded here.
type chainBudget struct {
	reader   *chain.Client  // views only; no key material is used
	jobVault common.Address // the contract jobEconomics lives on
	led      *budget.Ledger // where the divergence record lands
	log      *slog.Logger

	mu sync.Mutex
	// noted keeps the alarm to once per vault job per process: check runs at boot for every
	// milestone with a chain identity AND at the moment the owner binds one, and a repeated
	// view call plus a repeated event would add noise, not information.
	noted map[string]bool
}

// check compares one job's two operating budgets and, when they disagree, warns and appends
// budget.chain_divergence.
//
// A nil receiver is a no-op: a daemon with no deployment configured has no chain figure to
// compare, and callers should not have to know that. Every read failure is a WARNING and
// never fatal (an unreadable chain budget is a missing alarm, not a reason to refuse to
// serve), and a zero maxBudget is silence rather than a divergence, because a job that is not
// on chain yet has no figure to disagree with.
func (c *chainBudget) check(ctx context.Context, jobID, vaultJobID string) {
	if c == nil || c.reader == nil || jobID == "" || vaultJobID == "" {
		return
	}
	c.mu.Lock()
	seen := c.noted[vaultJobID]
	c.mu.Unlock()
	if seen {
		return
	}

	id, err := chain.JobID32(vaultJobID)
	if err != nil {
		c.log.Warn("cannot compare the on-chain operating budget: the vault job id does not parse",
			"job", jobID, "vault_job_id", vaultJobID, "err", err)
		return
	}
	ret, err := c.reader.CallView(ctx, c.jobVault, chain.CalldataJobEconomics(id))
	if err != nil {
		c.log.Warn("cannot compare the on-chain operating budget: the jobEconomics view failed",
			"job", jobID, "vault_job_id", vaultJobID, "err", err)
		return
	}
	_, _, maxBudget, err := chain.DecodeJobEconomics(ret)
	if err != nil {
		c.log.Warn("cannot compare the on-chain operating budget: jobEconomics did not decode",
			"job", jobID, "vault_job_id", vaultJobID, "err", err)
		return
	}
	if maxBudget == nil || maxBudget.Sign() == 0 {
		return
	}
	if !maxBudget.IsInt64() {
		// Refusing to compare a truncated figure is the same fail-closed posture the money
		// paths take: a wrong comparison here would report a divergence that is not real, or
		// hide one that is.
		c.log.Warn("cannot compare the on-chain operating budget: it does not fit in int64 micros",
			"job", jobID, "vault_job_id", vaultJobID, "chain_budget_atomic", maxBudget.String())
		return
	}
	// USDC is 6dp, so an atomic on-chain figure IS micro-USDC: no scaling, and no float on a
	// money path anywhere in this codebase.
	chainMicros := maxBudget.Int64()
	daemonMicros := policy.DemoPolicy().JobBudgetMicros

	c.mu.Lock()
	c.noted[vaultJobID] = true
	c.mu.Unlock()

	if chainMicros == daemonMicros {
		return
	}
	c.log.Warn("BUDGET DIVERGENCE: this job's on-chain operating budget is not the daemon's policy budget, "+
		"so a purchase the policy engine allows can still revert OverBudget on chain (V6 §1.C: the two figures are "+
		"deliberately unreconciled, and deliberately loud)",
		"job", jobID, "vault_job_id", vaultJobID,
		"chain_budget_usdc", policy.FormatUSDC(chainMicros),
		"daemon_budget_usdc", policy.FormatUSDC(daemonMicros))
	if c.led == nil {
		return
	}
	if err := c.led.NoteChainDivergence(ctx, jobID, vaultJobID, chainMicros, daemonMicros); err != nil {
		c.log.Warn("could not record the budget divergence in the event log (the warning above stands)",
			"job", jobID, "vault_job_id", vaultJobID, "err", err)
	}
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

	telegramCfg, err := telegram.LoadConfig(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("telegram approvals: %w", err)
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

	// ── Brain: wired, recovered, SERVED (the G8 serve step, the promise from #4). ──
	// One Brain, one Recover, one escalation pass, then it serves: tasks bound to this
	// daemon's root context, spends routed through the real policy+approval Purchaser.
	br, life, lanes, err := wireBrain(ctx, log, st, dbPath, cfg.OrgID, deployment)
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
	if err := configureTelegramApprovals(telegramCfg, life, sup, log); err != nil {
		return err
	}

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
				// The owner binds the job to its on-chain identity at request time, the
				// explicit demo-phase producer for vault_job_id (on-chain job creation
				// automating this binding remains open, and is said so at standup).
				if err := br.BindVaultJob(wctx, ownerJob, ownerVault); err != nil {
					return fmt.Errorf("vault binding: %w", err)
				}
				log.Info("job bound to on-chain identity", "job", ownerJob, "vault_job_id", ownerVault)
				// V6 §1.C: the moment a job HAS a chain identity is the first moment its
				// on-chain operating budget and the daemon's policy budget can be compared.
				// A no-op with no deployment configured.
				lanes.chain.check(wctx, ownerJob, ownerVault)
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
	// endpoints, the surface the reject-and-adapt beat is decided through.
	api := ownerapi.New(life, st, log)
	// H2 §4.1: the owner-request invoice trigger routes through Brain's single site.
	api.Generate = func(gctx context.Context, jobID string) (billing.Record, error) {
		return br.GenerateInvoice(gctx, jobID, "owner-request")
	}
	// The customer surface, the settlement principal's half of the fall: per-job
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

	// The funding-observed advance trigger. HONEST STATE: never fired for real, no
	// deployment, no JobFunded rows, nothing writes vault ids, but it runs so funding
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
	// never had anything to observe, no deployment means no JobSettled rows and nothing
	// writes jobs' vault ids (the chain gap), but it runs so settlement day needs no
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

	// FR-PAY-007's serve half (V6 §2.5). The boot reclaim in wireBrain is not enough on its
	// own: a hold can expire, or a payment can stall, while the daemon is up. An
	// unreclaimable hold permanently shrinks a job's budget, so both paths get a supervised
	// owner, and NEITHER ever auto-releases a post-sign hold.
	if err := sup.Register(workerFunc{name: "budget-reclaimer", fn: func(wctx context.Context) error {
		sweep := time.NewTicker(budgetSweepInterval)
		defer sweep.Stop()
		reconcile := time.NewTicker(budgetReconcileInterval)
		defer reconcile.Stop()
		// noted keeps the stall escalation to ONCE per hold per process, so a hold nothing can
		// resolve does not re-page the owner every five minutes. A restart re-raises it on
		// purpose: every boot re-asks a money question nobody has answered yet.
		noted := make(map[string]bool)
		for {
			select {
			case <-wctx.Done():
				return nil
			case <-sweep.C:
				// Path 1: expiry-provable holds only. A hold carrying the lifecycle's
				// payment.executing claim is never touched here, whatever its expiry says.
				if n, err := lanes.led.Sweep(wctx, time.Now()); err != nil {
					log.Warn("budget sweep failed; the holds stand and the next tick retries", "err", err)
				} else if n > 0 {
					log.Info("released expired-unattempted budget holds", "count", n)
				}
			case <-reconcile.C:
				// Path 2, then path 3 over whatever path 2 could not resolve.
				states := reconcileUnresolved(wctx, lanes.led, lanes.payer, log)
				for _, h := range lanes.led.StalledSince(reconcileDeadline, time.Now()) {
					if noted[h.RequestID] {
						continue
					}
					// The state the poll just observed, so the escalation record does not
					// re-ask the sidecar. "" means no answer was available at all.
					state := states[h.RequestID]
					if state == "" {
						state = "UNKNOWN"
					}
					if err := lanes.led.NoteReconcileStalled(wctx, h, state); err != nil {
						log.Warn("could not record a stalled reconcile; the hold stands and the next tick retries",
							"request", h.RequestID, "payment_id", h.PaymentID, "err", err)
						continue
					}
					noted[h.RequestID] = true
					log.Warn("a post-sign budget hold has outlived the reconcile deadline and needs an OWNER decision: "+
						"it is never auto-released, because the signed authorization is a bearer instrument that may still settle",
						"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "state", state,
						"held_usdc", policy.FormatUSDC(h.ReserveMicros),
						"held_since", h.ReservedAt.UTC().Format(time.RFC3339))
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
	// shield, wait for those goroutines before closing the store under them.
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

func configureTelegramApprovals(cfg telegram.Config, life *approval.Lifecycle, sup *supervisor.Supervisor, log *slog.Logger) error {
	if !cfg.Enabled() {
		return nil
	}
	notifier := telegram.New(cfg, log)
	previousPending := life.Pending
	life.Pending = func(req approval.Request) {
		if previousPending != nil {
			previousPending(req)
		}
		notifier.Enqueue(req)
	}
	for _, req := range life.PendingRequests() {
		notifier.Enqueue(req)
	}
	if err := sup.Register(notifier); err != nil {
		return fmt.Errorf("register telegram approvals: %w", err)
	}
	log.Info("telegram approval mirror enabled",
		"dashboard_url", cfg.DashboardURL, "recovered_pending", len(life.PendingRequests()))
	return nil
}

// wireBrain is THE single Brain wiring point in the daemon (pinned by
// TestMain_SingleBrainWiringSite): one Brain, one Recover, one escalation pass, then
// serve. Constructing a second Brain over the same store would race two replays of the
// same event log, the double-recovery hazard from #4.
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

func wireBrain(ctx context.Context, log *slog.Logger, st *store.Store, dbPath, orgID, deployment string) (*brain.Brain, *approval.Lifecycle, *bootLanes, error) {
	mem, err := brain.NewMemoryStore(filepath.Join(filepath.Dir(dbPath), "memory"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening brain memory store: %w", err)
	}
	fund := funding.New()
	br := brain.New(log, st, mem, fund)
	// SNAPFALL_SCOPER_MODEL unset ⇒ deterministic StubScoper (the default the demo/spine rely
	// on); set ⇒ a local Ollama scoper that still falls back to the stub on any failure.
	br.SetScoper(brain.NewScoper(log))
	// The G8 adaptive DD worker with its scripted source plan: the \$0.04 profile primary
	// (auto-approves under DemoPolicy) with the \$0.06 benchmark as the cheaper fallback.
	// G10: the DD worker DISCOVERS its sources by description, no merchant or resource
	// name reaches it (the scripted plan type no longer exists; discovery is the only
	// path the binary can run). Needs are a SLICE in demo-script order: the profile
	// ($0.04, auto-approves, the 0:45 beat) before the market need ($4.00 escalates,
	// a cost reason re-queries discovery for the $0.06 benchmark, the 1:10 beats).
	dd := worker.NewDiscoveryDD(worker.StubCompliance{},
		discoveryFinder{discovery.NewMatcher(discovery.V2StandIn())},
		[]string{discovery.DemoNeedProfile, discovery.DemoNeedMarket}, 1)
	if err := br.RegisterWorker(dd); err != nil {
		return nil, nil, nil, err
	}
	if err := br.RegisterWorker(worker.NewBuildMonitor(worker.GitChecklistSource{})); err != nil {
		return nil, nil, nil, err
	}
	if err := br.RegisterQAWorker(qa.Worker{}); err != nil {
		return nil, nil, nil, err
	}

	// G11 kill switch: replayed from the event log, gating intake, dispatch, and payment.
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		return nil, nil, nil, err
	}
	br.SetFreeze(reg, orgID)

	// ── V6: the reservation ledger, built BEFORE the lifecycle hooks and before
	//    SetPurchaser, because wiring Spend is the line that makes policy rule 1 (job
	//    budget) and rule 3 (daily cap) LIVE for the first time: until now they evaluated
	//    against a constant zero SpendState, so neither could ever deny. It is a fold of the
	//    event log, not a table, so it needs nothing but the store and a clock. ──
	led := budget.New(st, time.Now)

	// The REAL Purchaser: policy -> approval -> (freeze-gated) execution -> H3 payment.
	// Every decision and gate is genuine, and the money hold under them is now real too.
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = led.Spend
	// One critical section per job, covering the spend read, the evaluation and the hold:
	// two concurrent 4.00 intents against a 6.00 job budget must not both pass rule 1 by
	// reading the same unheld state.
	life.JobGate = led.JobGate
	// Both scopes: rule 1 sums one job, rule 3 sums the whole org. Lock order is org then job,
	// documented on Lifecycle.OrgGate. Without this the org-wide daily cap is racy across jobs.
	life.OrgGate = led.OrgGate
	// Hold at intake on any non-Deny outcome (V6 §1.A), durable BEFORE the request is
	// visible. A hold with no payment.executing claim is therefore provably unattempted,
	// which is the only thing that makes the expiry sweeper safe.
	life.Reserve = reserveHook(led)
	life.Freeze = reg
	// The other half of the sweeper's safety proof, and the ONLY thing that makes releasing an
	// expired hold sound rather than probably-sound (review finding, main.go:578 P0): the
	// durable payment.executing claim is appended AFTER the lifecycle's expiry check passes, so
	// the sweeper needs the executor's live view too. Wired here because the ledger deliberately
	// does not import approval.
	led.Claimed = executionClaimed(life)

	// The H3 payment lane, wired onto Funding (the daemon's only holder of a spend
	// capability) BEFORE the Purchaser exists, because the Purchaser's first act on a
	// purchase is to ask Funding for a quote (H3 §7 step 1).
	payer := wirePayer(ctx, log, fund)

	br.SetPurchaser(purchasing.New(life, st, fund, led, purchasing.RealClock{}, orgID, 5*time.Minute))

	// G12 Billing: the read-side invoice formatter over the shared store's chain rows
	// (arc-testnet chainId 5042002, deployments/arc-testnet.json). Brain alone holds the
	// pointer; GenerateInvoice is its single pinned invocation site.
	br.SetBilling(billing.New(st, arcTestnetChainID, nil))

	// The advance's human-authorized path (the snap's daemon half): proposals enter
	// the approval lifecycle pre-marked for the owner, execution flows through the
	// same Grant gates as payments, and Funding stops honestly at advance.pending_chain.
	adv := advancing.New(life, st, fund, log, orgID, 10*time.Minute)
	br.SetAdvanceFlow(adv)

	// chainBudgets stays nil unless a chain READER is wired below: with no view client there
	// is no on-chain figure to compare, and an alarm that cannot read is not an alarm.
	var chainBudgets *chainBudget

	// The chain lanes, wired only when a deployment config is given. POSTURE, stated:
	// a configured deployment with a MISSING key wires no lane, and the affected flow
	// stops LOUDLY at *.pending_chain (a labeled pending record, never a fabricated
	// success), the keys are env-only, format-validated, and never logged. The
	// treasury key signs requestAdvance; the customer key signs acceptDelivery
	// (SC-JV-005, daemon-custodial for the demo, stated openly).
	if deployment != "" {
		dep, err := chaincfg.Load(deployment, os.LookupEnv)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("chain deployment: %w", err)
		}
		fpAddr := common.HexToAddress(dep.Contracts.FloatPool.Address)
		jvAddr := common.HexToAddress(dep.Contracts.JobVault.Address)
		var advanceLane, settleLane funding.Submitter
		var reader *chain.Client
		var operator common.Address
		if treasury, err := chain.NewFromEnv("TREASURY_PRIVATE_KEY", dep.Network.RPCURL, dep.Network.ChainID); err != nil {
			log.Warn("treasury chain lane NOT wired, advances stop at advance.pending_chain", "reason", err)
		} else {
			advanceLane, reader = lane{treasury, fpAddr}, treasury
			operator = treasury.Address()
			log.Info("treasury chain lane wired", "signer", treasury.Address().Hex(), "floatPool", fpAddr.Hex())
		}
		if customer, err := chain.NewFromEnv("SNAPFALL_CUSTOMER_PRIVATE_KEY", dep.Network.RPCURL, dep.Network.ChainID); err != nil {
			log.Warn("customer chain lane NOT wired, settlements stop at settlement.pending_chain", "reason", err)
		} else {
			settleLane = lane{customer, jvAddr}
			if reader == nil {
				reader = customer
			}
			log.Info("customer chain lane wired (demo-custodial)", "signer", customer.Address().Hex(), "jobVault", jvAddr.Hex())
		}
		fund.SetChain(advanceLane, settleLane)
		if reader != nil {
			// V6 §1.C: the same view this block already reads for the quote also returns
			// maxOperatingBudget, which used to be discarded here. Nothing reconciles it with
			// policy.DemoPolicy().JobBudgetMicros, so the alarm below is what converts a
			// silent OverBudget revert at demo time into a named warning plus a
			// budget.chain_divergence record.
			chainBudgets = &chainBudget{reader: reader, jobVault: jvAddr, led: led, log: log, noted: make(map[string]bool)}
			oracle := chain.Oracle{Reader: reader, FloatPool: fpAddr, JobVault: jvAddr, Org: operator}
			adv.SetOracle(oracle)
			// Read the org's current advance rate at intent creation so the amount the
			// owner approves matches what FloatPool draws (no hardcoded-50%-vs-55%
			// divergence when the rate has climbed).
			if operator != (common.Address{}) {
				adv.SetRateOracle(func(ctx context.Context) (uint16, bool) {
					bps, err := oracle.AdvanceRateBps(ctx)
					if err != nil || bps == 0 || bps > 10_000 {
						return 0, false
					}
					return uint16(bps), true
				})
			}
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
	// shutdown, approved-but-unexecuted advances are NEVER auto-executed on boot
	// (AT-09's crash posture); the owner is told and re-proposes.
	if err := life.Recover(ctx); err != nil {
		return nil, nil, nil, err
	}
	if n, err := adv.EscalateInterrupted(ctx); err != nil {
		return nil, nil, nil, err
	} else if n > 0 {
		log.Warn("advances interrupted by restart escalated to the owner", "count", n)
	}

	// ── V6 boot reclaim, and the ORDER is the contract. The reservation ledger folds AFTER
	//    Lifecycle.Recover: the lifecycle owns request state, the ledger only owns money held
	//    against it, and the fold reads the lifecycle's payment.executing claims to tell a
	//    hold that was never attempted from one that may already be signed. ──
	if err := led.Recover(ctx); err != nil {
		return nil, nil, nil, err
	}
	// Path 1 (FR-PAY-007's missing half): a hold whose approval window elapsed with NO
	// execution claim can never be paid (Execute refuses an expired intent unconditionally),
	// so it is PROVABLY dead and releasing it needs no external check. This is the case an
	// event-triggered release would leak forever, because a request that expires with nobody
	// waiting on it is never marked expired at all.
	if n, err := led.Sweep(ctx, time.Now()); err != nil {
		return nil, nil, nil, err
	} else if n > 0 {
		log.Info("released expired-unattempted budget holds at boot", "count", n)
	}
	// Path 2: every hold that DOES carry a claim may or may not have been signed, so the
	// sidecar's durable record decides: never a guess, and never a fabricated release.
	reconcileUnresolved(ctx, led, payer, log)

	// Serve pin 2: task lifetimes bound to the daemon root, SIGTERM wakes blocked tasks
	// and refuses new dispatches; in-flight executions complete past their claim.
	br.SetRootContext(ctx)

	// Serve pins 1+3: exactly one Recover (guarded in Brain too), then interrupted tasks
	// escalate, a restart after clean shutdown and after a crash are the same case.
	if err := br.Recover(); err != nil {
		return nil, nil, nil, err
	}
	if err := br.EscalateInterruptedTasks(ctx); err != nil {
		return nil, nil, nil, err
	}
	log.Info("brain serving", "jobs_recovered", br.JobCount())

	// V6 §1.C at boot, over every job that already HAS a chain identity. Milestones are the
	// only jobs whose vault ids the daemon can enumerate (Brain's in-memory job state carries
	// none), so the one-shot owner flow checks its own at bind time instead. Read AFTER
	// br.Recover, so a restart compares the jobs it actually recovered.
	if chainBudgets != nil {
		if milestones, err := br.Milestones(); err != nil {
			log.Warn("could not enumerate milestone jobs to compare on-chain operating budgets", "err", err)
		} else {
			for _, m := range milestones {
				chainBudgets.check(ctx, m.JobID, m.VaultJobID)
			}
		}
	}
	return br, life, &bootLanes{led: led, payer: payer, chain: chainBudgets}, nil
}

// errCeilingAboveAmount refuses the one intent shape whose hold would misrepresent what the
// policy engine actually tested.
var errCeilingAboveAmount = errors.New("max-amount-above-amount: the reserve ceiling exceeds the evaluated amount; " +
	"the job-budget rule is fed AmountMicros only (approval/lifecycle.go's Evaluate call), so a wider ceiling would pass " +
	"rule 1 at the smaller figure and then hold the larger one. Widening the ceiling is a policy-engine change, not a " +
	"wiring change")

// reserveMicros is the figure held against the budget: H3 §7 step 2's intent.maxAmount.
//
// INVARIANT (V6 §1.B): MaxAmountMicros equals AmountMicros, or is zero meaning "unset, use
// the amount". Both shapes hold the EVALUATED amount, which is the only figure
// policy.Evaluate ever saw. A ceiling ABOVE the amount is refused here rather than silently
// reserved, because the reserve would then exceed what rule 1 approved. A ceiling BELOW the
// amount still holds the amount: funding's payment door refuses that intent pre-sign (the
// ceiling is hash-bound and must equal the amount), and under-reserving in the meantime would
// let a second intent pass rule 1 on budget this one has already spoken for.
func reserveMicros(in approval.Intent) (int64, error) {
	if in.AmountMicros <= 0 {
		return 0, fmt.Errorf("refusing to hold budget for a non-positive amount (%d micros): a hold that is not a "+
			"positive figure cannot constrain anything", in.AmountMicros)
	}
	if in.MaxAmountMicros > in.AmountMicros {
		return 0, fmt.Errorf("%w: ceiling %d micros, evaluated amount %d micros",
			errCeilingAboveAmount, in.MaxAmountMicros, in.AmountMicros)
	}
	return in.AmountMicros, nil
}

// reserveHook is the Intent-to-Hold translation, and it lives in the WIRING layer because
// package budget deliberately imports neither approval nor funding (its package doc says so):
// the ledger stays foldable and testable against nothing but a store and a clock, so the
// crossing belongs here.
//
// It carries the PRECOMPUTED payment id, which is the whole reason a crashed payment is
// recoverable. h3.PaymentID is a pure function of the wire hash and the nonce, and funding
// derives the same id from the same grant intent at pay time, so the id persisted in
// budget.reserved is provably the one the sidecar will use. Persisting it (rather than
// recomputing it during recovery) also survives a future change to the wire
// canonicalization, which a recompute would silently retarget at an id nobody ever paid.
//
// Fails CLOSED: an error here aborts intake with no hold and no visible request.
func reserveHook(led *budget.Ledger) func(context.Context, approval.Intent, string) error {
	return func(ctx context.Context, in approval.Intent, requestID string) error {
		micros, err := reserveMicros(in)
		if err != nil {
			return err
		}
		return led.Reserve(ctx, budget.Hold{
			RequestID:     requestID,
			IntentID:      in.IntentID,
			JobID:         in.JobID,
			OrgID:         in.OrgID,
			ReserveMicros: micros,
			// The intent arrives with PolicyVersion already stamped by Submit, which is what
			// makes this wire hash the one funding will recompute from the grant.
			PaymentID: h3.PaymentID(approval.WireHash(in), in.Nonce),
			Nonce:     in.Nonce,
			// BOTH conventions are recorded, because they mean different things: Merchant is
			// the HOST the policy allowlist matched, PayTo is the x402 payee ADDRESS the wire
			// will carry.
			Merchant:  in.Merchant,
			PayTo:     in.PayTo,
			ExpiresAt: in.ExpiresAt,
		})
	}
}

// executionClaimed is the reservation ledger's in-process claim oracle, and it closes the one
// window the durable claim cannot (review finding, main.go:578 P0): a payment admitted
// immediately before its approval expires could lose its hold to the expiry sweeper and still
// go on to sign, because the sweeper reads payment.executing, which the lifecycle appends only
// AFTER its expiry check has already passed.
//
// The lifecycle decides expiry and sets the request's executed flag in ONE critical section
// under its own mutex, and Snapshot reads that flag under the same mutex. So consulting it
// after the sweeper has established now >= ExpiresAt leaves exactly two interleavings, and both
// are safe: an execution whose critical section ran first is visible here and its hold is
// retained, and an execution whose section runs after this read sees a clock at or past
// ExpiresAt and refuses unconditionally. Both sides read time.Now, which is the precondition
// the Claimed hook documents.
//
// LOCK ORDER PRESERVED: gates -> ledger mutex -> (nothing). The sweeper calls this while
// holding NO ledger lock, so the ledger mutex is never held while acquiring the lifecycle's,
// and the lifecycle never holds its own mutex across a call into the ledger (Spend, Reserve and
// the executor all run outside it). Intake's documented order, org gate then job gate then the
// ledger, is untouched: this adds no gate acquisition anywhere.
//
// A request the lifecycle does not know reports NOT claimed, and that is correct rather than
// merely convenient: Execute refuses an unknown request outright, so such a hold can never
// acquire a claim, and it is precisely the reclaimable artifact the reserve-before-append
// ordering was chosen to produce (a hold whose approval.requested append failed).
func executionClaimed(life *approval.Lifecycle) func(string) bool {
	return func(requestID string) bool {
		req, ok := life.Snapshot(requestID)
		if !ok {
			return false
		}
		return req.Executed
	}
}

// wirePayer wires the H3 payment lane onto Funding from the environment and returns the
// read-only status lane the reclaim paths poll (nil when no lane exists).
//
// POSTURE, the same one the chain lanes have and stated as plainly: a missing token, a
// missing approval secret, a token the sidecar would reject, an unparseable base URL or an
// unanswered /health wires NO lane, and every purchase then stops LOUDLY at
// purchase.pending_settlement (an approval consumed exactly once, no money moved, a labelled
// record) instead of failing mid-flight. The daemon boots either way. That is not just
// politeness to operators: it is what keeps every existing test running unchanged, since none
// of them sets a sidecar variable.
//
// The two secrets are read HERE and handed only to internal/funding (the approval key) and
// internal/h3 (the bearer token). Neither is ever logged: the log lines name the origin, the
// minimum length and the reason, and nothing else.
func wirePayer(ctx context.Context, log *slog.Logger, fund *funding.Agent) statusPoller {
	rawToken, haveToken := os.LookupEnv(sidecarTokenEnv)
	rawSecret, haveSecret := os.LookupEnv(approvalSecretEnv)
	token, secret := strings.TrimSpace(rawToken), strings.TrimSpace(rawSecret)

	switch {
	case !haveToken && !haveSecret:
		// The ordinary development posture, not a misconfiguration. Said once, at info.
		log.Info("H3 payment lane NOT wired (no "+sidecarTokenEnv+"): purchases run every policy and approval gate "+
			"and stop at purchase.pending_settlement", "hint", "set "+sidecarTokenEnv+" and "+approvalSecretEnv+" to enable it")
		return nil
	case token == "":
		log.Warn("H3 payment lane NOT wired: " + sidecarTokenEnv + " is missing or blank, and every /v1 route requires " +
			"the bearer token. Purchases stop at purchase.pending_settlement")
		return nil
	case len(token) < sidecarTokenMinBytes:
		log.Warn("H3 payment lane NOT wired: the bearer token is shorter than the sidecar's minimum, so every call would "+
			"return 401 UNAUTHENTICATED. Purchases stop at purchase.pending_settlement",
			"min_bytes", sidecarTokenMinBytes, "got_bytes", len(token))
		return nil
	case secret == "":
		log.Warn("H3 payment lane NOT wired: " + approvalSecretEnv + " is missing, so no approvalToken could be signed " +
			"and every pay would return 401 APPROVAL_TOKEN_INVALID. Purchases stop at purchase.pending_settlement")
		return nil
	}

	// An empty base URL is the loopback default inside the client (the sidecar binds
	// 127.0.0.1 only), so no default is duplicated here.
	cl := h3.New(strings.TrimSpace(os.Getenv(sidecarBaseURLEnv)), token, nil)
	hctx, cancel := context.WithTimeout(ctx, sidecarHealthTimeout)
	defer cancel()
	if err := cl.Health(hctx); err != nil {
		// /health needs no token, so a failure here is "no sidecar", not "bad credentials".
		log.Warn("H3 payment lane NOT wired: the sidecar did not answer /health. Purchases stop at "+
			"purchase.pending_settlement", "base_url", cl.BaseURL(), "reason", err)
		return nil
	}
	fund.SetPayer(cl, []byte(secret), int64(arcTestnetChainID))
	log.Info("H3 payment lane wired", "base_url", cl.BaseURL(), "chain_id", arcTestnetChainID)
	return cl
}

// reconcileUnresolved is V6 §2.5 path 2 and the crash table's answer for W2 through W6: for
// every hold carrying the lifecycle's payment.executing claim, ask the sidecar what actually
// happened, using the paymentId the RESERVATION persisted and never one read out of a
// response (the in-flight-lock branch answers with paymentId:null).
//
// Every rule errs toward keeping the hold:
//
//	PAYMENT_NOT_FOUND (the CODE, never the 404)  no durable record exists, so nothing was ever
//	                                            signed: release in full, pre-sign.
//	DELIVERED / SETTLED                         commit at the SIDECAR's amountPaid, which
//	                                            releases the remainder in the same event.
//	FAILED / EXPIRED                            nothing settled: release, NOT pre-sign.
//	SIGNED / SUBMITTED / RECONCILING            the outcome is unknown: the hold STANDS.
//	any transport or decode failure             the outcome is unknown: the hold STANDS.
//	ANY OTHER state, including ""               the vocabulary changed under a live hold: the
//	                                            hold STANDS and the owner is escalated.
//
// It returns the state each STILL-OPEN hold reported, keyed by request id, with "" for a hold
// nothing could answer for, so the stall escalation can name a state without polling again.
func reconcileUnresolved(ctx context.Context, led *budget.Ledger, poller statusPoller, log *slog.Logger) map[string]string {
	holds := led.Unresolved()
	if len(holds) == 0 {
		return nil
	}
	open := make(map[string]string, len(holds))

	if poller == nil {
		// Nothing can be resolved, and that does NOT license a release: these holds carry an
		// execution claim, so the expiry sweeper is forbidden to touch them, and releasing
		// money that may have moved because a sidecar is absent would be the fabricated
		// release this codebase refuses everywhere else.
		log.Warn("budget holds are unresolved and no H3 lane is wired to resolve them: they stand until a sidecar answers",
			"count", len(holds))
		for _, h := range holds {
			open[h.RequestID] = ""
		}
		return open
	}

	for _, h := range holds {
		res, err := poller.Status(ctx, h.PaymentID)
		if err != nil {
			var he *h3.Error
			if errors.As(err, &he) && he.Code == h3.CodePaymentNotFound {
				// W2 and W5 are the same recovery: the sidecar's pre-sign checks persist
				// nothing, so no record means no signature.
				if rerr := led.Release(ctx, h.RequestID,
					"the sidecar holds no record of this payment, so nothing was ever signed (its pre-sign checks run "+
						"before the write-ahead record)", he.Code, true); rerr != nil {
					log.Warn("could not release a provably unsigned hold; it stands and the next reconcile retries",
						"request", h.RequestID, "payment_id", h.PaymentID, "err", rerr)
					open[h.RequestID] = ""
					continue
				}
				log.Info("released a budget hold the sidecar never recorded",
					"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID,
					"released_usdc", policy.FormatUSDC(h.ReserveMicros))
				continue
			}
			log.Warn("could not resolve a claimed budget hold: it STANDS, because a signed authorization is a bearer "+
				"instrument that may still settle",
				"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "err", err)
			open[h.RequestID] = ""
			continue
		}

		switch {
		case h3.Committed(res.State):
			paid, perr := h3.ParseAtomicMicros(res.AmountPaid)
			if perr != nil {
				log.Warn("the sidecar reports a committed payment with an unreadable amountPaid: refusing to commit a "+
					"budget at a guessed figure, so the hold stands",
					"request", h.RequestID, "payment_id", h.PaymentID, "state", res.State, "err", perr)
				open[h.RequestID] = res.State
				continue
			}
			if cerr := led.Commit(ctx, h.RequestID, paid, h.PaymentID, receiptHashOf(res), res.State); cerr != nil {
				log.Warn("could not commit a recovered payment against the budget; the hold stands and the next "+
					"reconcile retries", "request", h.RequestID, "payment_id", h.PaymentID, "err", cerr)
				open[h.RequestID] = res.State
				continue
			}
			log.Info("committed a payment recovered from the sidecar's durable record",
				"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "state", res.State,
				"paid_usdc", policy.FormatUSDC(paid))
		case h3.Unresolved(res.State):
			// W3 and W6. Nothing in the sidecar ever leaves RECONCILING, so this can persist
			// indefinitely; the reconcile deadline escalates it to the owner and nothing
			// releases it automatically.
			log.Warn("a claimed budget hold is still unresolved at the sidecar: it stands and is never auto-released",
				"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "state", res.State)
			open[h.RequestID] = res.State
		case res.State == h3.StateFailed || res.State == h3.StateExpired:
			// The two states, and ONLY these two, that are a PROVEN terminal failure (review
			// finding, main.go:1166 P1): the sidecar's own durable record says nothing settled.
			// The release is NOT pre-sign (a key may well have been used), so the event records
			// that honestly, and `code` carries the terminal STATE because a status poll returns
			// no error code to carry.
			reason := fmt.Sprintf("the sidecar's durable record reports %s, so nothing settled (reason: %q)",
				res.State, res.Reason)
			if rerr := led.Release(ctx, h.RequestID, reason, res.State, false); rerr != nil {
				log.Warn("could not release the hold of a failed payment; it stands and the next reconcile retries",
					"request", h.RequestID, "payment_id", h.PaymentID, "state", res.State, "err", rerr)
				open[h.RequestID] = res.State
				continue
			}
			log.Info("released the budget hold of a payment the sidecar reports as not settled",
				"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "state", res.State,
				"released_usdc", policy.FormatUSDC(h.ReserveMicros))
		default:
			// A state this daemon's vocabulary does not contain, INCLUDING the empty string.
			// Releasing here would reopen budget behind a payment that may well have settled,
			// which is the bearer-instrument rule the whole reclaim design rests on: only a
			// PROVEN terminal failure (the case above) or a proven absence of any record
			// (PAYMENT_NOT_FOUND) licenses returning post-sign money to the budget. A sidecar
			// that grows a state, or a response that decoded into no state at all, proves
			// neither. So the hold PINS, visibly, on path 3's terms rather than silently.
			state := res.State
			if state == "" {
				state = "UNKNOWN"
			}
			log.Warn("the sidecar reports a payment state this daemon does not recognize: the budget hold STANDS and needs "+
				"an OWNER decision, because an unknown state is not proof that nothing settled",
				"request", h.RequestID, "job", h.JobID, "payment_id", h.PaymentID, "state", state,
				"held_usdc", policy.FormatUSDC(h.ReserveMicros))
			// Escalated exactly like a post-sign hold past the reconcile deadline, and without
			// waiting for it: an unrecognized state is already a fact only an owner can act on.
			// The record is deduped inside the ledger, so the reconcile ticker does not re-page.
			if nerr := led.NoteReconcileStalled(ctx, h, state); nerr != nil {
				log.Warn("could not record the unrecognized-state escalation; the hold stands and the next reconcile retries",
					"request", h.RequestID, "payment_id", h.PaymentID, "state", state, "err", nerr)
			}
			open[h.RequestID] = res.State
		}
	}
	return open
}

// receiptHashOf is the bytes32 join key for a payment recovered from a status poll. H3 emits
// no receiptHash of its own, so it is the AuthNonce (V6 §3.4): the value actually signed into
// the EIP-3009 authorization, and the key Billing needs the day JobVault.recordExpense is
// wired. The receipt's own value is preferred; the fallback recomputes it from the intent
// hash, which is deterministic per intent and therefore the same number.
func receiptHashOf(res h3.StatusResult) string {
	if res.Receipt != nil && res.Receipt.AuthNonce != "" {
		return res.Receipt.AuthNonce
	}
	if res.IntentHash != "" {
		return h3.AuthNonce(res.IntentHash)
	}
	return ""
}
