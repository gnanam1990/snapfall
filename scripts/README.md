# scripts

## Demo run (V12)

- `./scripts/seed_demo` — prepares one clean run: tops the FloatPool up to the depth the
  snap needs, mints a **fresh job id**, creates the job, and funds the escrow from the
  customer wallet, then re-reads the chain to verify the job is `Funded`, no advance is open,
  and the pool clears the exposure floor. Idempotent: an already-deep pool gets no deposit.
  `--dry-run` prints the plan and submits nothing.

  Three keys, each for a party the contracts treat as distinct:
  `TREASURY_PRIVATE_KEY` (operator), `SNAPFALL_CUSTOMER_PRIVATE_KEY` (customer, because
  SC-JV-001 lets only the designated customer fund its own escrow), and
  `SNAPFALL_LP_PRIVATE_KEY` (the pool LP). The LP must **not** be the operator: the demo opens
  on a treasury holding exactly 0.00 that receives its first working capital from someone
  else's pool, and an operator that funded the pool itself would be lending itself money and
  would not open at zero. The command refuses an LP key equal to the operator address.
  Wallet balances are `./scripts/testnet-ops`' job, not this script's.

  **Why the pool depth is computed, not hardcoded:** FloatPool checks the position *after*
  issuance against two caps (one org's outstanding principal at 10% of TVL, and total
  outstanding at 80%), so a 12.50 advance into an empty pool needs **TVL ≥ 125.00** or
  `requestAdvance` reverts `CapExceeded`, at 0:30, on camera. The floor is derived from the
  **live** rate *and* the exposure the pool already carries, and the binding cap is named in
  the output. Two consequences worth knowing: after the flywheel lifts the rate to 55% the
  next run needs 137.50, and if a previous take left 12.50 outstanding for the same org the
  floor doubles to 250.00. The default target is 150.00, which covers a clean cycle at either
  rate; anything the floor demands beyond that wins automatically.

- `./scripts/reset_demo` — clears local state between takes: the daemon SQLite store (plus
  `-wal`/`-shm`), Brain's per-job memory, the sidecar's durable payment records, and the
  recorded run id. `--dry-run` lists without removing.

  Every path is confined to the project tree: targets are resolved (symlinked parents
  included) and refused unless they sit strictly inside `--root`, which must itself look like
  a Snapfall checkout. Files are removed with `os.Remove` and only genuine directories get the
  recursive form, so a path whose real kind contradicts its name aborts the reset instead of
  being deleted on faith. A stale `SIDECAR_STORE_PATH` or a mistyped flag therefore fails
  loudly rather than eating an unrelated directory.

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
