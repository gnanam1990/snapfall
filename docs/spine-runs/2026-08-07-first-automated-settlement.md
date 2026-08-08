# First automated spine settlement — 7 Aug 2026

The first time `scripts/spine_run` drove the whole demo spine end to end on Arc testnet and
produced a settlement on chain — funding through the fall, read back from chain at every beat.
Before this, the spine had only ever been walked by hand, one command at a time (job-004,
`docs/addresses.md`), which is exactly how a green repo and a red demo coexist.

**Honest headline:** this was a **0.6 reduced-scale run** and its verdict is **UNVERIFIED**, not
PASS — because the two x402 agent purchases still rest at `NOT_BROADCAST` (no Circle facilitator
key). Nothing hard-failed. Every beat that does not depend on a facilitator broadcast passed and
was read back from chain, including the settlement waterfall.

## The run

| | |
|---|---|
| Run id | `20260807T071203Z` |
| Job id (on chain) | `0xb02580600dbcb1025b892f6251d3c91f36ff54fb4319df4b046b167c10c20bc8` |
| Operator (org) | `0x99B723eD097721036C08dd9DEe307286Df3A792D` |
| Customer | `0x9c27EEde9De36cCb0447B87B48BE88739DAf7608` |
| Pool LP | `0x27Ff8025a0Ddc800D58e0F6169Ed5c166406Eb03` |
| Scale | `--price 0.60 --pool-seed 0` (logged `price=0.60`) |
| Verdict | **UNVERIFIED** (first non-PASS: 3-SPEND) |

## Beats, each read back from chain

