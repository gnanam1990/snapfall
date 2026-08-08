# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

**Primary: the solo operator.** One founder running an AI workforce that takes paid jobs from external customers. They are at a desk, on a laptop, watching agents spend money they are responsible for. Their job is to know — at a glance, at any moment — where their money is, what an agent is about to spend, and whether anything needs their decision. They approve or refuse purchases in real time and are the only human in the loop.

**Secondary, and decisive on 8 Aug 2026: a hackathon judge.** Sees the product once, for minutes, while a recorded run plays. Has no context and will not be narrated to in depth. Confirmed direction from the operator: build for the judge first, with the operator's rigour underneath — no staged affordances, no demo-only shortcuts.

**Third: the external customer.** Receives one tokenised magic link, sees one job's status, clicks Accept, gets a receipt. Never sees the operator's internals or any other customer's job. This isolation is a hard product rule, not a preference.

## Product Purpose

Snapfall is a local-first AI workforce that finances itself. A customer's payment is escrowed on chain; a capital pool advances working capital against that receivable the moment the job is funded; the agents spend that advance on the data they need; and on delivery a settlement waterfall repays the pool — principal and fee first, operator profit last — in one atomic transaction. Every accepted job raises the org's on-chain advance rate.

Success is a founder who can start at a zero balance and watch the business fund its own work, with every movement verifiable by someone who does not trust them.

## Positioning

The mechanism a neighbouring product cannot truthfully copy: **working capital advanced against an escrowed receivable, repaid by an atomic waterfall, with the credit rate set on chain by delivery history.** Competitors show agents that spend a prefunded balance. This shows agents that borrow against work they have not been paid for yet, and repay automatically.

The demo thesis, in the team's own words: *"We gave our AI business zero dollars and one customer. Watch it finance itself."*

## Operating Context

- Everything sensitive runs on the operator's own machine. Agent reasoning, customer data and deliverables never leave the local runtime; what reaches the chain is money movement and the hashes that anchor it.
- The dashboard talks to a local daemon over a single SSE stream carrying two vocabularies: the daemon's own events, and chain events relayed from an indexer. It also reads the chain directly for pool and treasury figures, so several surfaces still work with no daemon running.
- Arc testnet. USDC is the native gas token as well as the unit of account.
- The recorded demo is a fixed seven-beat sequence: fund, snap (the advance), spend, an escalation the owner refuses, the cheaper alternative, settle (the waterfall), and the rate rising.

## Capabilities and Constraints

- Nine surfaces: Overview, Jobs, job detail, Approvals, Float, Portal (customer), Audit, Workforce, Settings.
- Money is always atomic USDC — six decimal places, carried as base-10 strings or integers. Never a float, anywhere, including in display code.
- Contracts are frozen: JobVault (escrow), FloatPool (advance vault), AuditAnchor. An advance is capped at 10% of pool TVL per org and 80% utilisation pool-wide; the fee is 2% of principal and 20% of that fee goes to a first-loss reserve.
- **A 0.04 USDC agent purchase has settled end to end through the x402 path, self-facilitated (`docs/spine-runs/2026-08-08-first-x402-settlement.md`). Separately, the Circle Gateway facilitator client is built and has never run against Circle's live service.** These are two independent facts — settlement did not require Gateway — and surfaces must state both, implying neither's reverse.
- Two figures on Overview are read live from the chain and are real today (pool TVL, advance rate). Most other panels have no data until a run happens.
- Sample data is permitted **only** behind an explicit flag stripped from the deployed artifact, following the existing `SNAPFALL_DEMO_STREAM` precedent. This was a deliberate operator decision, taken after the risk was raised.

## Brand Commitments

- Name: **Snapfall**. Tagline: *"capital in a snap, settlement in a waterfall."*
- Those two words are the product's whole mental model — the *snap* is the advance arriving instantly, the *fall* is the waterfall repaying the pool before the operator. Any visual system must be able to carry both.
- Standing visual constraint from the operator, held consistently across the project: restrained and quiet. Monochrome, flat surfaces, semantic-only colour, the Linear/Stripe register. **No gradient tiles and no glows** — named by the operator as "AI slop". This survives the redesign; "full redesign" replaces the visual world, not this rule.
- Light and dark themes both ship, with a toggle.

## Evidence on Hand

- Real settlement transactions on Arc testnet, with block numbers and explorer links (`docs/CP2-SUBMISSION.md`, `docs/addresses.md`). Two jobs have run the full on-chain lifecycle: funded, worked, delivered, accepted, settled.
- Live contract reads: pool TVL (~20.02 USDC at time of writing), advance rate, utilisation, fees accrued.
- **Absences that must never be fabricated:** the Circle Gateway facilitator has never run against the live service (settlement so far is self-facilitated); the compliance screen is a labelled stub returning `not-screened`; discovery is a local TF-IDF ranker, not the Circle Agent Marketplace; USYC is a mock strategy. These are documented as honest gaps and the interface must not contradict them.

## Product Principles

1. **Every figure names its source.** A number on screen is traceable to a chain read, to a daemon event, or it says plainly that it is waiting. Provenance is the differentiator, not decoration.
2. **Absence is information.** An empty panel explains what it is waiting for. Blank is a failure; "no daemon connected — showing on-chain data only" is a feature.
3. **The human's refusal is load-bearing.** The owner can stop a purchase, and the workforce adapts rather than routing around them. Refusing must be as easy and as legible as approving.
4. **Never claim more than happened.** The codebase refuses to write evidence for a payment it cannot verify; the interface holds the same line.
5. **Money is exact.** Six decimals, tabular figures, no rounding that hides a cent.

## Accessibility & Inclusion

WCAG AA contrast is an existing, measured commitment: `dashboard/app/globals.css` documents per-token contrast ratios against every surface a token lands on, in both themes, including the compromises AA forced on the muted ramp. Any new visual world must re-earn those ratios rather than inherit the claim.
