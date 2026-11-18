# Internal contract review, 18 Nov 2026

This is an internal review, not an independent audit. It covers `contracts/src` (JobVault,
FloatPool, AuditAnchor, MockUSYCStrategy and the interfaces) at `28d049d`, and it was done as
preparation for an external audit. No transactions were sent on any network.

## How it was checked

- Each contract was read line by line, following the money paths: escrow, the settlement
  waterfall, advances, the loss waterfall, LP shares, caps, pause and admin.
- A new stateful invariant suite, [`contracts/test/Invariants.t.sol`](../contracts/test/Invariants.t.sol),
  drives JobVault and FloatPool together. A handler plays LPs, two allowlisted operators, one
  operator the pool never allowed, and customers. It covers deposits, withdrawals and every way a
  job can end. After every call, these must hold:
  - pool cash equals `totalAssets − totalOutstanding + reserve`;
  - LP shares sum to `totalShares`;
  - `totalOutstanding` equals the open advances and the sum of `orgOutstanding`;
  - `totalOutstanding + totalWrittenOff ≤ maxTotalExposure`;
  - the vault holds exactly the escrow of live jobs;
  - an operator the pool never allowed never borrows;
  - no USDC is created;
  - `totalAssets ≥ totalOutstanding`.

  CI runs it at 64 × 100 calls. A local run at 512 × 150 (76,800 calls, with advances, settlements
  and write-offs all exercised) also passed.
- Every finding below was reproduced as a failing scenario before it was fixed. The scenarios are
  now regression tests in [`contracts/test/ReviewAndDisputes.t.sol`](../contracts/test/ReviewAndDisputes.t.sol).
- Static analysers (Slither, Aderyn) were **not** run.

## Findings

| # | Severity | Finding | Status in source |
|---|---|---|---|
| F1 | Medium | A customer who already had the deliverable could wait for the deadline to pass, then call `refund` on the `Delivered` job and get the whole escrow back. The operator was paid nothing, its advance rate fell, and the LPs absorbed the advance. | Fixed |
| F2 | Medium (privileged) | The JobVault admin could `refund` any live job at any time, so a leaked admin key could write off the whole open book onto the LPs. No delay applied, unlike every risk-raising change in FloatPool. | Fixed |
| F3 | Low today; Medium once deposits are public | `withdraw` is priced at current `totalAssets`. An LP who knows a refund is coming can take out idle cash first and leave the remaining LPs the entire loss. The deposit allowlist keeps the LP set trusted for now. | **Open.** Needs a withdrawal cooldown or queue before the deposit allowlist is turned off. |
| F4 | Low | After a total loss (`totalAssets == 0` with shares outstanding), every `deposit` and `withdraw` panicked on a division by zero. | Fixed: reverts `PoolWipedOut` / `InsufficientLiquidity` |

### The fix for F1 and F2: a review window and disputes

- `submitDelivery` records `deliveredAt`. That starts a `REVIEW_PERIOD` of 3 days.
- During that window the customer can `acceptDelivery`, as before, or `dispute`.
- If the window passes with no dispute, the operator can `finalizeDelivery`. It runs the same
  waterfall as acceptance: the pool is paid first, then the operator.
- A disputed job ends in one of three ways:
  - the admin's `resolveDispute(jobId, refundCustomer)`, which either refunds the customer
    (writing off any advance) or settles;
  - the customer accepting anyway;
  - the operator conceding with `refund`.
- `refund` is now narrower:
  - The operator can still refund a live job at any time.
  - The customer, or the admin, can refund only once the deadline has passed, and only for work
    that was never delivered.
  - A leaked admin key can therefore end a delivered job only if its customer has disputed it.
- Pausing still blocks none of these exits.

## Informational

- **Job ids can be squatted.** Anyone can `createJob` with any id by naming itself the operator.
  The cost is a `JobExists` revert; retrying with a fresh id recovers.
- **The job-size cap only applies at creation.** `maxJobPayment` is checked in `createJob`, not
  in `fund`. Lowering it does not stop jobs that already exist; pausing does.
- **`deadline` is not validated** at creation. A deadline in the past only gives the customer an
  earlier timeout refund, and the operator is the one who chose it.
- **AuditAnchor's authority is fixed.** `operatorAuthority` is set to the deployer and cannot be
  rotated.
- **Dust goes to the next depositor.** When the last shares leave, any rounding dust left in
  `totalAssets` goes to the next first depositor.

## Already documented, not re-reported

- The operator bond, stage 1 of the loss waterfall, is not built.
- An allowlisted operator is trusted: nothing stops it from self-dealing up to the caps.

See [HARD-QUESTIONS.md](HARD-QUESTIONS.md).

## What is live

**None of these fixes is on chain.** The contracts are not upgradeable. Every deployed set (see
[MAINNET.md](MAINNET.md)) still has the old `refund` rules, F4 included, and also predates the
operator allowlist. These fixes take effect only in a set deployed from this source.
