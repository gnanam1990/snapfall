# sidecar (owner: C — CRITICAL PATH)

TypeScript x402/Gateway payment sidecar + our paid demo API.

Day-1 (PRD §14.3 C): Circle dev account → Gateway testnet → clone:
- circlefin/arc-nanopayments (buyer/seller reference)
- circlefin/agent-stack-starter-kits → **reuse `packages/circle-tools`** (Apache-2.0) as the wrapper base [R12]

Deliverable by **Tue Jul 21 night**: full loop `request → 402 → policy-approved sign → retry → 200 + data`
against our own paid API, screen-recorded immediately (insurance + deck asset).

**If not green by Tue night → all three swarm Wed. Nothing else matters until money moves.**
