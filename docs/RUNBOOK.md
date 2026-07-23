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

The command verifies Arc chain ID 5042002, reads every balance before sending, checks that the
funder can cover all deficits while retaining its gas reserve, sends only exact deficits, and
re-reads each funded balance. Without a keystore it exits with the exact shortages and the
Circle faucet URL. The faucet remains a human path because its reCAPTCHA and cooldown must not
be automated. Arc uses USDC as its native gas token; Foundry recommends encrypted keystores
instead of raw private keys:

- https://docs.arc.io/llms.txt
- https://getfoundry.sh/guides/best-practices/writing-scripts/

## Cadence-guarded redeployment

Import/name the deployer keystore, set the same runtime wallet addresses and canonical USDC
address, then run:

```bash
export ARC_USDC_ADDRESS=0x3600000000000000000000000000000000000000
./scripts/redeploy-testnet --account snapfall-deployer
```

The command verifies the RPC chain, reads the committed deployment block timestamp, and refuses
to broadcast until 48 hours have elapsed. The Solidity deploy script now accepts Foundry's
encrypted `--account` signer and no longer reads `TREASURY_PRIVATE_KEY`. After a successful
broadcast, verify all three contracts and update `deployments/arc-testnet.json` with the new
addresses and start block before restarting the indexer.
