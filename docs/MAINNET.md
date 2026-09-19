# Arc mainnet — capital controls, deployment, emergency stop

Arc public mainnet went live **16 Sep 2026** on chain **5042**. This page is what has to be
true before Snapfall is allowed to exist there, and what to do when something goes wrong.

Read it before running `script/Deploy.s.sol` against mainnet.

## Why the contracts were unfrozen

ADR-014 froze the contracts for the hackathon. That freeze was scoped to a testnet deployment
where the worst case costs nothing. Mainnet is a different risk surface, and the frozen
contracts could not meet three of the five preconditions a mainnet deployment was supposed to
have:

| Precondition | Frozen contracts | Now |
|---|---|---|
| explicit maximum total exposure | absent — `ORG_EXPOSURE_CAP_BPS` and `UTILIZATION_CAP_BPS` are *percentages of TVL*, so more capital means proportionally more at risk, without limit | `FloatPool.maxTotalExposure`, absolute |
| per-job and per-advance caps | absent | `FloatPool.maxAdvance`, `JobVault.maxJobPayment` |
| no public pooled liquidity | unenforceable — `deposit()` had no access check | `depositAllowlistEnabled` + `allowedDepositor` |
| owner-controlled capital only | by convention | enforced by the allowlist |
| documented emergency stop | none anywhere; `admin` could only wire, seed and refund | `setPaused` on both contracts, plus this page |

The percentage caps remain and still bound the *shape* of the book. The new controls bound its
*size*. Both are checked; the absolute ceiling is checked first and is authoritative.

The testnet deployment recorded in `docs/addresses.md` predates all of this and is unaffected.
Its source is tagged `testnet-deployment-frozen`.

## The controls

All of them are admin-only and **fail closed**: a freshly deployed pool lends nothing and
accepts deposits from nobody but its deployer. Forgetting to configure a limit cannot silently
mean "unlimited" — `requestAdvance` reverts `CapNotSet()` and `createJob` reverts `CapNotSet()`
until the numbers are set.

| Control | Contract | Effect |
|---|---|---|
| `maxTotalExposure` | FloatPool | absolute ceiling on `totalOutstanding`; an advance that would breach it reverts `ExposureCapExceeded()` |
| `maxAdvance` | FloatPool | absolute ceiling on one advance; reverts `AdvanceTooLarge()` |
| `maxJobPayment` | JobVault | absolute ceiling on one job's escrow; reverts `JobTooLarge()` |
| `depositAllowlistEnabled` + `allowedDepositor` | FloatPool | who may supply capital; reverts `DepositorNotAllowed()` |
| `paused` | both | emergency stop; reverts `EnforcedPause()` |

They live in the contract, not the daemon, because the requirement is that the ceiling holds
*even if the API, the agent, the dashboard or the operator behaves incorrectly*. A limit that
lives in `daemon/internal/freeze` is a limit that a compromised or buggy daemon does not have.
`Caps.t.sol` includes a test that drives the operator path like a runaway daemon — ten funded
jobs, drawing as fast as it can — and asserts the contract stops it at the ceiling.

## What pause does and, more importantly, does not do

**Pause stops money going in and risk going on. Every path that takes money out stays open.**

| Blocked while paused | Still works while paused |
|---|---|
| `FloatPool.deposit` | `FloatPool.withdraw` |
| `FloatPool.requestAdvance` | `FloatPool.repayAdvance`, `FloatPool.writeOff` |
| `JobVault.createJob` | `JobVault.startWork`, `submitDelivery` |
| `JobVault.fund` | `JobVault.acceptDelivery` — a settlement in flight completes |
| | `JobVault.refund` — the customer can always be made whole |
| | `JobVault.cancel` |

This asymmetry is the whole design. A stop that blocked withdrawal or settlement would convert
an incident into a fund freeze, which is strictly worse than having no stop at all: the
operator would have taken custody of money nobody could retrieve. Four tests in `Caps.t.sol`
assert each exit stays open under pause, including a settlement with an advance outstanding
while *both* contracts are paused.

## Choosing the numbers

They are USDC base units on the 6-decimal ERC-20 surface: `25_000_000` is 25.00 USDC. Do not
mix in the 18-decimal native gas surface.

Three constraints worth knowing before picking:

1. `maxAdvance` must be ≤ `maxTotalExposure` — the deploy script refuses otherwise.
2. An advance is at most `CAP_BPS × 1.02 = 86.7%` of a job's escrow, so `maxJobPayment`
   implies a per-job borrow of roughly `0.867 × maxJobPayment`. Set `maxAdvance` at or below
   that unless you want the job ceiling to be the binding one.
3. The percentage caps still apply underneath. A 10%-per-org cap means an advance needs
   `TVL ≥ 10 × advance`, so a thin pool will refuse a draw that the absolute caps would allow.
   That is not a bug; it is the old cap doing its job.

A deliberately small first deployment — the only kind that should happen — looks like:

```bash
export SNAPFALL_MAX_TOTAL_EXPOSURE=50000000   # 50.00 USDC total at risk, ever
export SNAPFALL_MAX_ADVANCE=10000000          # 10.00 USDC per advance
export SNAPFALL_MAX_JOB_PAYMENT=25000000      # 25.00 USDC per job
```

The ceiling is the most you can lose to a total failure of everything off-chain. Pick a number
you would be willing to lose outright, not a number you expect to need.

## Deploying

```bash
export ARC_RPC=https://rpc.mainnet.arc.io
export ARC_USDC_ADDRESS=0x3600000000000000000000000000000000000000
cast wallet import snapfall-mainnet --interactive
export DEPLOYER_ADDRESS=$(cast wallet address --account snapfall-mainnet)
```

