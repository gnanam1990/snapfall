# Snapfall — Product Requirements Document (PRD)

**Capital in a snap, settlement in a waterfall. One Brain. One Loop. One Waterfall. The Self-Financing AI Workforce, Built on Arc.**

---

## 0. Document Control

| Field | Value |
|---|---|
| Document | Snapfall — Formal Product Requirements Document |
| Version | v8.0 (Formal PRD Edition) |
| Date | Wednesday 22 July 2026 |
| Status | Ratified content, formalized. **Introduces zero new decisions.** |
| Consolidates | v7.1 *The Constitution* (21 Jul 2026) and v7.2 *Full Detailed Edition* (the v7.2 line, including ADR-019). By inheritance: v4 (scope/schedule), v6 (adopted with amendments), Teammate 2's PRD v1.0 (absorbed with credit). |
| Superseded documents | All prior drafts. v4 remains the SRS annex for detailed requirement IDs (FR/SC/SEC/NFR/AT) except where amended herein. |
| Source of truth | The ratified v7-line Constitution remains canonical. This PRD reorganizes the same locked content into standard PRD form; **in any conflict, the Constitution wins.** |
| Change control | **Rule #9 (binding):** no new PRDs. Any change is a small PR against the canonical file, reviewed at standup. **Rule #8:** disagree and commit — vote once, then all three row the same direction. |
| Contracts status | **DONE.** JobVault, FloatPool, AuditAnchor — **84 tests green. FROZEN** (ADR-014). |
| Submission | Saturday 8 August 2026 (buffer Sunday 9 August, AoE — contingency only) · Demo Day Thursday 20 August 2026 |
| Tracks | Agentic Economy (primary) **+ DeFi (primary — ADR-015)** |
| Team | A: Contracts & Chain → indexer + integration · B: Runtime & Brain · C: Payments, Frontend & Story |

### Contents

1. Executive Summary
2. Background & Problem Statement
3. Product Overview
4. Goals, Success Criteria & Non-Goals
5. Scope — Demo Definition (1 Spine + 1 Beat)
6. System Architecture
7. Functional Requirements
8. User Experience Requirements (The Fabulous Layer)
9. Non-Functional Requirements
10. Security & Safety Requirements
11. Acceptance Criteria & Test Plan
12. Timeline, Milestones & Operating Rules
13. Dependencies & Platform Integrations
14. Risks & Mitigations
15. Architecture Decision Records (ADRs)
16. Roadmap (Post-Submission)
17. Assumptions & Open Items
- Appendix A: Demo Script v3.1 (3:00)
- Appendix B: Source Consolidation & Credit Map
- Appendix C: Glossary

---

## 1. Executive Summary

Snapfall is a local-first AI workforce for a solo founder. One shared **Brain** talks to the owner, scopes and routes every job, and watches every pipeline at once. Specialized **Workers** execute tasks but never touch money and never talk to the owner. A **Funding agent** — the isolated signer — moves money only on Brain-relayed, owner-approved instructions. A **Billing agent** turns every settlement into a real invoice from actual on-chain data.

Underneath sits one financial engine: **JobVault** escrows the customer's payment, **FloatPool** advances working capital against it the moment the job is funded, and the **settlement waterfall** repays the pool — principal + fee first, operator profit last — in one atomic Arc transaction. Every accepted job raises the org's on-chain advance rate.

**One line:** *One founder. A workforce that can't embezzle itself. A business that gets cheaper to run every time it delivers.*

**Demo thesis:** *"We gave our AI business zero dollars and one customer. Watch it finance itself."* The treasury genuinely starts at 0.00; by the end of the demo it has settled a real job using money borrowed against its own escrowed receivable — verifiable on the explorer, impossible to fake invisibly.

The smart-contract layer is complete and frozen: JobVault, FloatPool, and AuditAnchor are deployed with **84 tests green**. Remaining work is the runtime (Brain, Workers, Funding, Billing), the demo spine, and the presentation layer, against a submission date of **Saturday 8 August 2026**.

---

## 2. Background & Problem Statement

Snapfall answers a question that sounds simple and isn't: *what does it take for one person to run an AI-staffed business safely?* The hard part is not making an agent that does work — dozens of frameworks do that. The hard part is everything around the work: who is allowed to spend money, how much, on what, and who catches it when something goes wrong. Two independent gaps had to be solved; one binding rule ties them together.

### 2.1 The Spending Gap

An agent buying data or compute sits on a binary choice:

- **Unrestricted wallet access** — one hallucinated tool call or one prompt-injected webpage is a treasury-draining event.
- **Human approval per micro-purchase** — autonomy dies; you've built an expensive chatbot, not an autonomous business.

**Solution:** a deterministic policy engine between the agent's intent and any money movement. Agents submit **PaymentIntents** (a structured proposal: merchant, amount, purpose, nonce) and never touch keys. A pure-code engine evaluates each intent against manifest-declared rules — budget, per-transaction limit, daily cumulative limit, merchant allowlist, and explicit **blocked categories** (e.g. `token-trading`, `gambling` — blocked by default; absorbed from Teammate 2's work). Outcomes: **AutoApprove / HumanApprovalRequired / Deny**.

### 2.2 The Working-Capital Gap

A fully-signed, fully-funded job still requires the business to pay for the work — data sources, compute, agent time — *before* the customer's payment releases on delivery. Human small businesses factor this with invoice financing: slow, manual, gatekept. An autonomous one-person business has no credit history any bank recognizes.

**Solution — Float:** the customer's payment is *already escrowed on-chain* the moment the job is funded, so payment risk ≈ 0. What remains is *performance risk*, priced by a pure on-chain function of delivery history:

```
rate(org) = clamp(50% + 5%·acceptedJobs − 15%·writeOffs, 30%, 85%)
```

Every accepted job earns the business cheaper capital for the next one; every write-off makes the next advance more conservative. This is the flywheel — a real state variable any explorer visit can confirm, not a marketing metaphor.

### 2.3 The External-Customer Rule (Binding — ADR-013)

Wherever Float is involved, the pipeline serves an **external paying customer**. The flywheel requires **customer ≠ operator**: borrowing against money you deposited yourself is theater, not financing — no lending has actually happened, and it would quietly undermine the credibility of the one number in this product (the advance rate) that has to mean something real.

