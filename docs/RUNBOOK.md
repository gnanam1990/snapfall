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
