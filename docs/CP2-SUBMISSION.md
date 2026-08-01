# Snapfall — Checkpoint 2 Progress Summary

**Programmable Money Hackathon (Encode × Circle) · Arc testnet, chain 5042002**
**Tracks: Agentic Economy + DeFi**
**Team: gnanasekaran Jagannathan (lead), Vasanth, Anandan**

Repository: https://github.com/gnanam1990/snapfall

---

## The thesis

> We gave our AI business zero dollars and one customer. Watch it finance itself.

Snapfall is a local-first AI workforce that finances its own operations. A customer's
payment is escrowed on chain. A zero-balance business draws a working-capital advance
against that receivable *before* the work is done. Agents execute the job, buying the data
they need with x402 nanopayments under deterministic spending policies. On acceptance, a
settlement waterfall repays the capital pool **before** the operator sees any profit — and
the org's advance rate updates on chain based on its repayment record.

The business earns its own creditworthiness, cryptographically.

**Why this needs programmable money.** The waterfall ordering — pool repaid before operator
— is the entire credit guarantee. Off chain, that ordering is a promise. On chain, it is
enforced by contract, and a lender can verify it without trusting the operator. That
verification is demonstrated below, by log index, in a real transaction.

---

## What is live on Arc testnet

Three contracts, deployed and verified:

| Contract | Address |
|---|---|
| JobVault | `0xF3830D7C3B8ca873bB0b277c0e179999e3d52681` |
| FloatPool | `0xde9F58A997cf7a3258D09a797eB5546877dc86E5` |
| AuditAnchor | `0x7CDBF8a6…72c6` |

Two jobs have run the full lifecycle end to end on chain — funded, worked, delivered,
accepted, settled.

### job-004 — the complete spine, transaction by transaction

| Step | Transaction | Block |
|---|---|---|
| Start work | `0x751091efb958cfcef8a9f4d4f5ad83909a3b399279bed3f9757c9aeba6b59401` | 53611193 |
| Record expense (0.10) | `0x9b899aaaeaa11677886fe153aefe2624ad005565cb5fa690d0d30bded96bcc21` | 53611348 |
| Submit delivery | `0xc470609675639c77d1a0803687a538a6cd1ca5e04e4692e2b43a5fa6eeb5700b` | 53611521 |
| **Accept + settle** | `0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b` | 53613272 |

### The waterfall, provable by log ordering

Inside that final settlement transaction:

| logIndex | Event | Value |
|---|---|---|
| 10 | `RateChanged` | newRateBps **6000** |
| 12 | Transfer → FloatPool | **0.561 USDC** (0.55 principal + 0.011 fee) |
| 15 | Transfer → operator | **0.439 USDC** |

**The pool is repaid at log index 12. The operator is paid at log index 15.** That ordering
is not a convention we follow — it is the control flow of `acceptDelivery`, and anyone can
read it from the receipt. A lender does not have to trust us.

The settlement reconciles exactly:

- Payment `1.000` = pool `0.561` + operator `0.439`
- Fee `0.011` = first-loss reserve `0.0022` + LP yield `0.0088` (20 / 80 split)

### Creditworthiness, measured on chain

The org's advance rate has moved twice, both times as a consequence of an accepted job:

| Rate | Block | Cause |
|---|---|---|
| 50% (5000 bps) | — | Base rate, zero delivery history |
| 55% (5500 bps) | 53290364 | job-003 accepted |
| 60% (6000 bps) | 53613272 | job-004 accepted |

The rate engine is deliberately asymmetric: **+5% per accepted job, −15% per write-off**,
clamped between a 30% floor and an 85% cap. A single failure costs more than three
successes earn. A rate that could only rise would not be credit.

FloatPool state at the time of writing: TVL 20.0168 USDC · utilisation 0% · fees accrued
0.021 USDC · first-loss reserve 0.0042 USDC.

---

## Architecture

```
contracts/   Foundry — JobVault, FloatPool, AuditAnchor
daemon/      Go — local runtime: agent supervisor, policy engine, approval
             lifecycle, QA worker, chain indexer and write path
sidecar/     TypeScript — x402 / Circle Gateway payment sidecar
dashboard/   Next.js — treasury overview, jobs, approvals, Float, audit
docs/        PRD, ADRs, threat model, handshakes
```

The split is deliberate and is the product's core claim: **sensitive work stays on the
operator's machine; only payments and verifiable claims go on chain.** Agent reasoning,
customer data and deliverables never leave the local runtime. What reaches Arc is money
movement and the hashes that anchor it.

### Circle and Arc surface

- **Arc** — all settlement, USDC-denominated gas, sub-second finality
- **USDC** — escrow, advances, waterfall settlement, agent purchases
- **x402 nanopayments** — agents pay per-call for data through the sidecar, under
  deterministic per-category spending caps with human approval escalation
- **Circle facilitator** — validated in the sidecar payment path
- **USYC** — idle-float yield strategy (currently a mock strategy; see gaps)

### Both tracks, one system

- **Agentic Economy** — agents hold spending authority, purchase data autonomously within
  policy, escalate to a human above threshold, and settle jobs without a human in the loop
- **DeFi** — FloatPool is a real credit facility: LP capital, utilisation cap, first-loss
  reserve, fee split, and an on-chain underwriting curve driven by repayment history

---

## Engineering state

- 20 pull requests merged; CI runs the Foundry suite, the Go daemon integration suite and
  the TypeScript sidecar checks on every push, with worker isolation, duplicate-advance
  protection and Circle facilitator validation as named gates
- Contracts source-verified on the Arc testnet explorer
- Every claim in this document is a chain read, not a log line — the transaction hashes
  above are the evidence

---

## Honest gaps

We would rather state these than have them found.

- **Circle Programmable Wallets are not wired.** The email-based heir-style onboarding for
  non-crypto customers is designed but not implemented; credentials are provisioned and
  Arc testnet is confirmed supported (`ARC-TESTNET`), but no wallet is created yet.
- **USYC is a mock strategy.** The yield path is structurally real; the strategy behind it
  is a stand-in pending permissioned access.
- **Discovery uses a local TF-IDF ranker**, not the Circle Agent Marketplace.
- **Expense receipt hashes are placeholders.** The chain records a receipt hash for each
  expense, but it is not yet joined to the daemon's own purchase provenance — so an
  expense recorded via CLI surfaces correctly as outside-policy in the invoice. Settlement
  ordering is enforced; expense origination is not yet fully attested.
- **The local agent activity feed is not part of this checkpoint's evidence.** The money
  spine is what we can prove to someone who does not trust us, and that is what we have
  put on chain.

---

## Between now and the final submission

- Wire Circle Programmable Wallets for non-crypto customer onboarding
- Surface the advance-rate history in the dashboard from chain rather than the local
  snapshot, so the 50 → 55 → 60 progression is visible and independently verifiable
- Job detail and seed/reset tooling for clean demo re-runs
- 3-minute video and deck

---

*Arc testnet, chain 5042002. Unaudited hackathon code — not for production use.*
