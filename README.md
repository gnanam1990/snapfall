# Snapfall

**The self-financing AI workforce. Built on Arc.**

> "We gave our AI business zero dollars and one customer. Watch it finance itself."

Snapfall is an autonomous AI workforce that starts with an empty treasury. When a customer
commissions a job, the business borrows working capital against the receivable it is owed
(the **snap**), buys the data and does the work, then repays the lender out of the
customer's settlement (the **fall** — a single-transaction waterfall). Every dollar — the
advance, the expenses, the repayment, the operator's take — moves on Arc and is verifiable
on the explorer without trusting the operator.

Arc Programmable Money Hackathon (Encode × Circle) · Agentic Economy + DeFi · CP3 submission
Aug 9, 2026.

- [`docs/architecture.md`](docs/architecture.md) — architecture diagram: the local/on-chain split, the settlement waterfall, and the Circle surfaces marked proven vs unproven.
- [`docs/PRD.md`](docs/PRD.md) — the product/spec definition.
- [`docs/CP2-SUBMISSION.md`](docs/CP2-SUBMISSION.md) — the CP2 progress submission.
- [`docs/CIRCLE-FEEDBACK.md`](docs/CIRCLE-FEEDBACK.md) — Circle product feedback (Ignyte Stablecoins Commerce Stack Challenge).

## Live on Arc testnet

Three contracts are deployed on Arc testnet (chain `5042002`), and two jobs have run the
full spine end to end — funded, advanced, delivered, settled.

| Contract | Address |
| --- | --- |
| JobVault | [`0xF3830D7C3B8ca873bB0b277c0e179999e3d52681`](https://testnet.arcscan.app/address/0xF3830D7C3B8ca873bB0b277c0e179999e3d52681) |
| FloatPool | [`0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5`](https://testnet.arcscan.app/address/0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5) |
| AuditAnchor | [`0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6`](https://testnet.arcscan.app/address/0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6) |

**The settlement that proves the model** —
[`0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b`](https://testnet.arcscan.app/tx/0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b).
In that one transaction the capital pool is repaid at **log index 12** and the operator is
paid at **log index 15**. Pool first, operator second — and that ordering is the contract's
control flow, not a convention or an off-chain promise: `acceptDelivery` calls
`repayAdvance` before it transfers the operator's net. A lender can confirm they are made
whole before the operator takes a cent by reading the transaction, trusting no one.

## The advance rate is credit, not a number

What a business can borrow against its receivables is set by its on-chain track record, and
the dashboard reads it live from `FloatPool.advanceRate(org)`:

**50%** → **55%** (block 53290364) → **60%** (block 53613272)

Each accepted job raises the rate **+5%** (`GROWTH_BPS`); each write-off lowers it **−15%**
(`PENALTY_BPS`), floored at **30%** and capped at **85%**. The penalty is three times the
reward — a rate that could only rise would not be credit. The `RateChanged` events carrying
those ticks are on chain; the dashboard reconstructs the curve from them.

## Layout

```
contracts/   Foundry: JobVault, FloatPool, AuditAnchor          — Anandan
daemon/      Go runtime: Brain, agents, policy, chain writer     — Gnanam
             chain indexer, reconciliation, integration          — Anandan
sidecar/     TypeScript x402 / Circle-facilitator payment rails  — Vasanth
dashboard/   Next.js dashboard (Overview, Float, approvals, …)   — Vasanth
             live activity feed, Float page, hire cards          — Anandan
docs/        PRD, ADRs, threat model, submissions
deployments/ committed chain config + the Arc EVM notes to read before a deploy
scripts/     demo seed / reset
```

## CI

Pushes and PRs to `main` run the full Foundry contract suite, the Go daemon integration
suite (worker-isolation and duplicate-advance gates surfaced by name), the TypeScript
sidecar checks, and the dashboard typecheck / tests / build. `main` is kept green.

## Running it locally

Requires Go 1.26+, Node 20+, and Foundry.

```
# contracts
cd contracts && forge test

# daemon (unit + integration; serve mode needs --deployment + signer keys, see deployments/README.md)
cd daemon && go test ./... && go run ./cmd/snapfall --help

# dashboard — reads the live testnet
cd dashboard && npm ci && npm run dev        # http://localhost:3000
```

**Point `ARC_TESTNET_RPC` at a private RPC** (e.g. Alchemy) for the dashboard. The Float
page reconstructs the advance-rate and fee history by scanning `eth_getLogs` from the
deployment block (~1.45M blocks); the public Arc node's per-request range limit means that
scan never completes against it and the history stays "pending." A private RPC that allows a
≥10k-block `eth_getLogs` range completes the scan in ~150 requests. See
[`deployments/README.md`](deployments/README.md) for chain config and the Arc EVM
differences worth knowing before a deploy.

## Honest limits

- **The local agent activity feed is not demonstrable from a clean checkout.** The
  daemon-side records for both settled jobs were lost, and Snapfall does not fabricate local
  state to stand in for them. What *is* verifiable is the **money spine** — the advance, the
  waterfall, and the rate curve — all on Arc and readable from the explorer. That is what the
  demo proves; the conversational feed is not reproducible here.
- **The USYC idle-capital sweep is a disclosed mock** (`MockUSYCStrategy`) — Circle's
  permissioned USYC requires allowlisting. The mock moves real ERC-20 assets and never claims
  to be the real product.
- **The customer wallet is daemon-custodial for the demo**, stated openly — it stands in for a
  real customer self-custodying and clicking Accept.
- **Scoping and quotes above the money path use a deterministic stub, not a live LLM**, so demo
  runs are reproducible; the chain is authoritative for every figure that moves money.
