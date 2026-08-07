# Circle Product Feedback

*Snapfall — Ignyte Stablecoins Commerce Stack Challenge submission.*

This is the required Circle Product Feedback section. Every point below is grounded in
something that actually happened building Snapfall on Arc; each is traceable to a file, a
commit, or the settlement transaction on testnet. Where we could not substantiate a claim
against our own repo we say so rather than dress it up. The improvements and recommendations
are the useful part — a submission that says everything was great is worth nothing to you.

Products used: **USDC** (escrow, gas, settlement), **Arc** L1, **x402** for agent purchases
through the sidecar — integrated by calling Circle's **Gateway** `verify`/`settle` endpoints
directly over HTTP, not through an SDK — **Circle Wallets / EOA** (treasury and agent
identities), **USYC** (idle-pool sweep, P1), with **Paymaster** and **CCTP** as planned tiers.

---

## 1. Why we chose these products

Snapfall's entire pitch is a self-financing AI workforce where **every dollar is verifiable
on-chain without trusting the operator**. That thesis picks the stack for us:

- **USDC as the unit of account.** Because the settlement figure is denominated in dollars,
  no price oracle sits between "the customer paid 25.00" and what the contracts move. Every
  number in the demo is the same number on the explorer.
- **USDC as native gas → Arc.** This is what makes the headline claim literal. "We gave the
  business zero dollars" is a single balance read, not an accounting story, because the
  treasury holds exactly one asset and gas is that same asset. Arc's sub-second deterministic
  finality is why the settlement waterfall — pool repaid *before* the operator, enforced by
  `acceptDelivery` calling `repayAdvance` first — is confirmable in one transaction
  ([`0x108a8f…de4b`](https://testnet.arcscan.app/tx/0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b)).
- **x402 over Circle's Gateway.** The workforce buys data per call. We needed sub-cent,
  HTTP-native machine payments under a deterministic spending policy, each tied to a task
  receipt — which is exactly what the documented x402 flow (deposit → 402 → EIP-3009 → retry →
  resource) provides. We wired it by calling Circle's Gateway `verify`/`settle` endpoints
  directly over HTTP; there is no `@circle-fin/*` dependency in the sidecar.
- **USYC** to sweep idle pool capital into yield, behind a strategy interface so the pool can
  earn without the contracts learning anything about the counterparty.

Two products we selected but did not land: the **Paymaster / Gas Station** path is still
unresolved for us (the operator self-funds gas today, noted in `docs/RUNBOOK.md`), and **CCTP**
customer funding was a P2 tier we did not reach. We flag them here rather than imply more
coverage than we shipped.

---

## 2. What worked well

- **USDC-as-gas is the single best thing the stack gave us.** It collapses "the treasury" and
  "the gas budget" into one balance, which is what let the zero-start claim be a fact instead
  of a framing, and what makes the whole money spine readable from the explorer with no
  off-chain reconciliation. This is not incidental — it is the reason the project is
  demonstrable at all.
- **The x402 handshake and EIP-3009 flow were clean to implement from the docs.** The full
  402 → sign → retry → 200 loop is real and cryptographically end to end: the seller
  (`sidecar/src/seller.ts`) issues a 402 challenge, the buyer signs a real
  `transferWithAuthorization`, and the seller verifies the authorization fields before
  returning the paid resource. Circle's documented x402 interface matched what we built, line
  for line, with a plain HTTP client and no SDK. Nothing in the protocol layer fought us.
- **Instant finality simplified the client.** No reorg handling, `confirmationDepth` is `0` in
  `deployments/arc-testnet.json`, and a transaction is trustworthy on inclusion. For a demo
  that reads chain state live, that removed an entire class of "is this confirmed yet" code.
- **USYC's gate did not block our architecture, only the live integration.** Because the sweep
  sits behind a real `IIdleCapitalStrategy` interface, the mock we shipped is a drop-in for a
  real adapter. That is a property of how we built it, but the interface-first shape is exactly
  what Circle's product model rewards.

---

## 3. What could be improved

**The dual-decimal surface confused us, and the docs are the reason.** On Arc, USDC has two
faces: `decimals()` returns **6** (the ERC-20 surface) while the native/gas balance is
**18-decimal**. The practical consequence is that **every USDC transfer emits its `Transfer`
event twice** — once on each surface. In our own settlement receipt the pool repayment appears
at log index 11 (18dp) *and* 12 (6dp), and the operator payout at 14 *and* 15 (documented in
`docs/addresses.md`). A reader scanning that receipt sees each payment twice and cannot tell
which is authoritative. We only got this right by **measuring `decimals()` on-chain** with a
`cast call` rather than trusting a constant, then keeping the native and ERC-20 code paths
physically separate and making our log decoder read only the 6-decimal events. The dual surface
itself is defensible; the real problem is that the reported decimal count is *inconsistent
across Circle's own material and dates* (6 in some places, "18 native / 6 ERC-20" in others, a
flat 18 elsewhere), and the double-log emission is nowhere called out. That inconsistency is
what cost us time.

**`eth_getLogs` has undocumented, per-provider range caps and inconsistent errors.** Snapfall's
Float page reconstructs the advance-rate and fee history by scanning logs from the deployment
block — a ~1.45M-block range. Against the public Arc RPC that scan **never completes**: it
rate-limits with `-32011 request limit reached`. The managed providers cap the range instead
of the request rate, and at very different sizes — Alchemy's free tier caps `eth_getLogs` at a
**10-block** range, PAYG at **10,000** — and they even signal it differently (Alchemy returns
HTTP 400 with code `-32600`; the public node returns HTTP 200 with code `-32012`). Our first
implementation halved the range recursively from the full span and **burned a range-rejection
on every level of the descent** before finding a window that fit. The fix (commit `5e190db`)
forward-chunks at the provider's cap and makes the discovered cap *sticky* for the rest of the
scan, so we learn the real limit once instead of re-failing per chunk. A documented per-provider
range cap — and one canonical "range too large" error code and shape — would have saved us that
entire detour.

**The gas-fee model is under-documented, specifically the >100k-gas priority-fee rule.** There
is a known Arc deploy trap: above roughly 100,000 gas, a transaction must switch its priority
component from `1× base + 1× priority` to `1× base + priority/1e9`, or large contract
deployments hit `maxGasLimit` and fail. This is real and Circle has acknowledged it as a
documentation gap in the builder channel — and never filled it. **In fairness to our own
record, we cannot show that Snapfall tripped this wall:** our only deploy-gas note
(`docs/RUNBOOK.md`, "Deploy gas reality") captures a *different* surprise — forge estimating
2.3× above the actual cost — and our deploy of three small contracts came in under 0.08 USDC.
We raise it anyway because it is a genuine gap that will bite the next team deploying anything
larger, and because the fact that a real deploy trap has no home in the official gas docs is
itself the feedback.

**Getting from x402 protocol to a *live* Gateway settlement is the hard 20%.** The cryptographic
loop was the easy part; the settlement onboarding is where we stalled. The broadcast path is
built — `sidecar/src/facilitator.ts` (landed 2 Aug) calls Circle's Gateway `settle` endpoint
and reads `CIRCLE_API_KEY` — but it has **never run against the live Circle service**. It is
dormant without a key, so our seller reports `settlement: NOT_BROADCAST` and **no x402 purchase
has ever settled**; the loop is cryptographically end to end and financially a dry run, stated
plainly in `docs/CP2-SUBMISSION.md` and `docs/PRD.md` §0.1. Two honest consequences: the wire
contract we coded against Circle's documented interface — the request/response field names — is
an **assumption until that first live call**, which is the actual test; and a *green CI check on
the facilitator path is not evidence the broadcast works*, because that path has never touched
the real endpoint. Getting to a live settlement needs a human-gated setup we could not complete
in the timebox: a Circle account, terms acceptance, an API key, a funded Agent Wallet, and a
spend policy (`docs/V3-CIRCLE-SETUP.md`). Two smaller frictions compound it: the protocol ships
in **two coexisting transport versions** (v1 carries the challenge in the 402 *body*, v2 in a
`payment-required` *header*) that a seller must emit both of; and Circle's Gateway facilitator
lives at a **distinct endpoint** (`gateway-api-testnet.circle.com/gateway/v1/x402/*`) separate
from the generic `x402.org` facilitator, distinct enough that we keep a fixture whose only job
is to pin that we validate against Circle's endpoints and not the public one.

**USYC gating was communicated clearly, but the wait is too long to build on.** No complaint
about the messaging — it was made plain up front that USYC is permissioned and requires
allowlisting. The problem is duration: allowlist waits of multiple weeks are the observed norm
in the builder community, which is longer than any hackathon can absorb. We timeboxed the
investigation to half a day (`docs/WORK-SPLIT.md`, A8) and shipped `MockUSYCStrategy`. **With
access, we would have wrapped the permissioned USYC Teller behind the same
`IIdleCapitalStrategy` interface** — the interface was written for exactly that swap, so the
change would have been an adapter, not a rewrite. The gate cost us the real yield leg of the
demo, not the design.

---

## 4. Recommendations

1. **Publish one authoritative `decimals()` answer for USDC on Arc, and document the double
   `Transfer` emission.** State plainly that a reader will see every USDC transfer twice, which
   surface is canonical, and — as the loud first line — that code must call `decimals()` at
   runtime and never hardcode it. This is a one-paragraph fix that removes a whole category of
   silent-scaling bugs.
2. **Document each managed provider's `eth_getLogs` range cap, and standardize the
   range-too-large error.** One code and one response shape across the public node and every
   partner provider, plus a stated "recommended range for a full-history scan," would eliminate
   the halving dance entirely. Right now every builder rediscovers the cap by failing.
3. **Fill the gas-model documentation gap.** Document the base-fee/priority-fee semantics, that
   base fee must be fetched per transaction (never cached), and the >100k-gas rule (collapse
   priority to `base + priority/1e9`) so contract deploys don't hit `maxGasLimit`. You have
   already acknowledged this gap; closing it in the docs is the highest-leverage single fix for
   new deployers.
4. **Ship a "first settled x402 payment in 10 minutes" quickstart with a sandbox key.** Walk
   account → API key → Agent Wallet funding → spend policy → a first settled 402 through the
   Gateway `settle` endpoint, end to end, on testnet. The protocol is easy; the
   account-and-funding setup is what leaves teams stuck at `NOT_BROADCAST` with a broadcast path
   they cannot verify. A one-command testnet facilitator key would let a team turn their coded
   wire contract from an assumption into a proven call.
5. **Give USYC a self-serve testnet path.** Either a self-serve testnet allowlist or a
   Circle-published testnet mock/faucet, so builders can integrate the real interface shape
   without the multi-week allowlist wait. The interface is stable; only access is the blocker.

---

*Grounding index: dual decimals — `docs/addresses.md`, `deployments/README.md`; log scan —
`dashboard/lib/floatChain.ts`, commit `5e190db`, `docs/RUNBOOK.md`; deploy gas —
`docs/RUNBOOK.md` "Deploy gas reality"; x402 / Gateway facilitator — `sidecar/src/seller.ts`,
`sidecar/src/x402.ts`, `sidecar/src/facilitator.ts`, `sidecar/src/circle-facilitator-fixture.ts`,
`docs/CP2-SUBMISSION.md`, `docs/PRD.md` §0.1, `docs/handshakes/H3-sidecar-api.md` §8,
`docs/V3-CIRCLE-SETUP.md`; USYC —
`contracts/src/strategies/MockUSYCStrategy.sol`, `contracts/src/IIdleCapitalStrategy.sol`,
`docs/WORK-SPLIT.md` A8.*