Consequence: owner-personal pipelines (ETH Accumulation Watcher, Portfolio Signal on the owner's own book) are **roadmap** and never draw advances.

---

## 3. Product Overview

### 3.1 What Snapfall Is

| Field | Value |
|---|---|
| Product | **Snapfall** — capital in a snap, settlement in a waterfall |
| Category | Self-financing AI workforce (agentic economy + DeFi primitive) |
| Chain | Arc (sub-second finality, USDC-native gas) |
| Deployment posture | Local-first, one owner, pluggable Worker slots |
| Financial engine | JobVault (escrow) + FloatPool (ERC-4626-style advance vault) + atomic settlement waterfall + AuditAnchor — **built, tested (84 green), frozen** |

The product packs three claims into one thesis, each mapping to a different part of the architecture:

- **"A workforce that can't embezzle itself"** → the Brain architecture (§6.1–6.2): the guarantee is structural, not behavioral. No agent is ever in a position to both decide something matters and move money because of it.
- **"Gets cheaper to run every time it delivers"** → the financial backbone (§6.3–6.5): the advance rate is a pure function of delivery history, verifiable on-chain.
- **"One founder"** → the framing constraint behind every other decision: a single, non-technical-by-default person must be able to trust an autonomous system with real money, without hiring a compliance department to supervise it.

### 3.2 Target Users & Personas

| Persona | Role in the system | Key needs |
|---|---|---|
| **The Owner** (solo founder/operator) | The only human inside the business loop. Talks exclusively to Brain; confirms scope; approves or rejects escalated spend in one unified inbox. | Trust without supervision overhead; one-tap control; no exposure to keys, JSON, or internal plumbing; a business that gets cheaper to run as it delivers. |
| **The External Customer** (buyer of a service, e.g. a due-diligence report) | Funds the JobVault escrow; receives the deliverable through the magic-link portal; their **Accept** is the literal on-chain trigger for the settlement waterfall. | A clean status + accept + receipt experience; isolation from the operator's internals and from every other customer's job. |
| **The Standing-Pipeline Client** (e.g. a client paying a contractor through Snapfall) | Funds milestone-based work; each milestone cycles a fresh JobVault job through the identical loop, with completion % reported to Brain *before* any release. | Escrow-native, verifiable milestone payments; the same rails and guarantees as one-shot jobs. |

### 3.3 Value Proposition

1. **Capital in a snap:** working capital advances against escrowed receivables the moment a job is funded — no bank, no underwriter, no invoice factoring.
2. **Settlement in a waterfall:** on acceptance, one atomic transaction repays the pool (principal + fee) *before* the operator sees a cent — enforced by the contract test suite, not by documentation.
3. **A flywheel you can verify:** the advance rate moves only with on-chain delivery history (§2.2). Delivery is the credit score.
4. **Structural safety:** role-splitting by capability placement means a fully compromised Worker can produce, at worst, a bad report — no channel to money exists (§6.1).

---

## 4. Goals, Success Criteria & Non-Goals

### 4.1 Goals (this release)

| # | Goal | Measure |
|---|---|---|
| G1 | Ship the self-financing loop end-to-end on testnet | The Due Diligence spine (§5.1) runs live, treasury provably starting at 0.00, with daily spine runs from Wed 29 Jul |
| G2 | Prove the architecture generalizes | The Build-Monitor beat (§5.2) demonstrates "different work, same Brain, same rails, same waterfall" in ≤30 s |
| G3 | Make the mechanism *visible* | Fabulous Layer F1–F4 + F7 (§8) over events already emitted; zero contract changes |
| G4 | Demonstrate judge-facing Circle/Arc adoption | Every layer of the adoption map (§6.6) demonstrably used — including Circle's own Gateway x402 facilitator (AT-18) |
| G5 | Submit on time, verified | Sat 8 Aug 2026, evening IST; links verified incognito; Sun 9 Aug held as AoE contingency only |
| G6 | Keep the safety story mechanically true | AT-01..19 green; 84 contract tests stay green; the freeze (ADR-014) holds |

### 4.2 Success criteria

- Treasury starts at **0.00** and the demo settles a real job using an advance drawn against its own escrowed receivable — every beat a real, replayable transaction.
- The settlement waterfall executes in **one atomic transaction**, pool repaid principal + fee **before** any operator transfer, with explorer proof.
- The advance-rate ring visibly moves **50% → 55%** on the first accepted job ("next job unlocks 13.75").
- The rejection beat lands: a $4.00 spend escalates, the owner **rejects** live, the workforce adapts ($0.06 alternative) — the human's "no" is respected, not worked around.
- The QA beat lands: the QA-worker bounces one unsupported claim; Delivery revises; `DeliveryReady` stays blocked until revision (AT-19).
- All acceptance tests green (§11), restart recovery demonstrated (including Brain), secret audit clean, full reset rehearsed twice.

### 4.3 Non-goals (this release)

Cut from demo scope → slide-ware + roadmap (ADR-012: one unforgettable loop beats six shallow ones):

| Item | Disposition | Rationale |
|---|---|---|
| ETH Accumulation Watcher | Roadmap | Owner-personal; no escrow; DCA-bot silhouette; cannot draw Float (ADR-013) |
| Portfolio Signal (owner's own book) | Roadmap | Owner-personal; cannot draw Float (ADR-013) |
| Treasury Transparency Report | Slide / future SKU | Real future product; doesn't need screen time this submission |
| Compliance as a standalone pipeline | Folded in | Runs as one task step inside the DD-worker instead |
| WorkforceRegistry (§6.5) | Roadmap, post-submission (ADR-019) | Additive and safe, but new scope is new scope this close to submission |
| In-contract staged release (Circle Session Keys) | Roadmap, post-GA | Fresh-job-per-milestone (§6.4) covers milestones until Session Keys mature |
| Policy sliders + playground; shareable receipt card | Deferred garnish | Only if the Aug 3–5 window is calm |

---

## 5. Scope — Demo Definition (1 Spine + 1 Beat)

One crown-jewel loop + one 30-second generalization proof. Everything else is a slide of pluggable Worker slots.

### 5.1 The spine — Due Diligence, sold to an external customer

End-to-end flow (all amounts exact):

1. An **external customer pays 25 USDC** for a due-diligence / market report.
2. Payment escrows in JobVault; treasury is at **0.00**.
3. **The snap:** FloatPool advances **12.50 USDC** (50% advance rate), sub-second.
4. Brain scopes the request; **owner confirms scope** before work starts.
5. Brain assigns the **DD-worker** (a Worker never chooses its own work).
6. Service discovery: the worker's fuzzy need is embedding-matched against **Circle Agent Marketplace**; highest similarity wins (FR-DSC-001: the embedding decides *which* service, never *whether* to pay).
7. The worker buys sources via **Circle's Gateway x402** — a **$0.04 purchase, auto-approved** by the policy engine.
8. **Compliance step folded in:** a Circle Compliance Engine screen runs as one task step; the report carries **"evidence, not a guarantee"** plus a confidence indicator (FR-CMP-001).
9. **The rejection beat:** a **$4.00** purchase escalates past policy → owner **rejects** in the activity feed → the worker adapts, buying a **$0.06** alternative.
10. **The QA beat:** the QA-worker bounces one unsupported claim with reasons → Delivery revises (FR-QA-001) — the "workforce polices itself" moment.
11. Delivery is content-hashed; the customer opens the **magic-link portal** and clicks **Accept** — the literal on-chain trigger for settlement.
12. **The fall:** one atomic transaction — **12.75 to the pool first** (12.50 principal + 0.25 fee at 200 bps on the advance), remainder to the operator.
13. **The flywheel:** the rate ring ticks **50% → 55%**; "next job unlocks 13.75."

**Precondition for step 3 (the snap):** FloatPool must be seeded with **TVL ≥ 125 USDC** before the demo. The frozen contract caps per-org exposure at 10% of TVL (`ORG_EXPOSURE_CAP_BPS = 1000`), so the 12.50 advance reverts `CapExceeded` on any smaller pool. Plan **150 USDC** for headroom — it also covers the 13.75 second-job beat (needs ≥ 137.50). Seeding lands in the V12 seed script.

### 5.2 The generalization beat — Project-Build Fund Monitor (≤30 s)

The escrow-native standing pipeline: a client pays a contractor through Snapfall; the **Build-Monitor-worker** watches the contractor's repo; on a fund request it reports completion % to Brain **before** any release; each milestone = a **fresh JobVault cycle** (§6.4) through the identical loop.

Line: *"Different work. Same Brain. Same rails. Same waterfall."* The beat exists to prove the architecture is a platform, not a single trick.

### 5.3 Never-cut list

Protected beats regardless of schedule pressure: **the snap, the fall, the rejection beat, the QA beat, the portal accept, the money graph, the $0-start.** (Scope-cut order in §12.3.)

---

## 6. System Architecture

### 6.1 The Brain architecture — one loop, four fixed roles, one law

**The governing law: no agent ever talks to another agent directly.** Every message is `Agent → Brain`; Brain decides what happens next. This is the entire security model restated as a routing rule: a fully compromised Worker can produce, at worst, a bad report — there is structurally no channel from "Worker" to "money moves." Enforced by **capability placement, not instruction**.

```
Owner ⇄ Brain ⇄ Worker (pipeline-specific, pluggable slot; incl. QA-worker)
              ⇄ Funding agent (isolated signer → contracts)
              ⇄ Billing agent (→ Owner + Vendor invoices)
```

### 6.2 Roles

| Role | Trust level | Responsibilities | Hard boundaries |
|---|---|---|---|
| **Brain** | The only agent the owner talks to | Scopes each request; presents scope for owner confirmation *before* work starts; assigns the correct Worker; monitors every pipeline; keeps a **separate memory file per job** (scope, stage/completion %, assigned Worker, every owner confirmation with timestamp, escrow/settlement state). Control plane: Circle CLI + Circle Skills. | Router with memory, not a state monopoly — contracts + event log remain truth; rebuilds per-job memory by replaying the log after restart. |
| **Worker** | Least trusted, most bounded | Receives exactly one assignment from Brain, executes, reports back to Brain. Worker types are the pluggable slot; the loop never changes. | Never contacts the owner, another agent, or Funding. Never chooses its own work. |
| **QA-worker** (Worker slot; absorbed from Teammate 2, ADR-017) | Independent checker | Pre-delivery Quality Reviewer pass: completeness, source coverage, unsupported-claim detection, customer-data-leakage check. | Failures bounce back to the originating Worker **with reasons**; a separate Worker, not self-review — self-certification is the failure mode this architecture exists to avoid. |
| **Funding agent** | The only component that can move money | Acts only on Brain-relayed, **owner-approved** instructions. Two independent authorization layers: **outer** — Circle Agent Wallet spend-policy (allowlists, time-bound limits, at the wallet layer); **inner** — the deterministic policy engine (per-job budget, per-tx limits, blocked-category floor). Executes against the contracts. | Never receives raw Worker output. Thin deterministic service wrapping the existing signer — not an LLM agent. |
| **Billing agent** | Loop-closer | Assembles invoices from **actual on-chain transaction data** (real settled amounts, real tx hashes) — never a summary that could drift from chain state. Sends to owner + paid vendor. Sources: Arc Explorer + Gateway settlement records + Brain's per-job files. | Thin deterministic service formatting indexer data — not an LLM agent. |

**Why this shape:** a single agent that reasons about a job AND releases funds is one prompt-injection from unrecoverable loss. Role-splitting removes that possibility by construction.

**Mapping from v4 (renames, nothing orphaned):** Manager → Brain. Research / Delivery / Quality → Worker slots (QA the newest). Finance-Controller → Funding agent (execution half) + policy engine (authorization half, functionally unchanged). All v4 FR-ACT / FR-PAY / FR-APR / SEC requirements remain in force under the new role names.

### 6.3 The financial backbone — contracts as built, FROZEN (84 tests green)

**JobVault** — escrow and lifecycle engine: `Created → Funded → InProgress → Delivered → Accepted`. Operating expenses bounded against a budget; **content-hashed delivery required before acceptance**; refund/cancel paths notify FloatPool so a written-off job's economics hit the pool's accounting. Revision/expired/disputed handling per v4 annex P1s (confirmed by Teammate 2's spec).

**FloatPool** — ERC-4626-style vault. On `Funded`, the org may draw `advance = advanceRate(org) × customerPayment` (as-built simplified formula — deliberately no separate operating-budget ceiling layered on top). The caps that matter are the pool's own systemic solvency limits: **≤10% of TVL to any one org; ≤80% pool-wide utilization.** Exactly **one advance per job** (SC-FP-003). **Fee 200 bps** per advance; **20% of fees → first-loss reserve**, so the pool self-insures over time. Write-off waterfall: **bond → reserve → LP shares**.

**Settlement waterfall** — runs inside `acceptDelivery`: one atomic transaction repays the pool principal + fee **before any operator transfer**. This ordering is **asserted directly in the test suite** — a future reordering is a red, failing test, not a silent regression.

**AuditAnchor** — anchors an event root, a receipt root, and a delivery hash on-chain. Enough for independent verification, with the discipline that **nothing sensitive ever touches the chain in cleartext** — only hashes.

### 6.4 The milestone model — resolved by the contracts (SC-FP-003, ADR-016)

For standing pipelines, each milestone is a **fresh JobVault instance** under the same standing instruction — *forced, not chosen*: SC-FP-003 permits exactly one advance per job and the waterfall settles exactly once, so a reused job can never draw or settle again. Orchestration lives at the Brain/Worker layer, not in the contract.

The constraint is a feature: every milestone-job independently increments `acceptedJobs`, so **standing pipelines turn the flywheel faster** than one equivalent large job. (True in-contract staged release arrives with Circle Session Keys, post-GA — roadmap.)

### 6.5 WorkforceRegistry — specified, deferred to roadmap (ADR-019)

Absorbed from Teammate 2's PRD: an on-chain registry recording each Worker's **role-hash, permission-hash, and financial-policy-hash**, mutable only through the config-guard discipline (**propose → delay → diff → confirm**), never a direct setter. It would let a judge or auditor verify exactly which policy version governed a specific Worker at the moment of a specific transaction — "verify our policy engine, on-chain" — and would lean on Arc Explorer the way Billing already does.

It touches nothing in JobVault, FloatPool, or the waterfall, so it does not violate the letter of the freeze. It is deferred anyway: ADR-012's scope discipline protects *focus*, and a new contract this close to submission is new scope regardless of how safely it bolts on. Credited, specified, sequenced for immediately after submission (FR-WFR-001/002, §7.8).

### 6.6 Service discovery & the authorization boundary

Worker need = a fuzzy description, embedded and similarity-matched against Circle Agent Marketplace; highest similarity wins. **The embedding only ever decides WHICH service — never WHETHER to pay.** Payment authorization is always the deterministic policy engine (allowlist, blocked categories, per-transaction limit, budget), regardless of how the service was found. Everywhere pattern-matching appears in this system: **suggest, never authorize** (FR-DSC-001).

### 6.7 Circle / Arc adoption map (judge-facing)

| Layer | Adopts |
|---|---|
| Brain (control plane) | Circle CLI + Circle Skills |
| Funding agent | Circle Agent Wallet spend-policy (outer guard) + deterministic policy engine (inner) |
| DD-worker discovery + purchases | Circle Agent Marketplace *(discovery: **roadmap** — the shipped code embedding-matches against a local stand-in catalog, our own V2 paid API, behind a `Catalog` seam built for the marketplace; no marketplace API is integrated today)* + **Circle's Gateway Nanopayments SDK/facilitator** (`@circle-fin/x402-batching`, agents.circle.com) — **never** the generic x402.org facilitator or another vendor's (Coinbase/Stripe/Cloudflare also implement x402; judges score *Circle's* tools) |
| Compliance step | Circle Compliance Engine, direct |
| Billing agent | Arc Explorer + Gateway settlement records |
| JobVault / FloatPool / Waterfall | **Ours** — Arc supplies sub-second finality + USDC-native gas; the primitive is our own engineering |
| Idle pool capital | USYC sweep (mock behind interface if testnet-absent) |
| WorkforceRegistry (roadmap) | Arc Explorer — every policy-hash independently checkable |
| Roadmap | Session Keys (staged release), CCTP funding, Receipt NFT |

---

## 7. Functional Requirements

The v4 SRS annex remains fully in force — families **FR-ORG, FR-EVT, FR-JOB, FR-TSK, FR-MEM, FR-ACT, FR-PAY, FR-X402, FR-APR, FR-DEL, FR-AUD, FR-UI** — re-mapped to the new role names per §6.2, not replaced. The v7 line adds and amends the following.

### 7.1 Brain (new)

| ID | Requirement | Source |
|---|---|---|
| FR-BRN-001 | Brain is the sole owner interface and sole router. No agent talks to another agent directly; every message is `Agent → Brain`. | v6 architecture |
| FR-BRN-002 | Brain maintains a separate memory file per job: scope, stage/completion %, assigned Worker, every owner confirmation with timestamp, escrow/settlement state. Prevents cross-job context bleed; Billing's source of truth. | v6 |
| FR-BRN-003 | Workers receive assignments from and report to Brain only — never the owner, another agent, or Funding. A Worker never chooses its own work. | v6 |
| FR-BRN-004 | The Funding agent acts only on Brain-relayed, owner-approved instructions; it never receives raw Worker output. | v6 |
| FR-BRN-005 | The Billing agent assembles invoices only from actual on-chain data (Arc Explorer + Gateway settlement records + per-job files) and sends them to owner + paid vendor. | v6 |

### 7.2 Discovery (new)

| ID | Requirement | Source |
|---|---|---|
| FR-DSC-001 | Discovery suggests; policy authorizes. Embedding similarity selects *which* service; the deterministic policy engine alone decides *whether* to pay. | v6 |

### 7.3 Compliance (new)

| ID | Requirement | Source |
|---|---|---|
| FR-CMP-001 | The DD-worker runs a Circle Compliance Engine screen as one task step; every report carries a confidence indicator and the words "evidence, not a guarantee." | v7 merge |

### 7.4 Quality (new — Teammate 2, absorbed per ADR-017)

| ID | Requirement | Source |
|---|---|---|
| FR-QA-001 | The QA-worker reviews every deliverable pre-delivery: completeness, source coverage, unsupported-claim detection, customer-data-leakage check. Failures bounce back to the originating Worker with reasons; the deliverable stays blocked from `DeliveryReady` until revised (verified by AT-19). | Teammate 2 PRD v1.0 |

### 7.5 Policy engine (new + carried behavior)

| ID | Requirement | Source |
|---|---|---|
| FR-POL-010 | Worker manifests support **blocked spend categories**; `token-trading` and `gambling` are blocked by default — a hard floor beneath whatever else is configured. | Teammate 2 PRD v1.0 |
| (carried, v4 FR-PAY/FR-APR) | Agents submit PaymentIntents (merchant, amount, purpose, nonce) and never see keys. The deterministic engine evaluates budget, per-transaction limit, daily cumulative limit, merchant allowlist, blocked categories → **AutoApprove / HumanApprovalRequired / Deny**. Escalations land in one unified inbox; unactioned requests escalate. | v4 annex, v5/v6 laws |

### 7.6 Pipelines (new)

| ID | Requirement | Source |
|---|---|---|
| FR-PIPE-001 | Each milestone is a fresh JobVault job under the same standing instruction (forced by SC-FP-003; §6.4). | ADR-016 |
| FR-EXT-001 | Float pipelines serve external paying customers only (customer ≠ operator; ADR-013). Owner-personal pipelines never draw advances. | v7 merge review |

### 7.7 Financial backbone (amended)

| ID | Requirement | Source |
|---|---|---|
| FR-FLT-002 (**amended**) | Advance formula → the as-built simplified formula: `advance = advanceRate(org) × customerPayment`, with `rate(org) = clamp(50% + 5%·acceptedJobs − 15%·writeOffs, 30%, 85%)`; pool caps ≤10% TVL per org, ≤80% utilization; one advance per job (SC-FP-003); fee 200 bps; 20% of fees to first-loss reserve; write-off waterfall bond → reserve → LP shares. | ADR-014 — as-built, frozen; 84 green tests outrank any spec |
| FR-DEL-003 (carried) | The customer receives a magic-link portal with status + Accept + receipt; Accept fires the settlement waterfall. Satisfied by F7 (§8). Portal privacy: never exposes internal memory, prompts, policies, or other customers' jobs (Teammate 2's spec). | v4 annex + Teammate 2 |

### 7.8 Roadmap requirements (not this release — ADR-019)

| ID | Requirement | Source |
|---|---|---|
| FR-WFR-001 | WorkforceRegistry records each Worker's role-hash, permission-hash, and financial-policy-hash on-chain. | Teammate 2, via v7.2 §4.4 |
| FR-WFR-002 | Registry mutation only via the config-guard discipline (propose → delay → diff → confirm); never a direct setter. | Teammate 2, via v7.2 §4.4 |

---

## 8. User Experience Requirements (The Fabulous Layer)

UI over events the system already emits — **zero contract changes**. Owner: C. Build window: Mon 3 – Wed 5 Aug, plus integration-week spare hands. The locked five:

| # | Feature | Effort | Requirement — the point |
|---|---|---|---|
| F1 | **Live Money Graph** | 1.5d | The "watch the Snapfall" screen: fund → snap → droplets → waterfall pool-first, animated over real emitted events. Kills the "just an escrow app" first impression on sight. |
| F2 | **Snapfall Score Ring** | 0.5d | Advance rate rendered as a felt reward: 50% → 55% + "next job unlocks 13.75." The flywheel, felt. |
| F3 | **Humanized Activity Feed** | 1d | Brain/Workers presented as a named team chat; approval is a button, not JSON — the rejection beat reads as a real human decision. |
| F4 | **Hire Cards** | 0.5d | "Grow your team" over Worker manifests — makes the pluggable-Worker architecture legible to a non-technical owner without the word "manifest." |
| F7 | **Customer Magic-Link Portal** | 1d | Customer view: status + Accept + receipt; Accept literally fires the waterfall (FR-DEL-003). Privacy spec: never exposes internal memory, prompts, policies, or other jobs. |

**Deferred:** policy sliders + playground (if Aug 4 is calm) · shareable receipt card (Friday-morning garnish).

---

## 9. Non-Functional Requirements

The full catalog **NFR-001..014** remains in force in the v4 SRS annex. The v7 line makes these explicit:

| ID | Category | Requirement |
|---|---|---|
| NFR-v7-01 | Recoverability | Brain is a router with a rebuildable cache, not a state monopoly: on restart it rebuilds per-job memory by replaying the event log (supervisor restart; AT-10 extended). Contracts + event log remain the source of truth. |
| NFR-v7-02 | Privacy | Nothing sensitive (customer data, deliverable content) touches the chain in cleartext — AuditAnchor stores roots and hashes only. The customer portal never exposes internal memory, prompts, policies, or other customers' jobs. |
| NFR-v7-03 | Verifiability / auditability | Billing invoices derive only from on-chain data; waterfall ordering is asserted in the contract test suite; every demo beat maps to a real, replayable transaction visible on the explorer. |
| NFR-v7-04 | Performance | Settlement is one atomic transaction; sub-second finality and USDC-native gas are supplied by Arc (the snap must land sub-second in the demo). |
| NFR-v7-05 | Change safety | Configuration changes follow propose → delay → diff → confirm, and are simulated against the last 30 days of real transactions before going live. |
| NFR-v7-06 | Interface stability | Interface freeze Fri 24 Jul (Brain schemas + local APIs); contract ABI already frozen by virtue of being done (ADR-014). |
| NFR-v7-07 | Demo integrity | Recorded demo is a replay of real runs with live transactions and a disclosed caption — never staged narration over inert screenshots. |

---

## 10. Security & Safety Requirements

### 10.1 The five laws (structural, not aspirational)

1. **LLM proposes, deterministic code authorizes, an isolated signer executes** — enforced by capability placement, not instruction.
2. **Embeddings suggest, never authorize** — everywhere pattern-matching appears (discovery, approval triage).
3. **Per-project memory** — Brain's per-job files; no cross-job context bleed.
4. **One unified inbox, escalating** — every approval request from every pipeline (one-shot or standing) lands in a single place; unactioned requests escalate progressively.
5. **x402 stays inside Circle's facilitator** — Circle's Gateway Nanopayments SDK/facilitator only, never the generic x402.org facilitator or another vendor's.

### 10.2 Standing security requirements

- v4 threat model stands; **SEC-001..011** remain in force.
- Funding authorization is layered twice — Circle Agent Wallet spend-policy (outer) + deterministic policy engine (inner); either layer alone blocks a bad instruction.
- Blocked spend categories (`token-trading`, `gambling` by default) are a hard floor, not a preference (FR-POL-010).
- Mechanically verified: Worker→Funding direct call impossible (AT-16); x402 verifiably uses Circle's facilitator (AT-18).

### 10.3 Specific risk treatments

See §14 for the full register: approval fatigue, compliance-read-as-guarantee, misconfiguration repeats, Brain single point of failure, Funding/Billing added scope.

---

## 11. Acceptance Criteria & Test Plan

### 11.1 Contract suite (baseline — green, frozen)

- **84 tests green** across JobVault, FloatPool, AuditAnchor. The waterfall ordering (pool principal + fee before operator) is asserted in tests — a reordering fails red.
- Per ADR-014, no contract reopens before submission short of a fund-loss bug.

### 11.2 System acceptance tests

AT-01..15 remain in force from the v4 SRS annex, with AT-10 extended (restart recovery now includes Brain rebuild from the event log). New in the v7 line:

| ID | Acceptance test |
|---|---|
| AT-16 | A Worker → Funding direct call is impossible (no channel exists). |
| AT-17 | A milestone forces a fresh job; a second advance attempt on a prior job reverts. |
| AT-18 | The x402 flow verifiably uses Circle's own facilitator (not generic x402.org, not another vendor). |
| AT-19 | A QA rejection bounces the deliverable and blocks `DeliveryReady` until revised. |

### 11.3 Release gate (hardening week, Thu 6 – Fri 7 Aug)

- AT-01..19 green; 84 contract tests green.
- Restart recovery demonstrated, including Brain.
- Secret audit clean.
- Full reset rehearsed twice (reset ×2).
- Daily spine runs have been green since Wed 29 Jul.
- Recording integrity: replay of real runs, live transactions, disclosed caption. Record Thu; edit + deck + README Fri (**Teammate 2 owns final README/deck review**).
- Submission Sat 8 Aug, evening IST; all links verified incognito. Sun 9 Aug = AoE contingency only, not build time.

### 11.4 Requirement-to-test traceability *(proposed — mined from the Kimi formalization, standup ratification required)*

Canonical IDs only; the invented AT-20/AT-21 from the source material are deliberately excluded.

| Requirement / rule | Verified by |
|---|---|
| FR-BRN-001 / FR-BRN-003 — Brain is sole router; Workers report to Brain only | AT-16 (a Worker → Funding direct call is impossible) |
| FR-BRN-004 — Funding acts only on Brain-relayed, owner-approved instructions | AT-16 + AT-05 approval-hash binding (v4 annex) |
| FR-PIPE-001 / ADR-016 — fresh JobVault job per milestone | AT-17 (second advance on a prior job reverts; SC-FP-003 contract tests) |
| Law 5 / §6.7 — x402 stays inside Circle's facilitator | AT-18 |
| FR-QA-001 — QA bounce blocks `DeliveryReady` until revised | AT-19 |
| NFR-v7-01 — Brain restart rebuilds from the event log | AT-10 (extended) |
| FR-FLT-002 (amended) — advance formula, 10%/80% caps, 200 bps fee, reserve cut | Frozen contract suite (rate, caps, fee, reserve tests) |
| Settlement waterfall ordering — pool principal + fee before operator | Frozen contract suite (ordering asserted; reordering fails red) |
| FR-APR-004 / AT-05 — substitution defence (hash + live merchant/price/asset equality) | Sidecar service tests on `main` (hash mismatch, merchant swap, price-exceeds-reserved) |
| SEC-009 — kill switch stops payments and advances ≤1 s | AT-09 |

---

## 12. Timeline, Milestones & Operating Rules

### 12.1 Phase plan (today: Tue 21 Jul, contracts DONE)

| Dates | Phase | Exit criteria |
|---|---|---|
| **Tue 21 – Wed 22 Jul** | Money moves | **C: full x402 buyer loop against our paid API via Circle's facilitator — TONIGHT; all-three swarm Wed if red.** A: indexer skeleton on deployed contracts. B: Brain skeleton — router + per-job files + owner chat. |
| Thu 23 – Sun 26 Jul | Brain kernel | Brain routes a stub DD job end-to-end. Funding wraps signer; Billing formats from indexer. **Interface freeze Fri 24 Jul** (Brain schemas + local APIs; ABI already frozen by being done). |
| Mon 27 Jul – Sun 2 Aug | The spine, live | Full DD spine on testnet incl. compliance step + QA pass + rejection beat + portal accept + fall + ring. **Daily spine runs from Wed 29 Jul.** |
| Mon 3 – Wed 5 Aug | Fabulous + generalize | F1–F4 + F7; Build-Monitor beat (fresh-job-per-milestone); approval digest if calm. |
| Thu 6 – Fri 7 Aug | Hardening + story | AT-01..19 green; restart recovery incl. Brain; secret audit; reset ×2; **record Thu; edit + deck + README Fri** (Teammate 2 owns final README/deck review). |
| **Sat 8 Aug** | **SUBMIT** | Evening IST; verify links incognito. Sun 9 = contingency only (AoE). |
| Thu 20 Aug | Demo Day | — |

### 12.2 Team allocation

- **A — Contracts & Chain:** indexer on deployed contracts, then integration.
- **B — Runtime & Brain:** Brain kernel (router, per-job files, owner chat), Worker slots, Funding/Billing wrappers.
- **C — Payments, Frontend & Story:** x402 buyer loop (first, tonight), Fabulous Layer (F1–F4, F7), portal, recording, deck.

### 12.3 Scope-cut order and never-cut list

**Cut in this order if time runs short** (pre-agreed, so no stressful real-time argument): approval digest → hire cards → USYC mock stays mock → build-monitor beat compresses to 15 s → F4.

> *Source note: the v7.1 cut order lists "hire cards" and "F4" as separate entries, but F4 **is** Hire Cards (§8). Reproduced verbatim from the Constitution; treated as one item in practice.*

**Never cut:** the snap, the fall, the rejection beat, the QA beat, the portal accept, the money graph, the $0-start.

### 12.4 Operating rules

- **Rule #8 — disagree and commit.** Vote once; then all three row the same direction.
- **Rule #9 — PRD freeze (binding).** No new PRDs, from anyone, for any reason. The Constitution is the canonical file; any change is a small PR against it, reviewed at standup. A new document is a rule violation, not a contribution. (This formal edition reorganizes ratified content only; it introduces no decisions and does not amend the canon.)

---

## 13. Dependencies & Platform Integrations

| Dependency | Used by | Notes / fallback |
|---|---|---|
| Arc (chain) | All contracts, settlement, explorer | Sub-second finality; USDC-native gas. The primitive (JobVault/FloatPool/waterfall) is our own engineering on top. |
| Circle CLI + Circle Skills | Brain control plane | Agent-native command interface for the dispatch role. |
| Circle Agent Wallet | Funding agent outer guard | Spend-policy (allowlists, time-bound limits) enforced at the wallet layer. |
| Circle Agent Marketplace | Worker service discovery | Embedding similarity match against the catalog. **Status: roadmap** — shipped discovery matches a local stand-in catalog (our own V2 paid API) behind a `Catalog` seam; the marketplace slots into that seam, it is not integrated today. |
| Circle Gateway Nanopayments SDK / facilitator (`@circle-fin/x402-batching`, agents.circle.com) | DD-worker purchases (x402) | **Circle's facilitator only** — never generic x402.org, never another vendor's implementation (AT-18). |
| Circle Compliance Engine | DD-worker compliance step | Direct integration; folded into the DD-worker as one task step. |
| Arc Explorer + Gateway settlement records | Billing agent; audit story | Invoice source data; WorkforceRegistry (roadmap) leans on Explorer the same way. |
| USYC | Idle pool capital sweep | Mock behind interface if the testnet integration is unavailable in time (stays mock per cut order). |
| Circle Session Keys | Roadmap: in-contract staged release | Post-GA; removes the fresh-job-per-milestone workaround. |
| CCTP | Roadmap: funding | Post-submission. |
| **Demo pool seeding** | The snap beat (§5.1 step 3) | **FloatPool TVL ≥ 125 USDC required** for the 12.50 advance under the 10% per-org exposure cap; seed **150** (V12 seed script). Under-seeding reverts `CapExceeded` at the demo's most important moment. |

---

## 14. Risks & Mitigations

| Risk | Mitigation | Priority |
|---|---|---|
| **Approval fatigue** — several standing pipelines produce enough daily approval pings that the owner starts rubber-stamping, quietly eroding the human-checkpoint safety benefit. | Similarity triage: each new request is embedded against the owner's own past decisions; high-similarity-to-always-approved patterns batch into a **daily digest** with one-tap "always allow this pattern"; novel/low-confidence requests still interrupt immediately. The policy engine still authorizes either way (§6.6 boundary). | P1 |
| **Compliance read as a guarantee** | Explicit confidence indicator + the words **"evidence, not a guarantee"** on every report (FR-CMP-001). A disclosure fix, not a modeling one. | Shipping |
| **Misconfiguration repeats** — a config error silently becomes systemic. | Config changes: propose → delay → diff → confirm; simulate against the last 30 days of real transactions before going live — "what would this change actually have done" is answered before it's answered the hard way. | Shipping |
| **Brain single point of failure** | Supervisor restart from event log + per-job files (AT-10 extended); Brain is a router with memory, not a state monopoly — contracts + log remain truth. | Shipping |
| **Funding/Billing added scope** | Both are thin deterministic services, not LLM agents — Funding wraps the existing signer; Billing formats indexer data. Days, not weeks. | Shipping |
| **Schedule slip** | Pre-agreed scope-cut order (§12.3); never-cut list protects the pitch; daily spine runs from 29 Jul surface drift early; Sun 9 Aug AoE buffer. | Standing |

---

## 15. Architecture Decision Records (ADRs)

| ADR | Decision | Why |
|---|---|---|
| ADR-011 | Brain hub-and-spoke adopted; four-agent mesh retired | Stronger isolation, simpler routing, better pitch. |
| ADR-012 | Demo = 1 spine + 1 generalization beat | One unforgettable loop beats six shallow ones. |
| ADR-013 | External-customer rule wherever Float is involved | Customer ≠ operator makes the flywheel financing, not theater. |
| ADR-014 | Contracts frozen as built | 84 green tests outrank spec nostalgia; only a fund-loss bug reopens a contract file. |
| ADR-015 | DeFi restored to primary track | FloatPool is a genuine financial mechanism; demoting discards judging surface. |
| ADR-016 | Fresh job per milestone | Forced by SC-FP-003; makes standing pipelines flywheel accelerators. |
| ADR-017 | Absorb Teammate 2's QA-worker, blocked categories, portal privacy spec, revision paths — decline the Float cut and mesh revert from the same source | The former sharpens the product; the latter un-builds shipped work and deletes the differentiator. |
| ADR-018 | Rule #9 — PRD freeze | Five documents and ~5,000 lines existed; the winning version of this team stopped writing new documents and makes small reviewed PRs against one file. |
| ADR-019 | WorkforceRegistry absorbed to the **roadmap**, not the MVP | Additive and safe to build doesn't make it in-scope this close to submission — new scope is new scope regardless of how little it can break. Credited, specified, sequenced for immediately after submission. |

---

## 16. Roadmap (Post-Submission)

| Item | Notes |
|---|---|
| WorkforceRegistry (FR-WFR-001/002) | First post-submission item (ADR-019); on-chain policy-hash registry, config-guard mutation, Arc Explorer-verifiable. |
| Receipt / Reputation NFT | Minted on every settlement — a natural extension of data the system already produces. |
| Circle Session Keys → in-contract staged release | Post-GA; removes the fresh-job-per-milestone workaround. |
| Owner-personal pipelines (ETH Watcher, Portfolio Signal) | Return once a non-Float mechanism is designed for them (the External-Customer Rule stands). |
| Treasury Transparency Report | Future SKU. |
| Third-party LPs + richer underwriting + TEE-attested scoring | Real credit market on the FloatPool primitive. |
| EURC / StableFX multi-currency | Multi-currency support. |
| Confidential advances | Via Arc opt-in privacy, once mature. |
| CCTP funding | Cross-chain funding path. |

---

## 17. Assumptions & Open Items

### 17.1 Assumptions

- The v4 SRS annex (FR/SC/SEC/NFR/AT detailed IDs) remains authoritative where this document references it; its contents are not reproduced here.
- Contracts are done and frozen; all remaining work is runtime, frontend, and story.
- An external customer (or a credible stand-in verifiably distinct from the operator) participates in the demo — the External-Customer Rule applies to the demo itself.
- USYC testnet integration may be unavailable; the sweep ships mocked behind an interface if so (already the third item in the cut order).

### 17.2 Open items

Resolved: the milestone ↔ JobVault mapping (v6's open item) is resolved by SC-FP-003 (§6.4). The approval-fatigue digest is P1 and first in the cut order; policy sliders/playground and the shareable receipt card are conditional garnish.

Open *(added by the Kimi-formalization mining pass — each needs a standup ruling)*:

| # | Open question |
|---|---|
| OQ-A | **WorkforceRegistry / ADR-019 provenance.** v7.1 lists exactly eight ADRs and claims "zero new decisions"; ADR-019 and FR-WFR-001/002 exist only in the v7.2 line. Ratify ADR-019 explicitly (one standup nod) or mark it pending. Roadmap placement is unaffected either way. |
| OQ-B | **The "84 tests green" figure is stale.** The five contract test files on disk contain ~102 `function test` declarations (Advance 18, FloatPool 19, JobVault 30, Waterfall 24, Wiring 11). The frozen suite is *larger* than advertised — good news, but the number is cited in three normative places and should be corrected once, everywhere. |
| OQ-C | **Event-name drift vs the frozen ABI.** The H1 handshake schema says `DeliverySet` and `RateUpdated`; the frozen contracts emit `DeliverySubmitted` and `RateChanged` (and also `AdvanceRepaid`, absent from the H1 list). The contract names are frozen and win; WORK-SPLIT.md is corrected in this PR — indexer (A2) and Score Ring (V11) consume the corrected names. |

---

## Appendix A — Demo Script v3.1 (3:00)

| Time | Beat | On screen |
|---|---|---|
| 0:00–0:15 | Thesis | Treasury 0.00. "Zero dollars, one customer. Watch it finance itself." |
| 0:15–0:30 | Brain scopes | Owner asks; Brain proposes scope + 25 USDC quote; owner confirms; customer funds vault (explorer flash). |
| 0:30–0:45 | **The snap** | 12.50 lands sub-second; Money Graph animating. "Capital in a snap." |
| 0:45–1:10 | Autonomous work | Brain → DD-worker; Marketplace discovery; $0.04 x402 buy auto-approved (Circle facilitator visible); Compliance screen passes — "evidence, not a guarantee." |
| 1:10–1:30 | Human control | $4.00 escalates → owner REJECTS in the feed → worker adapts, $0.06 alternative. "The workforce can't embezzle itself." |
| 1:30–1:50 | QA + delivery | QA-worker bounces one unsupported claim → Delivery revises → hash → customer opens magic link → **Accept**. |
| 1:50–2:10 | **The fall** | "Watch the Snapfall" — one tx, pool 12.75 first, operator remainder; explorer proof. |
| 2:10–2:25 | Flywheel | Ring 50% → 55%; "next job unlocks 13.75." |
| 2:25–2:50 | Generalization | Build-Monitor: milestone → completion % → fresh job cycles the same rails. "Different work. Same Brain. Same waterfall." |
| 2:50–3:00 | Close | Adoption map flash. "Snapfall, built on Arc. The first AI business that finances itself." |

### A.1 Per-beat replayable evidence *(proposed — mined from the Kimi formalization, standup ratification required)*

What each beat leaves behind for the recording-integrity rule (NFR-v7-07) and judge Q&A. Honest note: the rejection in beat 5 is deliberately an **off-chain** human decision (inbox escalation + rejected PaymentIntent record); its on-chain half is the $0.06 replacement settlement. The opening balance read and the close carry no new transaction.

| Beat | Replayable evidence |
|---|---|
| Thesis | Treasury balance read: 0.00 (explorer address view) |
| Brain scopes + fund | `JobCreated` + `JobFunded` events; 25 USDC escrow tx |
| The snap | `AdvanceIssued` event; 12.50 transfer to treasury, sub-second |
| Autonomous work | x402 settlement for 0.04; payment receipt with request/response hashes; compliance screen record |
| Human control | Inbox escalation + rejected PaymentIntent (off-chain, by design); $0.06 replacement x402 settlement (on-chain half) |
| QA + delivery | Bounce-with-reasons record; revised deliverable; `DeliverySubmitted` event with content hash |
| The fall | One atomic tx: `AdvanceRepaid` (12.75 to pool) then operator transfer; `JobSettled(advanceRepaid, operatorNet)` |
| Flywheel | `RateChanged` event; `advanceRate()` read: 5000 → 5500 bps |
| Generalization | Fresh `JobCreated`/`JobFunded`/`AdvanceIssued` cycle for the milestone job |
| Close | No new transaction (adoption-map slide) |

## Appendix B — Source Consolidation & Credit Map

Every component's origin, credited — nothing in this PRD is uncredited or silently folded in:

| Component | Source | Status |
|---|---|---|
| Self-financing economics (escrow → advance → waterfall → flywheel) | v4 lineage (Gnanasekaran + research) | The crown jewel; the demo spine |
| Brain hub-and-spoke, Funding/Billing isolation, per-project memory | v6 (Teammate 1's architecture) | The skeleton of everything |
| Contracts as-built (simplified formula, caps-based), 84 tests | Teammate 1's build | Accepted and frozen (ADR-014) |
| Circle-facilitator-only x402 rule, adoption map, Session Keys deferral | v6 | Adopted verbatim |
| Discovery-by-embedding + "suggest, never authorize" | v6 | Adopted |
| Approval-fatigue digest | v6 | Adopted (P1) |
| Quality Reviewer worker, blocked spend categories, portal privacy spec, revision paths | Teammate 2's PRD v1.0 | Absorbed (ADR-017) |
| WorkforceRegistry | Teammate 2's PRD v1.0 | Absorbed to roadmap (ADR-019) |
| Six pipelines | v6 | Cut to 1+1 for demo (ADR-012); rest = slide-ware/roadmap |
| External-customer rule | v7 merge review | Binding (ADR-013) |
| Fabulous Layer (5 locked UX features) | Fabulous Layer session | §8, owner: C |
| Milestone ↔ JobVault mapping | v6 open item | Resolved by SC-FP-003 (§6.4) |
| Calendar, ownership, operating rules | v4 + Execution Plan | Updated to contracts-done reality (§12) |

## Appendix C — Glossary

| Term | Meaning |
|---|---|
| **The snap** | The FloatPool advance landing the moment a job is funded (12.50 USDC at 50% on the demo job). |
| **The fall / waterfall** | The atomic settlement on acceptance: pool repaid principal + fee first, operator remainder last, one transaction. |
| **Float** | Working-capital advance against an on-chain escrowed receivable; priced by delivery history. |
| **Flywheel** | The advance-rate state variable rising with accepted jobs (and falling with write-offs); verifiable on-chain. |
| **Brain** | The single owner-facing agent: scoper, router, monitor, per-job memory keeper. |
| **Worker** | Bounded single-task executor in a pluggable slot (DD-worker, Build-Monitor-worker, QA-worker). |
| **Funding agent** | The isolated signer; the only component that can move money; Brain-relayed, owner-approved instructions only. |
| **Billing agent** | Invoice assembler reading only actual on-chain data. |
| **PaymentIntent** | An agent's structured spend proposal (merchant, amount, purpose, nonce); never key access. |
| **Manifest** | A Worker's declared policy surface: allowlist, limits, budget, blocked categories. |
| **JobVault** | Escrow + job-lifecycle contract (Created → Funded → InProgress → Delivered → Accepted). |
| **FloatPool** | ERC-4626-style advance vault; ≤10% TVL per org, ≤80% utilization, one advance per job, 200 bps fee. |
| **AuditAnchor** | On-chain anchor of event root, receipt root, delivery hash — hashes only, never sensitive cleartext. |
| **WorkforceRegistry** | Roadmap on-chain registry of Worker role/permission/policy hashes (ADR-019). |
| **x402** | HTTP-native payment protocol; Snapfall uses Circle's Gateway Nanopayments facilitator exclusively. |
| **bps** | Basis points; 200 bps = 2% (0.25 USDC on the demo job's 12.50 advance). |
| **TVL** | Total value locked in the pool. |
| **AoE** | Anywhere on Earth — the submission-buffer timezone convention for Sun 9 Aug. |
| **AT / FR / NFR / SEC / SC** | Acceptance test / functional / non-functional / security / smart-contract requirement families (detailed IDs in the v4 SRS annex). |

## Appendix D — Data Schemas *(proposed — input to the Fri 24 Jul interface freeze; standup ratification required)*

The four record shapes the Brain runtime, policy engine, sidecar, and dashboard exchange. The PaymentIntent and approval-token shapes below are **already shipped in code on `main`** (`sidecar/src/h3.ts`, H2/H3 handshake surface) — freezing them here costs nothing and prevents drift. Amounts are atomic-USDC decimal strings (6dp).

**PaymentIntent (wire form; H3 `pay` input, policy-engine output):**
`intentId, jobId, taskId, agentId, resource, network, asset, merchant, amount, maxAmount, purpose, nonce, decision (AUTO_APPROVE | HUMAN_APPROVED | HUMAN_APPROVAL_REQUIRED | DENY), policyVersion, createdAt, expiresAt` — all strings. The 14-field canonical subset (excluding `decision`, `createdAt`) is keccak256-hashed as `intentHash`; approval binds to that hash (AT-05).

**Approval token (H2 decision object, consumed unchanged by H3 `pay`):**
`intentHash, decision, approvedAmount, approver, policyVersion, issuedAt, expiresAt, signature` — HMAC-SHA256 over `intentHash|decision|approvedAmount|expiresAt`, lowercase hex.

**Brain per-job memory file (FR-BRN-002):**
`jobId, scope, stagePercent, assignedWorker, ownerConfirmations[] (each with timestamp), escrowState, settlementState` — rebuilt from the event log on restart (NFR-v7-01).

**Worker manifest (policy surface, FR-POL-010):**
`role, model, memoryNamespace, filesystemScope, commandAllowlist, networkAllowlist, budgetUsdc, perTxLimitUsdc, blockedCategories (default: token-trading, gambling), escalatesTo` — validated deterministically before activation; `can_sign_payments`/`can_request_advance` are structurally false for every Worker.

---

*Formalized 22 July 2026 from the ratified v7-line documents. No new decisions. Everything after the Constitution is still commits.*

**One founder. A workforce that can't embezzle itself. A business that gets cheaper to run every time it delivers.**
