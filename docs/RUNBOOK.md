# Snapfall Operational Runbook

Operational knowledge for demo/recording day. Facts observed in real runs land here —
a runbook section, not a code comment, once a behavior repeats.

## The public Arc testnet RPC (rpc.testnet.arc.network)

Three distinct rate-limit behaviors observed from the same endpoint family — plan
around all three on recording day:

1. **Limits inside HTTP 200 responses** — a request can "succeed" at the HTTP layer
   and carry a rate-limit error in the JSON-RPC body. Never treat HTTP 200 as RPC
   success; check the body's `error` field.
2. **Limits inside JSON-RPC batches** — individual entries of a batch can be
   rate-limited while the batch itself returns. Check per-entry errors.
3. **`eth_getLogs` 429s when chasing head with small chunks** (observed 23 Jul during
   the live indexer confirmation): catch-up over ~8,000 blocks at 2,000-block chunks
   was clean, but once the cursor neared head — Arc mints sub-second blocks, so "near
   head" means many small ranges per second — the endpoint returned
   `HTTP 429 {"code":-32011,"message":"request limit reached"}` repeatedly.

**Recording-day posture:**
- Start the indexer EARLY so catch-up (large chunks, few requests) completes before
  the take; the failure mode is head-chasing, not catch-up.
- `ARC_TESTNET_RPC` overrides the RPC in `deployments/arc-testnet.json` — a private or
  secondary endpoint slots in without a code change.
- The indexer's sync loop already treats a failed `SyncOnce` as retryable; expect and
  ignore intermittent 429 log lines near head, or raise the poll interval.
- Sub-second blocks also mean **non-decreasing timestamps** (see
  `deployments/README.md`, Arc EVM differences) — block numbers, never timestamps,
  for ordering claims.

## Faucet (faucet.circle.com)

20 USDC per claim, one claim per ~2 hours, reCAPTCHA-gated (human hands required).
The scaled lifecycle (pool 20 / job 1.00 / advance 0.50) fits inside one claim with
4x margin under both contract caps (advance ≤ 10% TVL; payment ≤ 20% pool). The full
demo figures (150/25) need ~9 claims over ~18 hours — schedule them across the
recording week, not the recording day.

## Deploy gas reality

Forge's estimate ran ~2.3x above actual on both Arc deploys observed (0.1856 estimated
vs 0.0791036 USDC actual for the three-contract deploy + wiring). Budget from actuals,
not estimates.

## Wallet health and funding

Set the two runtime addresses, then run the read-only health check from the repository root:

```bash
export SNAPFALL_TREASURY_ADDRESS=0x...
export SNAPFALL_CUSTOMER_ADDRESS=0x...
./scripts/testnet-ops
```

Defaults:

- `externalCustomer`: 25.10 USDC (the 25.00 full-demo escrow plus a 0.10 gas margin)
- `operatorTreasury`: 0 USDC, preserving the zero-start demo claim
- funding account reserve: 0.25 USDC

Override those with `SNAPFALL_CUSTOMER_MIN_USDC`, `SNAPFALL_TREASURY_MIN_USDC`, and
`SNAPFALL_FUNDER_RESERVE_USDC`. If the operator must self-fund gas rather than use the
still-unresolved Paymaster/Gas Station path, explicitly raise the treasury minimum; do not
quietly invalidate the zero-start claim.

For guarded automatic top-up, import a testnet key into Foundry's encrypted keystore and name
the account—never put a raw private key in a command or environment variable:

```bash
cast wallet import snapfall-funder --interactive
./scripts/testnet-ops --fund --funder-account snapfall-funder
```

The command independently requires Arc chain ID 5042002, reads every balance before sending,
estimates each transfer's native-USDC gas with 20% headroom, checks that the funder can cover
all deficits while retaining its configured reserve, sends only exact deficits, and re-reads
both funded and funder balances. A read-only invocation without `--fund` lists current/minimum
balances and exits with the Circle faucet URL when a wallet is low. `--fund` additionally
requires a named encrypted keystore account. The faucet remains a human path because its
reCAPTCHA and cooldown must not be automated. Arc uses USDC as its native gas token; Foundry
recommends encrypted keystores instead of raw private keys:

- https://docs.arc.io/llms.txt
- https://getfoundry.sh/guides/best-practices/writing-scripts/

## Cadence-guarded redeployment

Import/name the deployer keystore, set the same runtime wallet addresses and canonical USDC
address, then run:

```bash
export ARC_USDC_ADDRESS=0x3600000000000000000000000000000000000000
./scripts/redeploy-testnet --account snapfall-deployer
```

The command independently requires Arc chain ID 5042002, compares the committed deployment
timestamp against current chain-head time, and refuses to broadcast until 48 hours have
elapsed. It passes an explicit `--sender` resolved from the encrypted `--account`, preventing
a sender mismatch after contract creation. A successful broadcast immediately writes
`deployments/arc-testnet.json.redeploy-guard.json`, so an unchanged deployment artifact cannot
authorize a second broadcast. Verify all three contracts and update
`deployments/arc-testnet.json` with the new addresses and start block before restarting the
indexer.

Before restart, remove stale deployment overrides or update them to the new deployment:

```bash
unset SNAPFALL_DEPLOYMENT_BLOCK
unset SNAPFALL_AUDIT_ANCHOR_ADDRESS SNAPFALL_JOB_VAULT_ADDRESS SNAPFALL_FLOAT_POOL_ADDRESS
```

## FloatPool.requestAdvance guard order (measured on chain, 23 Jul)

`requestAdvance` evaluates its guards in this order (FloatPool.sol:188→196):
1. `jobStatus != Funded` → `JobNotFunded()` (0x514f27b4)
2. `msg.sender != org` → `NotTreasury()`
3. `advances[jobId] != None` → `DuplicateAdvance()` (0x38d6f89b)

Consequence, observed: a second `requestAdvance` on a fully-settled job reverts
**`JobNotFunded`**, not `DuplicateAdvance` — because acceptance moves the job to Accepted
(past Funded), and the status guard is checked first. Both conditions hold, but the
status guard wins. The receipt-status discipline records it as REVERTED either way; the
reason decoded from the receipt is the honest one, not the intended demo label.

## The stale-memory-dir gotcha (job-002 failure, 23 Jul)

The daemon's job memory lives in `<db-dir>/memory/`, a directory BESIDE the db, not
inside it. `rm -rf run.db` does NOT clear it. Relaunching a one-shot on a "fresh" db
whose sibling `memory/` still holds the job → the create step hits "job already exists",
the supervisor retries 5× and gives up (correct — but reads as a mystery exit if you
only see the log tail). A one-shot is not idempotent against a db/memory that already
holds its job. Reset recipe: `rm -rf run.db run.db-wal run.db-shm memory`.

## Customer-wallet gas headroom (job-002 fund failure, 23 Jul)

USDC is the gas token, so a wallet that both pays gas and holds a working balance is
always short by exactly what it spent. A wallet given 2.00 that funds a 1.00 job loses
~0.0044 to gas across create/approve/fund and cannot fund a second 1.00 job. Fund a job
wallet with amount + gas headroom, never the exact figure. This surfaced at
`estimateGas` (pre-flight) as `ERC20: transfer amount exceeds balance`, burning nothing
— the same estimate-pre-empts-revert behavior force-advance's fixed-gas bypass exists to
work around.
