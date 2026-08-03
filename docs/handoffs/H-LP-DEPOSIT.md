# H-LP-DEPOSIT — the liquidity surface

**For:** Kimi (dashboard) · **From:** Vasanth's stream · **Written:** 3 Aug 2026

Every constant below was read from chain or from `deployments/arc-testnet.json` on the date above,
not copied from docs. Where a doc disagrees with this file, this file was verified more recently.

---

## 1. Why this exists

Two reasons, and the first is a live blocker.

**It unblocks every spine run.** `FloatPool.sharesOf(operator)` currently reads **20,000,000**
against a `totalShares()` of **20,000,000** — the operator owns 100% of the float. `seed_demo`
reads that and refuses, because a pool the operator funded is a self-funded float, which makes the
demo's opening claim ("we gave our AI business zero dollars") false. No spine run can start until
someone else is the LP. There is currently no way to become an LP except a CLI script holding a
private key.

**It is the only thing a judge can actually do.** The dashboard has four write actions and all
four are mid-flow — they assume a job already exists. Depositing into the pool is a real contract
call, on testnet, with real money, that a stranger can perform in thirty seconds and watch land on
the vessel. Nothing else in the product offers that.

---

## 2. Scope

**Build:** one surface, `/float` or a new `/pool`, with a connected-wallet panel that can

- show the connected address's LP position (`sharesOf`) and what it is worth,
- **deposit** USDC into the pool,
- **withdraw** USDC from the pool.

**Do not build:** operator wallet connect for job actions, customer escrow funding, job creation,
or a "connect wallet" gate on any existing surface. The daemon signs `requestAdvance` and
`acceptDelivery` autonomously with its own key — that is the product thesis, and putting a wallet
prompt in front of it builds the ordinary dApp we are differentiating against. **The LP role is
the exception**: LPs were always meant to be third parties, so a wallet here contradicts nothing.

Withdraw is in scope specifically because it completes the unblock: the operator connects their
own wallet, withdraws their 20 USDC as an LP, and the float becomes third-party capital without
anyone pasting a private key into a terminal.

---

## 3. Verified constants

| | |
|---|---|
| chain id | `5042002` |
| RPC | `https://rpc.testnet.arc.network` |
| explorer | `https://testnet.arcscan.app` |
| FloatPool | `0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5` |
| USDC | `0x3600000000000000000000000000000000000000` |
| USDC decimals | `6` (confirmed by `decimals()` on chain) |

Read them from `deployments/arc-testnet.json` rather than hardcoding — `app/audit/page.tsx` and
`app/settings/page.tsx` already import it and are the pattern to copy.

### Functions you need

```solidity
// FloatPool — permissionless, no modifier, no owner check
function deposit(uint256 assets, address receiver) external returns (uint256 shares);
function withdraw(uint256 assets, address receiver, address owner) external returns (uint256 shares);
function sharesOf(address) external view returns (uint256);   // selector 0xf5eb42dc
function totalShares() external view returns (uint256);
function totalAssets() external view returns (uint256);       // selector 0x01e1d114
function totalOutstanding() external view returns (uint256);  // selector 0x16078d04
```

---

## 4. The part that will bite you

**USDC on Arc is a predeploy AND the native gas token.** Both are true at once and they interact
badly if you ignore them.

1. **Deposit is two transactions, not one.** `deposit` ends in
   `usdc.safeTransferFrom(msg.sender, address(this), assets)`, so the user must `approve` the
   FloatPool for at least `assets` first. Standard ERC-20 approve → deposit. Show it as two steps
   in the UI; do not silently fire two prompts and let the user wonder what happened.

2. **Never offer a "Max" button that deposits the full balance.** Gas is paid in the same token
   being deposited, so depositing everything leaves nothing to pay for the transaction and it
   fails. `docs/RUNBOOK.md` records this biting the team once already (job-002). Cap any max at
   balance minus a visible headroom, and say why.

3. **Receipts show doubled events.** A transfer emits both the ERC-20 `Transfer` and an
   18-decimal native mirror — I counted four transfer logs in the settlement receipt where two
   were expected. If you decode logs anywhere, filter on the USDC address and treat 6dp as
   canonical.

### Contract guards you must surface, not discover

`withdraw` reverts unless:

- `owner == msg.sender` — there is no allowance system; an LP withdraws their own position only.
  So the withdraw control must be disabled unless the connected address has shares.
- `assets <= totalAssets - totalOutstanding` — outstanding principal is deployed and cannot be
  redeemed. When an advance is open, part of the pool is genuinely locked. Show idle liquidity,
  not just TVL, or the user hits `InsufficientLiquidity` and thinks it is a bug.
