# SNAPFALL — v7.1 THE CONSTITUTION

## One Brain. One Loop. One Waterfall. The Self-Financing AI Workforce, Built on Arc.

**Definitive merged PRD — v7.1 · Tuesday 21 July 2026 · supersedes v4 (scope/schedule), v6 (adopted with amendments), teammate PRD v1.0 (absorbed with credit), and all drafts. v4 remains the SRS annex for detailed IDs (FR/SC/SEC/NFR/AT) except where amended below. Under Rule #9, this is the LAST document — all future changes are PRs against this file.**

| Field | Value |
|---|---|
| Product | **Snapfall** — capital in a snap, settlement in a waterfall |
| Tracks | Agentic Economy (primary) **+ DeFi (primary — ADR-015)** |
| Contracts | **DONE.** JobVault, FloatPool, AuditAnchor — **84 tests green. FROZEN** (ADR-014) |
| Submission | Saturday 8 August 2026 (buffer Sun 9 Aug, AoE) · Demo Day Thursday 20 Aug |
| Team | A: Contracts & Chain → indexer + integration · B: Runtime & Brain · C: Payments, Frontend & Story |

---

## 0. The Merge Map — every idea, credited

This is not a new idea. It is the best version of every idea this team had, welded together. Everyone's fingerprints are on it:

