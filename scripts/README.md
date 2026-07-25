# scripts

## Demo run (V12)

- `./scripts/seed_demo` — prepares one clean run: tops the FloatPool up to the depth the
  snap needs, mints a **fresh job id**, creates the job, and funds the escrow from the
  customer wallet, then re-reads the chain to verify the job is `Funded`, no advance is open,
  and the pool clears the exposure floor. Idempotent: an already-deep pool gets no deposit.
  `--dry-run` prints the plan and submits nothing.

  Two keys, because SC-JV-001 lets only the designated customer fund its own escrow:
  `TREASURY_PRIVATE_KEY` (operator) and `SNAPFALL_CUSTOMER_PRIVATE_KEY` (customer).
  Wallet balances are `./scripts/testnet-ops`' job, not this script's.

  **Why the pool depth is computed, not hardcoded:** FloatPool caps one org's outstanding
  principal at 10% of TVL, so a 12.50 advance needs **TVL ≥ 125.00** or `requestAdvance`
  reverts `CapExceeded` — at 0:30, on camera. The seed derives the floor from the **live**
  `advanceRate`, so after the flywheel lifts the rate to 55% the next run correctly needs
  137.50 rather than silently under-seeding. The default target is 150.00, which covers both.

- `./scripts/reset_demo` — clears local state between takes: the daemon SQLite store (plus
  `-wal`/`-shm`), Brain's per-job memory, the sidecar's durable payment records, and the
  recorded run id. `--dry-run` lists without removing.

  It deliberately does **not** touch Arc: the chain has no delete, and `createJob` reverts
  `JobExists` on a reused id. That is why `seed_demo` mints a fresh id per run — it is what
  makes the release gate's *reset → spine → reset → spine* work with no manual fixes. Pool
  liquidity persists on purpose, so the second take needs no new deposit.

Typical loop between takes:

```
./scripts/reset_demo && ./scripts/seed_demo
```

## Testnet operations

- `./scripts/testnet-ops` — checks every funded wallet from
  `deployments/arc-testnet.json`. Add `--fund --funder-account <name>` to top up exact
  deficits from an encrypted Foundry keystore while retaining the configured gas reserve.
- `./scripts/redeploy-testnet --account <name>` — broadcasts the frozen deployment script
  only after 48 chain-hours have elapsed from the later of the current deployment and last
  successful broadcast. It resolves and passes the keystore's sender explicitly.
