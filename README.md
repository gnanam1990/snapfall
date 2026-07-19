# Snapfall

**The self-financing AI workforce. Built on Arc.**

> "We gave our AI business zero dollars and one customer. Watch it finance itself."

Arc "Programmable Money" Hackathon (Encode x Circle) · Tracks: Agentic Economy + DeFi · Final submission: Aug 8, 2026 (buffer Aug 9 AoE)

## Source of truth
- [`docs/PRD.md`](docs/PRD.md) — Snapfall PRD/SRS v4.1 FINAL. Read §14 (Delivery Plan) first.
- [`docs/okx-asp.md`](docs/okx-asp.md) — OKX side-quest (strictly slack-time; see Appendix D of PRD).

## Layout
```
contracts/   Foundry project: JobVault, FloatPool, AuditAnchor (owner: A)
daemon/      Local runtime: supervisor, agents, policy engine, indexer (owner: B)
sidecar/     TypeScript x402/Gateway payment sidecar (owner: C)
dashboard/   Next.js dashboard (owner: C)
docs/        PRD, ADRs, threat model
scripts/     demo seed/reset scripts
```

## Day-0 checklist (tonight)
- [x] Runtime language call (Go vs Node) — **Go, LOCKED 19 Jul** per ADR-001 / PRD §6.2
- [ ] Standup time fixed (15 min daily)
- [ ] Everyone: `bun add -g @circle-fin/cli && circle skill install --tool claude-code` + Arc MCP (docs.arc.io/ai/mcp)
- [ ] A: `cd contracts && forge install OpenZeppelin/openzeppelin-contracts && forge build`
- [ ] A: read docs.arc.io/arc/references/evm-differences BEFORE first deploy; note quirks below
- [ ] C: Circle dev account + Gateway testnet; clone circlefin/arc-nanopayments + circlefin/agent-stack-starter-kits
- [ ] Verify + record here: faucet limits, USYC-on-testnet availability, Paymaster vs Gas Station status

### Testnet notes

Verified 19 Jul 2026 against docs.arc.io. Items marked _unverified_ still need a human with
a funded wallet — do not treat them as done.

| Item | Value | Source |
| --- | --- | --- |
| Arc testnet RPC | `https://rpc.testnet.arc.network` | docs.arc.io/arc/references/connect-to-arc |
| Chain ID | `5042002` | same |
| Explorer | `https://testnet.arcscan.app` | same |
| Faucet | `https://faucet.circle.com` | same |
| Native gas token | USDC, **18 decimals** | same |
| Faucet rate limits | _unverified_ — needs a real claim | — |
| USYC on testnet | _unverified_ (FR-FLT-009 mock fallback is pre-authorized) | — |
| Paymaster vs Gas Station | _unverified_ — C picks one per PRD §4.1 P1 | — |

#### EVM differences that will bite us
From docs.arc.io/arc/references/evm-differences, read before the first Foundry deploy per PRD §14.2.

- **USDC has two decimal surfaces on one balance: 18dp for native/gas, 6dp for ERC-20.**
  The single biggest footgun in this repo. Our contracts do all accounting through the ERC-20
  surface (6dp), so `25.00 USDC == 25_000_000`. Never mix a gas-denominated figure into vault
  math. `MockUSDC` in the tests is deliberately 6dp to match.
- **Transfers to the zero address revert** ("Zero address not allowed"). Validate addresses
  before they can reach a transfer — `JobVault.createJob` rejects zero customer/operator.
- **Finality is deterministic and instant** — transactions finalize on inclusion. This is what
  makes the sub-second "snap" and the one-beat waterfall real rather than marketing.
- **`PREVRANDAO` always returns 0.** No on-chain randomness. We don't use it; keep it that way.
- **Blob transactions (EIP-4844) are rejected by the mempool**; `BLOBHASH` → 0, `BLOBBASEFEE` → 1.
  Irrelevant to us, but Foundry tooling that assumes type-3 support will fail.
- **Base fee goes to the block beneficiary, not burned** (no EIP-1559 burn).
- **Block timestamps are non-decreasing, not strictly increasing** (1-second granularity).
  Deadline logic must use `>=`/`<=`, never assume a strict increase between blocks.
- **`SELFDESTRUCT`**: a non-zero-value `CALL` to a self-destructed account reverts on Arc where
  it succeeds on Ethereum — the docs call this "the largest semantic departure."
- Known limitation: a transfer that fully drains a brand-new account (zero nonce, no code)
  currently reverts. Worth remembering given the demo opens with a **zero-balance treasury**.
- Supported and identical to Ethereum: CREATE2, EIP-7702 set-code txs, EIP-2935 historical blockhash.

### Open spec questions
[`docs/OPEN-SPEC-QUESTIONS.md`](docs/OPEN-SPEC-QUESTIONS.md) — **SPEC-01 is a blocker**: the demo's
12.50 USDC advance contradicts SC-FP-002's formula given a 6.00 max operating budget. Resolve at
standup; it blocks `requestAdvance` and fails AT-11 as written.

## Operating rules (from PRD §14.4)
1. Daily 15-min standup; blocked >4h → escalate immediately
2. ABI + API schemas FREEZE Fri Jul 24
3. Demo spine runs daily from Jul 29; red spine outranks features
4. Scope-cut order (PRD §14.5) is law
5. `main` is always demoable
6. No secrets in repo/logs/screenshots
7. **Submit Aug 8. The 9th does not exist.**