| Beat | Verdict | Read-back |
|---|---|---|
| 0 / 0b PREFLIGHT / WALLETS | PASS | all three signer keys derive the deployment-expected address; wallets healthy |
| 1 FUND | **PASS** | escrow re-read as `Funded`; pool seeded **3.60 USDC by the LP** (tx [`0xe8e1c0…907e7e`](https://testnet.arcscan.app/tx/0xe8e1c0dfd9a23ae7f9b18ef40ec9db1adad534ee338aac88d951efc952907e7e), block 55738793) |
| 2 SNAP | **PASS** | `openAdvanceOf` principal **0.36**, fee 0.0072, open=true |
| 2b START-WORK | **PASS** | `jobStatus` → **2 (InProgress)** — `JobVault.startWork` |
| 3 SPEND | UNVERIFIED | 0.04 x402 rests at DELIVERED / `NOT_BROADCAST` — the Circle gap, honest |
| 4 REJECT | **PASS** | owner answered `request_alternative` |
| 5 ALTERNATIVE | UNVERIFIED | 0.06 x402 also `NOT_BROADCAST` — the Circle gap, honest |
| 5c DELIVER | **PASS** | `jobStatus` → **3 (Delivered)** — `JobVault.submitDelivery` |
| 6 SETTLE | **PASS** | `jobStatus` → **4 (Accepted)**, advance open=false — the waterfall, below |
| 7 RATE | **PASS** | advance rate **6000 → 6500 bps** — the flywheel ticked on a clean settlement |

Beats **2b** and **5c** are the operator's on-chain delivery assertions (`startWork`,
`submitDelivery`), added in PR #83. Their absence is [what a live run surfaced](#the-gap-a-live-run-surfaced-that-ci-could-not).

## The settlement waterfall

Settlement tx: [`0xc0fb6a86…c52fc`](https://testnet.arcscan.app/tx/0xc0fb6a8699147cc6381d6d8863c7dadd96d4b43e0299773ff5442574d26c52fc)
(block 55738882, status success, gas 147,761). Its `Transfer` logs, each appearing twice on Arc's
dual-decimal USDC surface (18dp native and 6dp ERC-20):

| logIndex | Transfer | Value |
|---:|---|---|
| **69** (18dp) / **70** (6dp) | escrow → **FloatPool** (repayment) | **0.3672 USDC** (0.36 principal + 0.0072 fee) |
| **72** (18dp) / **73** (6dp) | escrow → **operator** (payout) | **0.2328 USDC** |

**The pool is repaid at log index 69/70; the operator is paid at 72/73. Pool is strictly lower.**
As with job-004, this is contract control flow: `JobVault.acceptDelivery` calls
`repayAdvance(...)` before `usdc.safeTransfer(operator, …)`, in frozen code. A lender confirms it
was made whole before the operator took a cent, without trusting the operator.

### Reconciliation (balances to the cent)

- **Escrow 0.60 = pool 0.3672 + operator 0.2328** — the full escrow leaves JobVault in one tx.
- **Fee 0.0072 = first-loss reserve 0.00144 + LP yield 0.00576** — the 20 / 80 split (FEE_BPS 200,
  RESERVE_CUT_BPS 2000).

## Independent LP capital — the difference from job-004

job-004 settled on a pool the operator had seeded: the treasury *was* the lender, so "the business
opens on someone else's capital" was not literally true for that proof. This run is different. The
pool was funded by the LP `0x27Ff` (which deposited 3.60 with its own key, tx `0xe8e1c0…907e7e`),
and the treasury held **zero** pool shares. The 0.36 advance genuinely borrowed a third party's
capital, and the fall repaid that third party first. Two settlements — one hand-driven on
operator-seeded liquidity, one from an automated run on independent LP liquidity — is a stronger
record than one.

## What 0.6 scale does and does not close

The **mechanism** is proven end to end and independent of scale: fund → advance against
independent LP capital → `startWork` → `submitDelivery` → settle with the pool repaid first →
rate flywheel, every step read back from chain. The `+500 bps` rate tick is `GROWTH_BPS` per
accepted job and does not depend on the amount.

It does **not** close the done-when clauses that name PRD figures or a settled purchase:

- **V1** (a real four-cent purchase that *settles*) was open at this run — beats 3/5 rested at
  `NOT_BROADCAST`. **Update (2026-08-08):** the settlement half is now proven — a real 0.04 USDC
  x402 payment settled on Arc, self-facilitated (tx [`0x0d39b5…dccc`](https://testnet.arcscan.app/tx/0x0d39b5738f7042ae82ae0a17f24474e67c27db0cd837b791c112f8d264b6dccc),
  seller `0 → 40000` atomic); see [`2026-08-08-first-x402-settlement.md`](2026-08-08-first-x402-settlement.md).
  That settlement was self-facilitated, not through Circle Gateway — the Gateway path is built and
  has never run against the live service, so the Circle-specific V1 fixture (`capture-v1-fixture`,
  which requires Circle's endpoints) stays open.
- **V7 / V10 / V11 / V12** name the 25.00 PRD figures or two consecutive clean runs; this is a
  single 0.60 run, logged in the scale column so it is never counted as a PRD-figure proof.

A `--price 25` run (which needs a deeper LP-funded pool) plus a Circle key for the x402 beats
would close them. This run proves the spine works; it does not claim the PRD figures.

## The gap a live run surfaced that CI could not

The settlement beat had failed on earlier attempts, and the reason is the argument for running the
live spine at all: **nothing outside `chainops` drove the operator's on-chain delivery
progression.** `startWork` (Funded → InProgress) and `submitDelivery` (InProgress → Delivered) had
no caller in either the daemon pipeline or the spine script. The daemon completed the work
off-chain (`delivery_ready`) but the on-chain job never left **Funded**, so `acceptDelivery` — which
accepts only **Delivered** — reverted on status, and the customer Accept returned 500.

CI is green and could not have caught this. The contracts, daemon, sidecar and dashboard suites all
pass; the gap lives between them, in the on-chain lifecycle a job takes across a full run. It only
appears when a real job is walked from `createJob` to `acceptDelivery` against the live chain — the
one thing a unit or integration test does not do. PR #83 adds the two operator beats; #82 hardened
the preflight so the earlier config errors (a customer key that derived the operator address) are
caught before beat 1 rather than after a wasted seed.

That is why this run exists. A green CI told us the parts worked. Only the live spine told us the
whole did not — and now does.