- `shares <= sharesOf[owner]` — shares round **up**, so dust is charged to the withdrawer. A user
  asking for exactly their full position can round one unit of shares over. Compute the max
  withdrawable as `sharesOf * totalAssets / totalShares` and round **down** for the max button.

`deposit` reverts on `assets == 0` and on `shares == 0`. The second only bites if the pool ever
holds assets with zero shares, which cannot happen today — noted for completeness.

---

## 5. Money rules (non-negotiable; they are in PRODUCT.md)

- Atomic USDC, 6dp, carried as **base-10 strings or bigint**. Never a float, never `parseFloat`,
  never `Number()` on an amount. `lib/format.ts` has `formatUsdcExact` — use it. `formatUsdc`
  truncates at 2dp and has already hidden money on two surfaces this week.
- Tabular figures on every amount (`font-variant-numeric: tabular-nums`).
- **Absence is not zero.** If a read fails, render what `DESIGN.md` §5 calls the absence idiom —
  muted, non-tabular, saying what is missing — never `0.00`. Prior art: `.vessel-facts dd.is-absent`
  and `.jobs-row .is-absent`.

---

## 6. States to handle

| State | What to show |
|---|---|
| no wallet extension | explain, link to one; do not fake a connected state |
| wrong chain | offer to switch to 5042002, name the chain |
| connected, no shares | position reads "no position", withdraw disabled |
| connected, has shares | position, idle liquidity, both actions enabled |
| approve pending / deposit pending | per-step progress; the user is signing twice |
| tx failed | the revert reason, mapped to plain language where you can |
| tx succeeded | the amount, and an explorer link to the tx |

Read-only state must render with **no wallet connected at all** — TVL, utilisation and the vessel
already work without one, and that is what the daemon-less public deploy shows.

---

## 7. Design

Follow `DESIGN.md`; it is derived from the shipped artifact, not from intentions. Specifically:

- The world is a P&ID: flat, no shadow, no glow, no gradient, `--radius: 2px`, semantic colour only.
- **Colour means something.** `--pos` settled/flowing, `--neg` alarm, `--warn` caution, `--accent`
  the live path. Do not colour a control because it is primary.
- No thick accent border down one side of a card — `DESIGN.md` §7 records that as a detector
  finding already fixed once. Corner registration marks are the house alternative.
- The vessel (`PoolVessel`) draws pool level and the caps. A deposit should visibly move it. If you
  have rebuilt or replaced PoolVessel, keep that property.

---

## 8. Dependencies

The dashboard has **no wallet libraries today** — `next`, `react`, `react-dom` only. You are adding
the first ones. `viem` is already a sidecar dependency and the repo's chain code is written against
it, so `viem` + `wagmi` keeps one ABI/encoding library across the repo. A heavier connect-kit is
your call; it is UI.

Keep it out of the server components. `app/audit` and `app/settings` are server components with no
client JS by design, and that is what lets them render on the daemon-less deploy. The wallet panel
is a client component; do not convert those pages to reach it.

---

## 9. Done when

1. A stranger with an Arc testnet wallet and USDC can deposit into the pool from the browser, and
   the pool's TVL and the vessel level both move.
2. The connected address's LP position is visible and correct against `sharesOf`.
3. An LP can withdraw, bounded by idle liquidity, with the max button rounding down.
4. `sharesOf(operator)` can be driven to **0** through this UI — that is the blocker cleared, and
   it is the acceptance test that matters most.
5. Every state in §6 renders without a wallet connected, without crashing.

---

## 10. Sequence, once it works

The blocker is not cleared by a deposit alone. `seed_demo` refuses while the operator holds *any*
shares, so:

1. An LP who is **not** the operator deposits **≥ 8 USDC**.
2. The operator connects their own wallet and withdraws their full 20.

Order matters. Reverse it and the pool drops to ~0.0168 USDC and every advance reverts
`CapExceeded` for a different reason.

**Why 8:** the org advance rate is now **6000 bps** (it rose across the two settled jobs), and
`ORG_EXPOSURE_CAP_BPS` requires `totalAssets ≥ 10 × principal`. A scaled run at a 1.00 job price
draws 0.60, so the floor is **6.00 USDC**; 8 leaves headroom. Note `docs/SPINE-RUNS.md` says 5.00 —
that was computed at the old 50% rate and is stale. A full-price 25.00 run needs **150**, not the
125 in that doc, for the same reason.
