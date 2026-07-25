// Command seed-demo prepares one clean demo run (V12).
//
// It exists because the demo's most important beat is also its most fragile: at 0:30 the
// treasury draws a Float advance, and FloatPool refuses that advance unless the pool is deep
// enough for BOTH of its caps. Getting that wrong means CapExceeded on camera. So this command
// derives the requirement from the live chain position rather than from assumptions, tops the
// pool up only by the deficit, mints a fresh job id, funds the escrow, and then re-reads the
// chain before printing "ready".
//
// Three things are read, never assumed: the org's advance rate (the flywheel raises it, which
// raises the advance and the depth it needs), and the two outstanding balances. The caps are
// evaluated on the position AFTER issuance, so a pool that already carries exposure needs more
// depth than the new principal alone implies.
//
// Three keys, because the contracts treat three distinct parties: the operator draws the
// advance, only the designated customer may fund its own escrow (SC-JV-001), and the LP
// supplies pool liquidity. The LP must not be the operator, or the demo's opening claim of a
// treasury at 0.00 receiving outside capital is not true.
//
// It composes the primitives chainops already exposes rather than reimplementing them, and it
// is idempotent: re-running against an already-seeded pool submits no deposit.
//
//	seed-demo --dry-run                 plan only, no keys and no transactions
//	seed-demo --dry-run --org 0xABC...  plan for an org without holding any key at all
//	seed-demo                           seed for real (operator + customer + LP keys)
//	seed-demo --price 25 --pool-seed 150
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/chaincfg"
	"github.com/gnanam1990/snapfall/daemon/internal/demoseed"
)

const arcTestnetChainID = 5042002

// RunState is written to disk so every other process in the demo (daemon, dashboard, the
// operator running chainops by hand) agrees on which job this take is about.
type RunState struct {
	RunID          string `json:"runId"`
	JobID          string `json:"jobId"`
	CustomerAddr   string `json:"customer"`
	OperatorAddr   string `json:"operator"`
	PriceUSDC      string `json:"priceUsdc"`
	BudgetUSDC     string `json:"budgetUsdc"`
	AdvanceUSDC    string `json:"expectedAdvanceUsdc"`
	RateBps        uint64 `json:"advanceRateBps"`
	PoolTVLUSDC    string `json:"poolTvlUsdc"`
	RequiredTVL    string `json:"requiredPoolTvlUsdc"`
	ExplorerJobURL string `json:"explorerJobUrl"`
	SeededAt       string `json:"seededAt"`
}

func main() {
	deployment := flag.String("deployment", "deployments/arc-testnet.json", "chain deployment config")
	operatorKeyEnv := flag.String("operator-key-env", "TREASURY_PRIVATE_KEY", "env var holding the operator/treasury key")
	customerKeyEnv := flag.String("customer-key-env", "SNAPFALL_CUSTOMER_PRIVATE_KEY", "env var holding the external customer key")
	lpKeyEnv := flag.String("lp-key-env", "SNAPFALL_LP_PRIVATE_KEY", "env var holding the pool LP/funder key; MUST NOT be the operator treasury")
	price := flag.String("price", "25", "customer payment in USDC (PRD §15.2 demo job)")
	budget := flag.String("budget", "6", "max operating budget in USDC")
	poolSeed := flag.String("pool-seed", "150", "desired pool TVL in USDC; the exposure-cap floor wins if higher")
	statePath := flag.String("state", ".demo/current.json", "where to record this run's job id")
	runID := flag.String("run-id", "", "run identifier (default: a fresh timestamp, which is what keeps takes collision-free)")
	org := flag.String("org", "", "org/treasury address to price the advance for; lets --dry-run run without any key")
	dryRun := flag.Bool("dry-run", false, "print the plan; submit nothing (keyless when --org is given)")
	flag.Parse()

	if err := run(*deployment, *operatorKeyEnv, *customerKeyEnv, *lpKeyEnv, *price, *budget, *poolSeed, *statePath, *runID, *org, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "seed-demo:", err)
		os.Exit(1)
	}
}

