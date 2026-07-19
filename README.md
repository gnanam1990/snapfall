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
- [ ] Runtime language call (Go vs Node) — 30 min, decide, LOCK
- [ ] Standup time fixed (15 min daily)
- [ ] Everyone: `bun add -g @circle-fin/cli && circle skill install --tool claude-code` + Arc MCP (docs.arc.io/ai/mcp)
- [ ] A: `cd contracts && forge install OpenZeppelin/openzeppelin-contracts && forge build`
- [ ] A: read docs.arc.io/arc/references/evm-differences BEFORE first deploy; note quirks below
- [ ] C: Circle dev account + Gateway testnet; clone circlefin/arc-nanopayments + circlefin/agent-stack-starter-kits
- [ ] Verify + record here: faucet limits, USYC-on-testnet availability, Paymaster vs Gas Station status

### Testnet notes (fill as verified)
- Arc RPC: _todo_
- Faucet: _todo_
- USYC on testnet: _todo_
- Paymaster / Gas Station: _todo_
- EVM differences that bit us: _todo_

## Operating rules (from PRD §14.4)
1. Daily 15-min standup; blocked >4h → escalate immediately
2. ABI + API schemas FREEZE Fri Jul 24
3. Demo spine runs daily from Jul 29; red spine outranks features
4. Scope-cut order (PRD §14.5) is law
5. `main` is always demoable
6. No secrets in repo/logs/screenshots
7. **Submit Aug 8. The 9th does not exist.**
