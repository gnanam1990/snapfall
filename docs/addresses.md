# Deployment & settlement proof — Arc testnet

Every number on this page was read back from chain, not copied from a deploy log or a
submission doc. Where this page and `CP2-SUBMISSION.md` could disagree, they were checked
against the chain and agree; the chain is the authority regardless.

**Network:** Arc Public Testnet, chain ID **5042002**
**Deployed:** 23 Jul 2026 — blocks 53,268,443–53,268,445
**Deployer / operator:** `0x99B723eD097721036C08dd9DEe307286Df3A792D`
**Explorer:** [testnet.arcscan.app](https://testnet.arcscan.app)
**Status:** unaudited hackathon code. One operator. Nothing here should hold value.

> **The RPC is the primary verification path. The explorer is secondary.** ArcScan has a
> documented, unresolved outage on transaction-hash lookups (*"Something went wrong…"*), so
> the tx links on this page may not render. Every claim below carries the `cast` command
> that produces it against the RPC. Nothing here requires the explorer to work.

## Setup

Set these once; every command on the page uses them.

```bash
export ARC=https://rpc.testnet.arc.network
export JOBVAULT=0xF3830D7C3B8ca873bB0b277c0e179999e3d52681
export FLOATPOOL=0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5
export AUDITANCHOR=0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6
export USDC=0x3600000000000000000000000000000000000000
export OPERATOR=0x99B723eD097721036C08dd9DEe307286Df3A792D
export JOB=0x736e617066616c6c2d6a6f622d30303400000000000000000000000000000000  # job-004 vault id
```

**Pace these calls.** Arc's public RPC rate-limits and will interrupt a tight loop with
`-32011 request limit reached`. A second or two between calls is enough.

## 1. Arc testnet resources

| Resource | Value |
|---|---|
| RPC | `https://rpc.testnet.arc.network` |
| Explorer | `https://testnet.arcscan.app` (secondary — see the note above) |
| USDC | `0x3600000000000000000000000000000000000000` |

USDC is the **gas token** on Arc, and it carries **two decimal surfaces on one balance**:
the native/gas balance is **18-decimal**, while the ERC-20 `decimals()` view is **6**.

```bash
cast call $USDC "symbol()(string)"  --rpc-url $ARC   # "USDC"
cast call $USDC "decimals()(uint8)" --rpc-url $ARC   # 6  (the ERC-20 surface; native gas is 18dp)
```

The practical consequence, and the reason this page decodes only the 6-decimal logs:
**every USDC transfer emits its `Transfer` event twice** — once on the 18-decimal native
surface and once on the 6-decimal ERC-20 surface. A reader scanning a receipt will see each
movement duplicated. That is expected, not a double-spend. In the settlement transaction
(§4) the pool repayment appears at log index 11 (18dp) *and* 12 (6dp), and the operator
payout at 14 (18dp) *and* 15 (6dp).

## 2. Deployment

All three contracts were deployed by the operator key at 25 gwei. Addresses, creation
transactions, blocks, and gas are read from the receipts — not from the deploy script's
stdout.

| Contract | Address | Deploy tx | Block | Gas | Cost (USDC, 18dp) |
|---|---|---|---:|---:|---:|
| AuditAnchor | [`0x7CDBF8a6…3672c6`](https://testnet.arcscan.app/address/0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6) | `0x7476b097…8ce480d` | 53,268,443 | 238,998 | 0.005974950000000000 |
| JobVault | [`0xF3830D7C…3d52681`](https://testnet.arcscan.app/address/0xF3830D7C3B8ca873bB0b277c0e179999e3d52681) | `0x22af2e11…9a5062c9` | 53,268,443 | 1,361,611 | 0.034040275000000002 |
| FloatPool | [`0xde9F58A9…7dc86E5`](https://testnet.arcscan.app/address/0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5) | `0x26bbe400…a0e03c10e` | 53,268,445 | 1,468,710 | 0.036717750000000000 |

Total deployment cost: **0.077732975 USDC**.

**Confirm code is live at each address** — read it back, don't trust the table:

```bash
cast code $AUDITANCHOR --rpc-url $ARC | wc -c     # non-trivial runtime present
cast code $JOBVAULT    --rpc-url $ARC | wc -c
cast code $FLOATPOOL   --rpc-url $ARC | wc -c
```

**Confirm each creation receipt says what the table says:**

```bash
cast receipt 0x7476b09723b8b1e823dbb882b51dd60226643703cc2a96e4ec0a0cd638ce480d --rpc-url $ARC  # AuditAnchor
cast receipt 0x22af2e113de047d19afd4620d8dc54e5b5a2386d94ae079a04efc3569a5062c9 --rpc-url $ARC  # JobVault
cast receipt 0x26bbe400e9de3d41b4c7cd18651a71a2c035754906d3e9809dfa4eda0e03c10e --rpc-url $ARC  # FloatPool
```

Each receipt's `contractAddress` matches the row, `gasUsed` matches the Gas column, and
`effectiveGasPrice` is `25000000000`.

## 3. job-004 lifecycle

One real job carried the full spine on chain: funded, worked, delivered, accepted, settled.
The four state-advancing transactions, with real hashes and measured gas:

| # | Step | Transaction | Block | Gas |
|---|---|---|---:|---:|
| 1 | start-work | `0x751091ef…6b59401` | 53,611,193 | 32,341 |
| 2 | record-expense (0.10) | `0x9b899aaa…ed96bcc21` | 53,611,348 | 54,961 |
| 3 | submit-delivery | `0xc4706096…6eeb5700b` | 53,611,521 | 55,366 |
| 4 | accept + settle | `0x108a8f90…c806ef9de4b` | 53,613,272 | 138,456 |

```bash
cast receipt 0x751091efb958cfcef8a9f4d4f5ad83909a3b399279bed3f9757c9aeba6b59401 --rpc-url $ARC  # start-work
cast receipt 0x9b899aaaeaa11677886fe153aefe2624ad005565cb5fa690d0d30bded96bcc21 --rpc-url $ARC  # record-expense
cast receipt 0xc470609675639c77d1a0803687a538a6cd1ca5e04e4692e2b43a5fa6eeb5700b --rpc-url $ARC  # submit-delivery
cast receipt 0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b --rpc-url $ARC  # accept + settle
```

The job's terminal on-chain status is `4` (Accepted), and its advance is closed:

```bash
cast call $JOBVAULT "jobStatus(bytes32)(uint8)" $JOB --rpc-url $ARC                 # 4 = Accepted
cast call $FLOATPOOL "openAdvanceOf(bytes32)(uint256,uint256,bool)" $JOB --rpc-url $ARC
#   550000 (0.55 principal)  11000 (0.011 fee)  false (repaid, no longer open)
```

## 4. The waterfall

The only transaction that matters for the credit claim is the settlement,
`0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b`. Read its logs:

```bash
cast receipt 0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b --rpc-url $ARC
```

Decoding the 6-decimal ERC-20 `Transfer` logs and the `RateChanged` log by index:

| logIndex | Event | Value |
|---:|---|---|
| 10 | `RateChanged` | newRateBps **6000** |
| 12 | `Transfer` → FloatPool | **0.561 USDC** (0.55 principal + 0.011 fee) |
| 15 | `Transfer` → operator | **0.439 USDC** |

**The capital pool is repaid at log index 12. The operator is paid at log index 15. Pool is
lower.** This ordering is **contract control flow, not a convention**: `JobVault.acceptDelivery`
calls `floatPool.repayAdvance(...)` **before** it executes `usdc.safeTransfer(operator, …)`.
The two events cannot be reordered without changing and redeploying the contract, and the
contract is frozen.

A lender reads this transaction and confirms it was made whole *before* the operator took a
cent — **without trusting the operator, and without the explorer.** That is the entire point
of putting the waterfall on chain.

## 5. The rate progression

An org's advance rate is set by its on-chain repayment record, read live from
`FloatPool.advanceRate(org)`:

```bash
cast call $FLOATPOOL "advanceRate(address)(uint16)" $OPERATOR --rpc-url $ARC   # 6000
cast call $FLOATPOOL "acceptedJobs(address)(uint32)" $OPERATOR --rpc-url $ARC  # 2
```

| Rate (bps) | Block | Emitted? |
|---:|---:|---|
| 5000 | — | **No.** The base rate is the pre-tick value (`BASE_BPS`, `acceptedJobs = 0`); `RateChanged` fires only *on* a tick, so 50% is never emitted. |
| 5500 | 53,290,364 | `RateChanged` (job-003 settlement) |
| 6000 | 53,613,272 | `RateChanged` (job-004 settlement, §4) |

`RateChanged` topic0 — scan for it directly:

```
0xec739c9af710a6df2b3e3656f38b5d59af57d3022cd5a88ca4516db96a4ca5c7
```

```bash
# each tick lives in its settlement receipt; both carry the topic0 above
cast receipt 0x9b57c8b8aa917823611b3f94a82de5cd9a14696ea74c6dc9c59107945b03ccdd --rpc-url $ARC  # -> 5500 @ 53290364
cast receipt 0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b --rpc-url $ARC  # -> 6000 @ 53613272
```

The engine is **deliberately asymmetric** — read the constants from chain, not the source:

```bash
cast call $FLOATPOOL "GROWTH_BPS()(uint16)"  --rpc-url $ARC   # 500   (+5% per accepted job)
cast call $FLOATPOOL "PENALTY_BPS()(uint16)" --rpc-url $ARC   # 1500  (-15% per write-off)
cast call $FLOATPOOL "FLOOR_BPS()(uint16)"   --rpc-url $ARC   # 3000  (floor 30%)
cast call $FLOATPOOL "CAP_BPS()(uint16)"     --rpc-url $ARC   # 8500  (cap 85%)
```

The penalty is three times the reward. A rate that could only rise would not be credit.

## 6. Reconciliation

The settlement balances exactly, to the cent, on chain:

- **Payment 1.000 = pool 0.561 + operator 0.439.** The full escrow leaves JobVault in the
  one transaction; nothing is retained.
- **Fee 0.011 = first-loss reserve 0.0022 + LP yield 0.0088** — the 20 / 80 split.

```bash
cast call $FLOATPOOL "FEE_BPS()(uint16)"         --rpc-url $ARC   # 200   (2% of the 0.55 principal = 0.011)
cast call $FLOATPOOL "RESERVE_CUT_BPS()(uint16)" --rpc-url $ARC   # 2000  (20% of the fee -> reserve)
cast call $FLOATPOOL "reserve()(uint256)"        --rpc-url $ARC   # 4200  (0.0042 cumulative: job-003 0.002 + job-004 0.0022)
```

The on-chain `reserve` is cumulative across both settled jobs (0.0042); job-004's own
contribution is the 0.0022 above.

## Security review

The settlement waterfall (§4) was reviewed against **every reachable path** through
`JobVault.acceptDelivery`, not only the path job-004 happened to take:

- **No advance drawn** — `open == false`; the operator receives the full escrow, the pool is
  owed nothing.
- **Advance open** — `repayAdvance(principal + fee)` executes before `safeTransfer(operator)`;
  pool first, by control flow.
- **Advance already repaid / written off** — unreachable: those states put the job at
  `Accepted` / `Refunded`, and `acceptDelivery` accepts only `Delivered`.
- **Partial repayment** — rejected: `repayAdvance` requires `amount == principal + fee`.
- **Write-off interleaved with settlement** — impossible: `acceptDelivery` flips the job to
  `Accepted` before any external call and is `nonReentrant`; `writeOff` requires an `Issued`
  advance.

The property that makes the ordering safe — `operatorNet = payment − (principal + fee)` cannot
underflow, because an advance is at most `CAP_BPS × 1.02 = 86.7%` of the immutable escrowed
`customerPayment` — is asserted over the full rate range and random org histories by two
256-run fuzz tests: **`testFuzz_settlement_escrowAlwaysCoversPool`** and
**`testFuzz_advance_neverExceedsEscrow`**. Both pass, alongside the full 111-test contract suite.

**What this review is not.** It was one pass by the author of the code — unaudited, and
reviewed by no one else. Knowing what the code was *meant* to do is precisely the wrong prior
for finding what it *actually* does, so the clean result above should be read as the author's
claim about the author's own work, pending an independent audit — not as assurance.

## What this proof does NOT show

This page proves the **money spine** — the part verifiable without trusting the operator.
It deliberately does not claim more.

- **The local agent activity feed is not part of this evidence.** The daemon-side records
  for both settled jobs were lost to a scratchpad wipe, and nothing here reconstructs them.
  What is proven is what is on chain.
- **Expense receipt hashes are placeholders** (`0x1111…1111`, `0x2222…2222`), not joined to
  daemon purchase provenance. The settlement *ordering* is enforced by the contract; expense
  *origination* is not attested — the `recordExpense` receipt hash could be anything.
- **Two jobs is not a track record.** The rate has only ever risen because nothing has been
  written off. `PENALTY_BPS` and the write-off waterfall are covered by the contract test
  suite, but the penalty path has **not** been demonstrated on chain.
- **USYC is a mock strategy** (`MockUSYCStrategy`). The yield seam is structurally real; the
  strategy behind it is not the permissioned Circle/Hashnote product.
- **A latent escrow-stranding gap, recorded not fixed (LOW).** `JobVault.fund` does not require
  the FloatPool to be wired, while `acceptDelivery` and `refund` both do. A customer who funds a
  job before wiring, in a deployment where wiring never happens, would hold escrow that can be
  neither accepted nor refunded. `refund` calls `floatPool.openAdvanceOf` unconditionally even
  though no advance can exist while unwired, so the dependency is stricter than the logic
  requires. **This is unreachable in this deployment:** FloatPool was wired at deploy, before any
  job existed. Per ADR-014 the contracts are frozen and this is not a fund-loss bug, so it is
  recorded here rather than remediated.
- **Nothing here has been audited.** Unaudited hackathon code, one operator, testnet only.