func run(deploymentPath, operatorKeyEnv, customerKeyEnv, lpKeyEnv, priceStr, budgetStr, poolSeedStr, statePath, runID, orgFlag string, dryRun bool) error {
	priceMicros, err := demoseed.ParseUSDC(priceStr)
	if err != nil {
		return fmt.Errorf("price: %w", err)
	}
	budgetMicros, err := demoseed.ParseUSDC(budgetStr)
	if err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	desiredTVL, err := demoseed.ParseUSDC(poolSeedStr)
	if err != nil {
		return fmt.Errorf("pool-seed: %w", err)
	}

	dep, err := chaincfg.Load(deploymentPath, os.LookupEnv)
	if err != nil {
		return err
	}
	if dep.Network.ChainID != arcTestnetChainID {
		return fmt.Errorf("deployment chain id %d is not Arc testnet %d", dep.Network.ChainID, arcTestnetChainID)
	}
	explorer := strings.TrimSuffix(dep.Network.ExplorerURL, "/")

	customerAddr := dep.WalletAddresses["externalCustomer"]
	if customerAddr == "" {
		return fmt.Errorf("deployment has no externalCustomer wallet address (set %s)", dep.FundedWallets["externalCustomer"])
	}

	fp := common.HexToAddress(dep.Contracts.FloatPool.Address)
	jv := common.HexToAddress(dep.Contracts.JobVault.Address)
	usdc := common.HexToAddress(dep.Contracts.USDC.Address)
	ctx := context.Background()

	// Reads never touch a key, but chain.NewFromEnv wants one. A dry run with --org given can
	// therefore price the advance with no key at all; without --org we need the key only to
	// learn which org address to price for.
	var (
		operator *chain.Client
		orgAddr  common.Address
	)
	// HexToAddress silently zero-pads anything it cannot parse, so a typo would plan for
	// 0x000...0 and report a floor for the wrong org. Reject it up front instead.
	if orgFlag != "" && !common.IsHexAddress(orgFlag) {
		return fmt.Errorf("--org %q is not a 20-byte hex address", orgFlag)
	}
	if dryRun && orgFlag != "" {
		orgAddr = common.HexToAddress(orgFlag)
		operator, err = chain.NewReadOnly(dep.Network.RPCURL, dep.Network.ChainID)
		if err != nil {
			return err
		}
	} else {
		operator, err = chain.NewFromEnv(operatorKeyEnv, dep.Network.RPCURL, dep.Network.ChainID)
		if err != nil {
			return fmt.Errorf("operator key: %w (or pass --org ADDRESS with --dry-run to plan without a key)", err)
		}
		orgAddr = operator.Address()
		if orgFlag != "" && !strings.EqualFold(orgFlag, orgAddr.Hex()) {
			return fmt.Errorf("--org %s does not match the key's address %s", orgFlag, orgAddr.Hex())
		}
	}

	// ── read the live position: nothing here is assumed ──
	// The rate is chain-authoritative, and so is the exposure the pool already carries. Both
	// FloatPool caps are checked against the position AFTER issuance, so planning from the new
	// principal alone can print "ready" and still revert CapExceeded (review finding, PR #41).
	// One block for all four reads. The floor is a function of the rate, pool assets and both
	// outstanding balances TOGETHER, so reading them at "latest" four times over can compose a
	// position that never existed on chain: another org drawing mid-sequence yields a floor that
	// is arithmetically correct for nothing, and the command prints "ready" anyway
	// (review: PR #41).
	at, err := operator.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("read block number: %w", err)
	}
	snap, err := readPosition(ctx, operator, fp, orgAddr, at)
	if err != nil {
		return err
	}
	rateBps, currentTVL := snap.rateBps, snap.totalAssets
	exposure := snap.exposure()

	plan, err := demoseed.BuildPlan(priceMicros, rateBps, currentTVL, desiredTVL, exposure)
	if err != nil {
		return err
	}

	if runID == "" {
		runID, err = newRunID()
		if err != nil {
			return err
		}
	}
	jobID := demoseed.JobID(runID)
	jobHex := "0x" + hex.EncodeToString(jobID[:])

	fmt.Printf("Snapfall demo seed · Arc testnet (chain %d)\n", dep.Network.ChainID)
	fmt.Printf("  run id            %s\n", runID)
	fmt.Printf("  job id            %s\n", jobHex)
	fmt.Printf("  operator (org)    %s\n", orgAddr.Hex())
	fmt.Printf("  customer          %s\n", customerAddr)
	fmt.Printf("  job price         %s USDC   (budget %s)\n", demoseed.FormatUSDC(plan.PriceMicros), demoseed.FormatUSDC(budgetMicros))
	fmt.Printf("  live rate         %d bps -> advance %s USDC\n", plan.RateBps, demoseed.FormatUSDC(plan.AdvanceMicros))
	fmt.Printf("  pool TVL          %s USDC now\n", demoseed.FormatUSDC(plan.CurrentTVLMicros))
	fmt.Printf("  already lent      %s USDC this org, %s USDC pool-wide\n",
		demoseed.FormatUSDC(plan.Exposure.OrgOutstandingMicros), demoseed.FormatUSDC(plan.Exposure.TotalOutstandingMicros))
	fmt.Printf("  cap floor         %s USDC   (%s cap binds, on existing + new exposure)\n",
		demoseed.FormatUSDC(plan.RequiredTVLMicros), plan.BindingCap)
	fmt.Printf("  seed target       %s USDC\n", demoseed.FormatUSDC(plan.TargetTVLMicros))
	if plan.SeedAlreadySufficient() {
		fmt.Printf("  deposit           none — the pool is already deep enough\n")
	} else {
		fmt.Printf("  deposit           %s USDC\n", demoseed.FormatUSDC(plan.DepositMicros))
	}

	if dryRun {
		fmt.Println("\ndry run: nothing was submitted.")
		return nil
	}

	customer, err := chain.NewFromEnv(customerKeyEnv, dep.Network.RPCURL, dep.Network.ChainID)
	if err != nil {
		return fmt.Errorf("customer key: %w (the customer must fund its own escrow — SC-JV-001)", err)
	}
	if !strings.EqualFold(customer.Address().Hex(), customerAddr) {
		return fmt.Errorf("%s holds %s but the deployment's externalCustomer is %s; SC-JV-001 lets only the designated customer fund",
			customerKeyEnv, customer.Address().Hex(), customerAddr)
	}

	fmt.Println()
	show := func(label string, r chain.Receipt, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if r.Reverted {
			return fmt.Errorf("%s REVERTED (mined and failed) — tx %s/tx/%s", label, explorer, r.TxHash)
		}
		fmt.Printf("  %-16s ok    %s/tx/%s\n", label, explorer, r.TxHash)
		return nil
	}

	// ── 1. top the pool up by the deficit only (idempotent) ──
	//
	// The LP is a THIRD party, not the operator. The demo's opening claim is a treasury at
	// exactly 0.00 that gets its first working capital from someone else's pool; if the operator
	// key funded the pool it would be lending itself money, and the dashboard would open on
	// whatever that key had left over instead of zero. So the deposit uses its own key and the
	// shares it mints belong to that LP.
	if !plan.SeedAlreadySufficient() {
		lp, err := chain.NewFromEnv(lpKeyEnv, dep.Network.RPCURL, dep.Network.ChainID)
		if err != nil {
			return fmt.Errorf("LP key: %w (the pool's liquidity must come from an LP, not the operator treasury; set %s)", err, lpKeyEnv)
		}
		if strings.EqualFold(lp.Address().Hex(), orgAddr.Hex()) {
			return fmt.Errorf("%s holds the operator address %s; the LP must be a separate wallet or the demo opens on a treasury that funded its own pool",
				lpKeyEnv, orgAddr.Hex())
		}
		fmt.Printf("  %-16s %s\n", "LP (funder)", lp.Address().Hex())
		r, err := lp.Submit(ctx, usdc, chain.CalldataApprove(fp, plan.DepositMicros))
		if err := show("pool approve", r, err); err != nil {
			return err
		}
		r, err = lp.Submit(ctx, fp, chain.CalldataDeposit(plan.DepositMicros, lp.Address()))
		if err := show("pool deposit", r, err); err != nil {
			return err
		}
	}

	// ── 2. create the job (operator) ──
	r, err := operator.Submit(ctx, jv, chain.CalldataCreateJob(
		jobID, common.HexToAddress(customerAddr), orgAddr,
		plan.PriceMicros, budgetMicros, demoseed.JobID("terms/"+runID), deadline(),
	))
	if err := show("create-job", r, err); err != nil {
		return err
	}

	// ── 3. fund the escrow (customer key — SC-JV-001) ──
	r, err = customer.Submit(ctx, usdc, chain.CalldataApprove(jv, plan.PriceMicros))
	if err := show("escrow approve", r, err); err != nil {
		return err
	}
	r, err = customer.Submit(ctx, jv, chain.CalldataFund(jobID))
	if err := show("fund-job", r, err); err != nil {
		return err
	}

	// ── 4. verify the chain agrees before claiming the demo is ready ──
	fmt.Println()
	observedTVL, err := verify(ctx, operator, jv, fp, orgAddr, jobID, plan)
	if err != nil {
		return err
	}

	state := RunState{
		RunID:          runID,
		JobID:          jobHex,
		CustomerAddr:   customerAddr,
		OperatorAddr:   orgAddr.Hex(),
		PriceUSDC:      demoseed.FormatUSDC(plan.PriceMicros),
		BudgetUSDC:     demoseed.FormatUSDC(budgetMicros),
		AdvanceUSDC:    demoseed.FormatUSDC(plan.AdvanceMicros),
		RateBps:        plan.RateBps,
		PoolTVLUSDC:    demoseed.FormatUSDC(observedTVL),
		RequiredTVL:    demoseed.FormatUSDC(plan.RequiredTVLMicros),
		ExplorerJobURL: fmt.Sprintf("%s/address/%s", explorer, dep.Contracts.JobVault.Address),
		SeededAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeState(statePath, state); err != nil {
		return err
	}

	fmt.Printf("\nready. job %s is Funded with %s USDC escrowed; the snap will draw %s USDC.\n",
		jobHex, state.PriceUSDC, state.AdvanceUSDC)
	fmt.Printf("run id recorded in %s\n", statePath)
	return nil
}