The RPC hostname is `rpc.mainnet.arc.io`. `rpc.mainnet.arc.network` does **not** resolve —
unlike testnet, where both work.

Verify the chain before spending anything on it:

```bash
cast chain-id --rpc-url "$ARC_RPC"
```

That must print `5042`. Then confirm the USDC surface is the one the contracts will use:

```bash
cast call "$ARC_USDC_ADDRESS" "decimals()(uint8)" --rpc-url "$ARC_RPC"
```

That must print `6`. Verified 19 Sep 2026: mainnet USDC sits at the same precompile address as
testnet, `symbol()` is `USDC`, `name()` is `USDC`, and the x402 EIP-712 domain pinned in
`sidecar/src/usdc-domain.ts` — name `USDC`, version `2` — reproduces the live mainnet
`DOMAIN_SEPARATOR` exactly. The signing path needs no mainnet-specific change.

Then, with the caps exported:

```bash
cd contracts && forge script script/Deploy.s.sol --rpc-url "$ARC_RPC" --account snapfall-mainnet --sender "$DEPLOYER_ADDRESS" --broadcast
```

The script deploys, wires both directions, and sets all three caps **in one broadcast**. It
aborts before broadcasting if any cap env var is missing. Afterwards, record the addresses and
the deployment block in `deployments/arc-mainnet.json` and `docs/addresses.md`.

The wiring setters are one-shot. A botched deploy is redeployed, never repointed.

## Emergency stop

If anything looks wrong — an unexpected advance, an unrecognised depositor, a daemon behaving
oddly, an exploit reported anywhere in the stack — stop first and diagnose second. Pausing is
cheap and fully reversible.

```bash
cast send "$FLOATPOOL" "setPaused(bool)" true --rpc-url "$ARC_RPC" --account snapfall-mainnet
cast send "$JOBVAULT"  "setPaused(bool)" true --rpc-url "$ARC_RPC" --account snapfall-mainnet
```

Confirm both landed:

```bash
cast call "$FLOATPOOL" "paused()(bool)" --rpc-url "$ARC_RPC" && cast call "$JOBVAULT" "paused()(bool)" --rpc-url "$ARC_RPC"
```

Both must print `true`. New deposits and new advances are now impossible. Everything already in
flight can still complete, and every LP and customer can still get their money out.

Pausing the contracts does **not** stop the daemon from trying. Stop it too, or it will spend
gas on transactions that revert:

```bash
pkill -f snapfalld
```

## Recovery

Work down this list. Nothing here needs a redeployment except the last step.

1. **Establish the size of the problem.** `totalOutstanding` is the money actually at risk;
   `maxTotalExposure` is the worst it could have become.

   ```bash
   cast call "$FLOATPOOL" "totalOutstanding()(uint256)" --rpc-url "$ARC_RPC" && cast call "$FLOATPOOL" "totalAssets()(uint256)" --rpc-url "$ARC_RPC" && cast call "$FLOATPOOL" "reserve()(uint256)" --rpc-url "$ARC_RPC"
   ```

2. **Lower the ceiling rather than unpausing blind.** `setCaps` can be called while paused, so
   the system can be reopened at a smaller size instead of at the size that caused the
   incident.

3. **Close open advances the normal way.** Each one ends through its job: `acceptDelivery`
   settles it and repays the pool, `refund` returns the customer's escrow and writes the
   advance off through the loss waterfall. Both work while paused. There is deliberately no
   admin path that moves an LP's or a customer's money for them.

4. **Let LPs exit.** `withdraw` works while paused, bounded by idle capital
   (`totalAssets - totalOutstanding`). Capital that is lent out becomes withdrawable as
   advances close in step 3.

5. **Close the allowlist.** `setDepositorAllowed(addr, false)` removes a supplier without
   touching anyone else. `setDepositAllowlistEnabled(true)` re-arms the gate if it was ever
   opened.

6. **Unpause when the cause is understood and the ceiling reflects it.**

   ```bash
   cast send "$FLOATPOOL" "setPaused(bool)" false --rpc-url "$ARC_RPC" --account snapfall-mainnet
   ```

7. **Redeploy only for a contract-level defect.** The wiring is one-shot, so a new deployment
   is a new set of addresses: drain the old one through steps 3 and 4 first, leave it paused
   forever, and update `deployments/arc-mainnet.json`.

### What recovery cannot do

There is no admin function that moves another party's funds — no emergency withdraw, no
sweep, no forced settlement. That is deliberate: such a function is itself the largest risk in
most lending contracts, and its absence is why `admin` being compromised cannot drain the pool.
The cost is that recovery runs at the speed of ordinary settlement, and a genuinely stuck
advance stays stuck until its job is accepted or refunded.

One known gap, inherited and recorded rather than fixed: `JobVault.fund` does not require the
FloatPool to be wired, while `acceptDelivery` and `refund` both do. A job funded into an
unwired vault would hold escrow that can be neither accepted nor refunded. Unreachable in any
deployment produced by `Deploy.s.sol`, which wires before it caps and never leaves a vault
unwired — but do not fund a hand-deployed vault.

## What a mainnet deployment does not claim

- **Unaudited.** One pass by the author over the settlement waterfall, reviewed by nobody else.
  The caps on this page bound the loss precisely because the code has not earned trust.
- **Not a lending product.** The allowlist is on and the operator is the only depositor. Taking
  third-party capital is a separate decision with separate obligations, not a config flag.
- **No track record.** Four accepted jobs on testnet and zero write-offs — `acceptedJobs` reads
  4 and `writtenOffJobs` reads 0 for the operator, checked 19 Sep 2026. The penalty path in
  `PENALTY_BPS` has never executed at all, against real money or otherwise.
- **Small on purpose.** The exposure ceiling is the point of the deployment, not a limitation
  of it.