| Component | Source | Status in v7.1 |
|---|---|---|
| Self-financing economics (escrow → advance → waterfall → flywheel) | v4 lineage (Gnanasekaran + research) | **The crown jewel. The demo spine.** |
| Brain hub-and-spoke, Funding/Billing isolation, per-project memory | v6 (Teammate 1's architecture) | **The skeleton of everything** |
| Contracts as-built (simplified formula, caps-based), 84 tests | Teammate 1's build | **Accepted and frozen** (ADR-014) |
| Circle-facilitator-only x402 rule, adoption map, Session Keys deferral | v6 | Adopted verbatim |
| Discovery-by-embedding + "suggest, never authorize" | v6 | Adopted |
| Approval-fatigue digest | v6 | Adopted (P1) |
| **Quality Reviewer worker, blocked spend categories, portal privacy spec, revision paths** | **Teammate 2's PRD v1.0** | **Absorbed** (ADR-017) |
| Six pipelines | v6 | Cut to 1+1 for demo (ADR-012); rest = slide-ware/roadmap |
| External-customer rule | v7 merge review | Binding (ADR-013) |
| Fabulous Layer (5 locked UX features) | Fabulous Layer session | §8, owner: C |
| Milestone ↔ JobVault mapping | v6 open item | **Resolved by SC-FP-003** (§4.3) |
| Calendar, ownership, operating rules | v4 + Execution Plan | Updated to contracts-done reality (§11) |

---

## 1. Thesis

Snapfall is a local-first AI workforce for a solo founder. One shared **Brain** talks to the owner, scopes and routes every job, and watches every pipeline at once. Specialized **Workers** execute tasks but never touch money and never talk to the owner. A **Funding agent** — the isolated signer — moves money only on Brain-relayed, owner-approved instructions. A **Billing agent** turns every settlement into a real invoice from actual on-chain data. Underneath sits one financial engine: **JobVault** escrows the customer's payment, **FloatPool** advances working capital against it the moment the job is funded, and the **settlement waterfall** repays the pool — principal + fee first, operator profit last — in one atomic Arc transaction. Every accepted job raises the org's on-chain advance rate.

**One line:** *One founder. A workforce that can't embezzle itself. A business that gets cheaper to run every time it delivers.*

**Demo thesis:** *"We gave our AI business zero dollars and one customer. Watch it finance itself."*

## 2. The Two Gaps + The One Rule

**The spending gap.** An agent buying data/compute either has unrestricted wallet access (one hallucination from treasury loss) or needs human approval per micro-purchase (autonomy dies). → Deterministic policy engine: AutoApprove / HumanApprovalRequired / Deny; agents submit PaymentIntents, never see keys. Manifests carry allow **and blocked** spend categories (e.g. `token-trading`, `gambling` — Teammate 2's addition).

**The working-capital gap.** A funded job still requires paying for the work before payment releases. Human SMEs factor invoices (slow, manual, gated); autonomous businesses have no credit history a bank recognizes. → Float: the receivable is already escrowed on-chain, so payment risk ≈ 0; only performance risk remains, priced by a pure on-chain function of delivery history:
`rate(org) = clamp(50% + 5%·acceptedJobs − 15%·writeOffs, 30%, 85%)`

**THE EXTERNAL-CUSTOMER RULE (binding, ADR-013).** Wherever Float is involved, the pipeline serves an **external paying customer**. The flywheel requires customer ≠ operator — borrowing against money you deposited yourself is theater, not financing. Owner-personal pipelines (ETH Watcher, Portfolio Signal on the owner's own book) are **roadmap** and never draw advances.

## 3. The Brain Architecture (from v6)

One loop, four fixed roles, one law: **no agent ever talks to another agent directly.** Every message is `Agent → Brain`; Brain decides what happens next.

```
Owner ⇄ Brain ⇄ Worker (pipeline-specific, pluggable slot; incl. QA-worker)
              ⇄ Funding agent (isolated signer → contracts)
              ⇄ Billing agent (→ Owner + Vendor invoices)
```

**Brain** — the only agent the owner talks to. Scopes each request, presents scope for owner confirmation, assigns the correct Worker (a Worker never chooses its own work), monitors every pipeline, and keeps a **separate memory file per job**: scope, stage/completion %, assigned Worker, every owner confirmation with timestamp, escrow/settlement state. Prevents cross-job context bleed; Billing's source of truth. Control plane: Circle CLI + Circle Skills.

**Worker** — bounded single-task executor. Receives exactly one assignment from Brain, executes, reports back to Brain. Never contacts the owner, another agent, or Funding. Worker types are the pluggable slot; the loop never changes. **QA-worker (Teammate 2's role, absorbed):** before delivery, Brain routes the draft through a Quality Reviewer pass — completeness, source coverage, unsupported-claim detection, customer-data-leakage check; failures bounce back for revision with reasons.

**Funding agent** — the only component that can move money, only on Brain-relayed owner-approved instructions. Never receives raw Worker output. Outer guard: Circle Agent Wallet spend-policy. Inner logic: our deterministic policy engine. Executes against the contracts.

**Billing agent** — assembles invoices from **actual transaction data** (never a summary that could drift from chain state); sends to owner + paid vendor. Sources: Arc Explorer + Gateway settlement records + Brain's per-job files.

**Why this shape:** a single agent that reasons about a job AND releases funds is one prompt-injection from unrecoverable loss. Role-splitting means a fully compromised Worker can produce, at worst, a bad report — no channel to money exists. Enforced by capability placement, not instruction.

**Mapping from v4:** Manager → Brain. Research/Delivery/Quality → Worker slots. Finance-Controller → Funding agent (execution) + policy engine (authorization, unchanged). All v4 FR-ACT/FR-PAY/FR-APR/SEC requirements remain in force under new role names.

## 4. The Financial Backbone — as built, frozen

### 4.1 Contracts (84 tests green — DONE)

**JobVault:** Created → Funded → InProgress → Delivered → Accepted; bounded operating expenses; content-hashed delivery before acceptance; refund/cancel paths notify FloatPool. Revision/expired/disputed handling per v4 annex P1s (confirmed by Teammate 2's spec).

**FloatPool:** ERC-4626-style vault. On Funded, the org may draw `advance = advanceRate(org) × customerPayment` — as-built simplified formula; the caps that matter are the pool's own: **≤10% TVL per org, ≤80% utilization.** One advance per job (SC-FP-003). Fee 200 bps; 20% of fees → first-loss reserve. Write-off waterfall: bond → reserve → LP shares.

**Settlement waterfall:** runs inside `acceptDelivery`, one atomic transaction, pool repaid principal+fee before any operator transfer — **asserted in tests; a reordering is a failing test, not a silent bug.**

**AuditAnchor:** anchors event root, receipt root, delivery hash. Nothing sensitive on-chain, ever.

### 4.2 Freeze declaration (ADR-014)

As-built formula and single-advance/single-settlement model ACCEPTED. 84 green tests outrank any spec — including every PRD any of us wrote. No contract reopens before submission short of a fund-loss bug.

### 4.3 Milestones — v6's open item, RESOLVED by the contracts

Fresh job per milestone — **forced, not chosen**: SC-FP-003 permits exactly ONE advance per job and the waterfall settles exactly once, so a reused job can never draw or settle again. Each milestone = a fresh JobVault instance under the same standing instruction, orchestrated at the Brain/Worker layer. Bonus: every milestone independently increments `acceptedJobs` — **standing pipelines turn the flywheel faster.** (True in-contract staged release: Circle Session Keys, post-GA — roadmap.)

## 5. Demo Scope — 1 + 1 (ADR-012)

One crown-jewel loop + one 30-second generalization proof. Everything else is a slide of pluggable Worker slots.

### 5.1 The spine — Due Diligence, sold to an external customer

An external customer pays 25 USDC for a due-diligence/market report. Escrow → $0 treasury → **snap** (12.50 at 50%) → Brain scopes, owner confirms → DD-worker assigned → Agent Marketplace discovery (embedding match) → buys sources via **Circle's Gateway x402** ($0.04 auto-approved) → **compliance step folded in** (Circle Compliance Engine screen as one task step; report carries "evidence, not a guarantee" + confidence) → the $4.00 **rejection beat** → cheaper source ($0.06) → **QA-worker pass** (bounces one unsupported claim; Delivery revises — the "workforce polices itself" beat) → delivery hash → customer accepts in the magic-link portal → **the fall** (12.75 to pool first, remainder to operator, one tx) → rate ring 50% → 55%.

### 5.2 The generalization beat — Project-Build Fund Monitor (≤30 s)

The escrow-native standing pipeline: a client pays a contractor through Snapfall; Build-Monitor-worker watches the repo; on a fund request it reports completion % to Brain **before** any release; each milestone = a fresh JobVault cycle (§4.3) through the identical loop. Line: *"Different work. Same Brain. Same rails. Same waterfall."*

### 5.3 Cut from demo → slide-ware + roadmap

ETH Accumulation Watcher (owner-personal, no escrow, DCA-bot silhouette — roadmap) · Portfolio Signal (owner-personal — roadmap) · Treasury Transparency Report (future SKU — slide) · Compliance as standalone pipeline (folded into DD-worker as a step).

## 6. Service Discovery + The Boundary

Worker need = fuzzy description, embedded and matched against Circle Agent Marketplace; highest similarity wins. **The embedding only decides WHICH service — never WHETHER to pay.** Payment authorization is always the deterministic policy engine (allowlist, blocked categories, per-tx limit, budget). Everywhere pattern-matching appears: **suggest, never authorize.**

## 7. Circle / Arc Adoption Map (judge-facing)

| Layer | Adopts |
|---|---|
| Brain (control plane) | Circle CLI + Circle Skills |
| Funding agent | Circle Agent Wallet spend-policy (outer) + deterministic policy engine (inner) |
| DD-worker discovery + purchases | Circle Agent Marketplace + **Circle's Gateway Nanopayments SDK/facilitator** (`@circle-fin/x402-batching`, agents.circle.com) — **never** the generic x402.org facilitator or another vendor's (Coinbase/Stripe/Cloudflare also implement x402; judges score *Circle's* tools) |
| Compliance step | Circle Compliance Engine, direct |
| Billing agent | Arc Explorer + Gateway settlement records |
| JobVault / FloatPool / Waterfall | **Ours** — Arc supplies sub-second finality + USDC-native gas; the primitive is our own |
| Idle pool capital | USYC sweep (mock behind interface if testnet-absent) |
| Roadmap | Session Keys (staged release), CCTP funding, Receipt NFT |

## 8. The Fabulous Layer (locked five, owner: C)

UI over events we already emit. Zero contract changes. Aug 3–5 window + integration-week spare hands.

| # | Feature | Effort | The point |
|---|---|---|---|
| F1 | **Live Money Graph** | 1.5d | The "watch the Snapfall" screen: fund → snap → droplets → waterfall pool-first. Kills "just escrow" on sight. |
| F2 | **Snapfall Score Ring** | 0.5d | 50% → 55% + "next job unlocks 13.75." The flywheel, felt. |
| F3 | **Humanized Activity Feed** | 1d | Brain/Workers as a named team chat; approval is a button, not JSON. |
| F4 | **Hire Cards** | 0.5d | "Grow your team" over Worker manifests — the adoption metaphor. |
| F7 | **Customer Magic-Link Portal** | 1d | Status + Accept + receipt; the customer's Accept literally fires the waterfall. Owed anyway (FR-DEL-003). Portal never exposes internal memory, prompts, policies, or other jobs (Teammate 2's spec). |

Deferred: policy sliders + playground (if Aug 4 is calm) · shareable receipt card (Fri-morning garnish).

## 9. Requirements Delta (v4 SRS annex remains in force)

**New:** FR-BRN-001 Brain is the sole owner interface and sole router · FR-BRN-002 per-job memory files with timestamped confirmations · FR-BRN-003 Workers receive from and report to Brain only · FR-BRN-004 Funding acts only on Brain-relayed owner-approved instructions · FR-BRN-005 Billing invoices only from on-chain data · FR-DSC-001 discovery suggests; policy authorizes · FR-CMP-001 DD-worker runs a Compliance Engine screen as a step; "evidence, not a guarantee" + confidence on every report · **FR-QA-001 QA-worker reviews every deliverable pre-delivery (completeness, sources, unsupported claims, leakage); failures bounce with reasons (Teammate 2)** · **FR-POL-010 manifests support blocked spend categories; `token-trading` and `gambling` blocked by default (Teammate 2)** · FR-PIPE-001 fresh JobVault job per milestone (§4.3) · FR-EXT-001 Float pipelines serve external customers only.

**Amended:** FR-FLT-002 advance formula → as-built (§4.1). Agent-role FRs re-map per §3. Everything else in v4 — FR-ORG/EVT/JOB/TSK/MEM/ACT/PAY/X402/APR/DEL/AUD/UI, SEC-001..011, NFR-001..014, AT-01..15 — stands. **New tests:** AT-16 Worker→Funding direct call impossible · AT-17 milestone = fresh job; second advance on prior job reverts · AT-18 x402 verifiably uses Circle's facilitator · **AT-19 QA rejection bounces the deliverable and blocks DeliveryReady until revised.**

## 10. Safety & Risk (merged)

The five v6 laws stand: (1) LLM proposes, deterministic code authorizes, isolated signer executes — by capability placement; (2) embeddings suggest, never authorize; (3) per-project memory; (4) one unified inbox, escalating; (5) x402 stays inside Circle's facilitator. v4's threat model stands. Additions:

| Risk | Mitigation |
|---|---|
| Approval fatigue | Similarity triage → daily digest + one-tap "always allow this pattern"; novel/low-confidence interrupts; policy engine still authorizes. (P1) |
| Compliance read as guarantee | Confidence indicator + "evidence, not a guarantee" on every report. |
| Misconfiguration repeats | Config change: propose → delay → diff → confirm; simulate vs last 30 days before live. |
| Brain single point of failure | Supervisor restart from event log + per-job files (AT-10 extended); Brain is a router with memory, not a state monopoly — contracts + log remain truth. |
| Funding/Billing added scope | Thin deterministic services, not LLM agents — Funding wraps the existing signer; Billing formats indexer data. Days, not weeks. |

## 11. Calendar (today: Tue 21 Jul, contracts DONE) + Rules

| Dates | Phase | Exit criteria |
|---|---|---|
| **Tue 21 – Wed 22 Jul** | Money moves | **C: full x402 buyer loop against our paid API via Circle's facilitator — TONIGHT; all-three swarm Wed if red.** A: indexer skeleton on deployed contracts. B: Brain skeleton — router + per-job files + owner chat. |
| Thu 23 – Sun 26 Jul | Brain kernel | Brain routes a stub DD job end-to-end. Funding wraps signer; Billing formats from indexer. **Interface freeze Fri 24 Jul** (Brain schemas + local APIs; ABI already frozen by being done). |
| Mon 27 Jul – Sun 2 Aug | The spine, live | Full DD spine on testnet incl. compliance step + QA pass + rejection beat + portal accept + fall + ring. **Daily spine runs from Wed 29 Jul.** |
| Mon 3 – Wed 5 Aug | Fabulous + generalize | F1–F4 + F7; Build-Monitor beat (fresh-job-per-milestone); approval digest if calm. |
| Thu 6 – Fri 7 Aug | Hardening + story | AT-01..19 green; restart recovery incl. Brain; secret audit; reset ×2; **record Thu; edit + deck + README Fri** (**Teammate 2 owns final README/deck review — cleanest writer on the team**). Recording integrity: replay of real runs, live transactions, disclosed caption. |
| **Sat 8 Aug** | SUBMIT | Evening IST; verify links incognito. Sun 9 = contingency only. |

**Scope-cut order:** approval digest → hire cards → USYC mock stays mock → build-monitor beat compresses to 15s → F4. **Never cut:** snap, fall, rejection beat, QA beat, portal accept, money graph, $0-start.

**Rule #8 — disagree and commit.** Vote once; then all three row the same direction.

**Rule #9 — PRD FREEZE (new, binding).** After tonight's vote, no new PRDs exist, from anyone, for any reason. This file is the constitution. Any change is a small PR against this file, reviewed at standup. A new document is a rule violation, not a contribution.

## 12. Demo Script v3.1 (3:00)

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

## 13. ADRs (new in v7 line)

| ADR | Decision | Why |
|---|---|---|
| ADR-011 | Brain hub-and-spoke adopted; four-agent mesh retired | Stronger isolation, simpler routing, better pitch. |
| ADR-012 | Demo = 1 spine + 1 generalization beat | One unforgettable loop beats six shallow ones. |
| ADR-013 | External-customer rule wherever Float is involved | Customer ≠ operator makes the flywheel financing, not theater. |
| ADR-014 | Contracts frozen as built | 84 green tests outrank spec nostalgia. |
| ADR-015 | DeFi restored to primary | FloatPool is a genuine financial mechanism; demoting discards judging surface. |
| ADR-016 | Fresh job per milestone | Forced by SC-FP-003; makes standing pipelines flywheel accelerators. |
| ADR-017 | Absorb Teammate 2's QA-worker, blocked categories, portal privacy spec, revision paths | Best details of the third PRD, kept; the Float cut and mesh revert, declined — they un-build shipped work and delete the differentiator. |
| ADR-018 | Rule #9 — PRD freeze | Five documents and ~5,000 lines exist; the winning version of this team stopped writing yesterday. |

## 14. Roadmap

Receipt/Reputation NFT · Circle Session Keys → in-contract staged release · owner-personal pipelines (ETH Watcher, Portfolio Signal) under the no-Float rule · Treasury Transparency Report SKU · third-party LPs + richer underwriting + TEE-attested scoring · EURC/StableFX multi-currency · confidential advances via Arc opt-in privacy.

## 15. The Close

Teammate 1's architecture routing the original economics through his own frozen, tested contracts, wearing the Fabulous Layer, quality-checked by Teammate 2's reviewer, aimed at one unforgettable three-minute story. Nobody lost an argument — the project absorbed every good idea and shed everything that diluted it.

**One founder. A workforce that can't embezzle itself. A business that gets cheaper to run every time it delivers.**

*Snapfall, built on Arc. The constitution is ratified. Everything after this file is commits.*