// verify re-reads the chain so a green "ready" cannot be a hopeful assumption. It returns the
// OBSERVED pool TVL, because the run state should record what the chain holds rather than what
// the plan aimed for (review: PR #41).
func verify(ctx context.Context, c *chain.Client, jv, fp, org common.Address, jobID [32]byte, plan demoseed.Plan) (*big.Int, error) {
	// Pinned to one block for the same reason the plan is: the pool values are compared against
	// each other, so they have to describe a single position.
	at, err := c.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify block number: %w", err)
	}
	ret, err := c.CallViewAt(ctx, jv, chain.CalldataJobStatus(jobID), at)
	if err != nil {
		return nil, fmt.Errorf("verify jobStatus: %w", err)
	}
	status, err := chain.DecodeJobStatus(ret)
	if err != nil {
		return nil, err
	}
	const funded uint8 = 1
	if status != funded {
		return nil, fmt.Errorf("verify: job status is %d, want %d (Funded) — the advance requires a Funded job (SC-FP-001)", status, funded)
	}
	fmt.Printf("  %-16s ok    status=Funded\n", "verify job")

	ret, err = c.CallViewAt(ctx, fp, chain.CalldataOpenAdvanceOf(jobID), at)
	if err != nil {
		return nil, fmt.Errorf("verify openAdvanceOf: %w", err)
	}
	_, _, open, err := chain.DecodeOpenAdvance(ret)
	if err != nil {
		return nil, err
	}
	if open {
		return nil, fmt.Errorf("verify: this job already has an open advance; a fresh run id should have prevented that")
	}
	fmt.Printf("  %-16s ok    no advance drawn yet\n", "verify advance")

	// Re-read the whole position, not just TVL. The floor depends on exposure, and between the
	// plan and now another org could have drawn against the same pool, so recomputing from fresh
	// reads is what makes "ready" a claim about the chain rather than about the plan.
	snap, err := readPosition(ctx, c, fp, org, at)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	floor := demoseed.RequiredPoolTVL(plan.AdvanceMicros, snap.exposure())
	if snap.totalAssets.Cmp(floor) < 0 {
		return nil, fmt.Errorf("verify: pool holds %s USDC but an advance of %s on top of %s org / %s pool-wide outstanding needs at least %s, so the snap would revert CapExceeded",
			demoseed.FormatUSDC(snap.totalAssets), demoseed.FormatUSDC(plan.AdvanceMicros),
			demoseed.FormatUSDC(snap.orgOutstanding), demoseed.FormatUSDC(snap.totalOutstanding), demoseed.FormatUSDC(floor))
	}
	fmt.Printf("  %-16s ok    TVL %s >= floor %s (org %s / pool %s already lent, block %s)\n", "verify pool",
		demoseed.FormatUSDC(snap.totalAssets), demoseed.FormatUSDC(floor),
		demoseed.FormatUSDC(snap.orgOutstanding), demoseed.FormatUSDC(snap.totalOutstanding), at)
	return snap.totalAssets, nil
}

// position is every value the cap arithmetic depends on, all read at ONE block.
type position struct {
	rateBps          uint64
	totalAssets      *big.Int
	orgOutstanding   *big.Int
	totalOutstanding *big.Int
}

func (p position) exposure() demoseed.Exposure {
	return demoseed.Exposure{
		OrgOutstandingMicros:   p.orgOutstanding,
		TotalOutstandingMicros: p.totalOutstanding,
	}
}

// readPosition reads the rate, pool assets and both outstanding balances against a single
// block tag, so the floor is computed from a position that actually existed on chain.
func readPosition(ctx context.Context, c *chain.Client, fp, org common.Address, at string) (position, error) {
	rate, err := readUintAt(ctx, c, fp, chain.CalldataAdvanceRate(org), at)
	if err != nil {
		return position{}, fmt.Errorf("read advanceRate: %w", err)
	}
	if !rate.IsUint64() {
		return position{}, fmt.Errorf("advanceRate returned an implausible value %s", rate)
	}
	assets, err := readUintAt(ctx, c, fp, chain.CalldataTotalAssets(), at)
	if err != nil {
		return position{}, fmt.Errorf("read totalAssets: %w", err)
	}
	orgOut, err := readUintAt(ctx, c, fp, chain.CalldataOrgOutstanding(org), at)
	if err != nil {
		return position{}, fmt.Errorf("read orgOutstanding: %w", err)
	}
	totalOut, err := readUintAt(ctx, c, fp, chain.CalldataTotalOutstanding(), at)
	if err != nil {
		return position{}, fmt.Errorf("read totalOutstanding: %w", err)
	}
	return position{rateBps: rate.Uint64(), totalAssets: assets, orgOutstanding: orgOut, totalOutstanding: totalOut}, nil
}

// readUintAt decodes a uint256 view call pinned to a block tag.
func readUintAt(ctx context.Context, c *chain.Client, to common.Address, calldata []byte, at string) (*big.Int, error) {
	ret, err := c.CallViewAt(ctx, to, calldata, at)
	if err != nil {
		return nil, err
	}
	return chain.DecodeUint256(ret)
}

// newRunID derives the default run identifier.
//
// Second granularity is not enough: two takes started inside the same second derive the same job
// id, and createJob reverts JobExists on the second one, which is precisely the collision a fresh
// id per run exists to prevent (review: PR #41). Nanoseconds plus four random bytes, because a
// clock that is coarse, or has been stepped backwards, should not be the only guard.
func newRunID() (string, error) {
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate run id nonce: %w", err)
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(nonce[:]), nil
}

// deadline is far enough out that a recording session never trips the refund path.
func deadline() uint64 { return uint64(time.Now().Add(30 * 24 * time.Hour).Unix()) }

func writeState(path string, state RunState) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	blob, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(blob, '\n'), 0o644)
}
