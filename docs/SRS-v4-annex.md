# SNAPFALL

## The Self-Financing AI Workforce — Built on Arc

**Final Product Requirements Document (PRD) + Software Requirements Specification (SRS) — v4.1 FINAL**

| **Field** | **Value** |
| --- | --- |
| Product name | **Snapfall** (single unified brand; supersedes TabPay, DaemonWorks, Float, FloatWorks, Aruvi) |
| Credit module | Float Protocol (FloatPool contract + JobVault settlement waterfall) — technical identifiers, not brands |
| Document status | FINAL — locked for implementation. No scope additions permitted. This is the single source of truth. |
| Version | 4.1 · 19 July 2026 · supersedes all prior PRD/SRS versions and drafts |
| Event | Arc "Programmable Money" Hackathon (Encode Club × Circle) |
| Tracks | Agentic Economy (primary) + DeFi (primary) — one build, one narrative |
| Final submission | **Saturday 8 August 2026** (buffer: Sunday 9 August, AoE / UTC-12; platform locks at deadline — re-verify exact lock timestamp on the Encode platform Day 1) |
| Demo Day | Thursday 20 August 2026 |
| Team | 3 members, full-time, AI-assisted development |

**Demo thesis (first 10 seconds of the video):**
> "We gave our AI business zero dollars and one customer. Watch it finance itself."

**Brand line:** *Snapfall — capital in a snap, settlement in a waterfall.*

---

# Document Control

| **Item** | **Details** |
| --- | --- |
| Purpose | Define product scope, user value, architecture, functional and non-functional requirements, smart-contract specification, security model, acceptance criteria, team ownership, calendar, and demo plan — everything needed to build and submit. |
| Audience | The three builders, judges, mentors, future contributors. |
| Normative language | "Shall" = mandatory. "Should" = recommended. "May" = optional. |
| Scope baseline | Hackathon MVP: one revenue-generating service workflow financed by one receivables advance. Not a universal autonomous company; not a general credit market. |
| Change control | Analysis phase is closed. Scope changes require all-three agreement at standup and a version bump. Everything after this document is commits. |

## Revision History

| **Version** | **Date** | **Change** |
| --- | --- | --- |
| 1.0 | 19 Jul 2026 | DaemonWorks PRD/SRS baseline (workforce + JobVault) |
| 2.0 | 19 Jul 2026 | Unified baseline: Float receivables financing integrated end-to-end |
| 3.0 (draft) | 19 Jul 2026 | FloatWorks/Aruvi condensed execution edition |
| **4.0 FINAL** | 19 Jul 2026 | **Snapfall**: final name; full requirement depth restored; execution engine (ownership, calendar, ABI freeze) integrated; all review patches applied |
| **4.1 FINAL** | 19 Jul 2026 | Doc-research patches: circle-tools starter kit, Circle CLI + Agent Skills for AI IDEs, App Kit bridge for CCTP, EVM-differences pre-deploy check, Settlement Advance differentiation, Gas Station option, refs R12–R14 |

---

# 0. Naming and Brand Compliance

**Snapfall** encodes the product's two signature demo moments in two syllables:

- **Snap** — the sub-second working-capital advance: a zero-balance treasury receives funds in a finger-snap, made possible only by Arc's deterministic sub-second finality.
- **Fall** — the settlement waterfall: on customer acceptance, funds cascade in strict seniority — pool principal + fee first, operator profit last — in one atomic Arc transaction.

**Pitch usage:** "Snapfall — capital in a snap, settlement in a waterfall." In the demo, the settlement beat is narrated as "watch the Snapfall" so judges learn the name as a verb.

**Namespace status (verified 19 Jul 2026):** no existing company, app, or crypto project uses the word "Snapfall." Watch-item for the future SaaS only: the "Snap-" prefix lives near Snap Inc.'s trademark gravity — irrelevant at hackathon scale; revisit at commercialization.

**Brand rules (per Arc Brand Guidelines & Partner Toolkit):**
- Always "Snapfall, built on Arc" — never "Arc Snapfall"; Arc never appears in the product name.
- Your brand leads; Arc is infrastructure. Use "live on Arc testnet" phrasing.
- Do not use the Arc logo unless qualifying as an active builder; never alter it.

**Handles (grab Day 1):** GitHub org `snapfall` (fallbacks `snapfallhq`, `getsnapfall`), X handle, `snapfall.com`/`.xyz`/`.io` in that order of preference.

---

# 1. Executive Summary

Snapfall is a local-first, event-driven platform that lets a solo founder or micro-agency operate a bounded team of specialized AI employees. Each employee has a role, scoped permissions, role-specific memory, a cost centre, and an escalation path. The workforce receives a customer request, prepares a human-confirmed quote, executes a task plan, purchases approved data services through policy-controlled USDC nanopayments, produces a deliverable, and settles the customer-funded job on Arc.

Its native **Float Protocol** makes the business self-financing. The customer's payment is escrowed on-chain in the JobVault *before* work begins, so the receivable carries near-zero payment risk — only performance risk (will the deliverable be accepted?) remains. Snapfall's machinery — deterministic policies, typed actions, human approvals, cryptographic audit anchors, immutable acceptance history — is a performance-risk underwriting machine. The FloatPool reads that history to price advances: a zero-balance treasury draws instant working capital against the funded escrow, and the settlement waterfall repays the pool before the operator profits. Every accepted job raises the organization's on-chain advance rate. **The business earns its own creditworthiness, cryptographically.**

The hackathon MVP demonstrates one complete self-financed commercial loop: customer request → funded escrow → **advance (snap)** → autonomous execution → controlled purchase → human approval → delivery → **settlement waterfall (fall)** → audit anchor → **advance rate increases**. Programmable money is the protagonist, not a decorative wallet feature.

## 1.1 One-line pitch

**A private AI workforce that runs customer jobs from request to payment — and finances itself against escrowed receivables — with role-based agents, controlled USDC budgets, human approvals, self-improving on-chain credit, and an audit trail for every action and cent.**

## 1.2 Hackathon fit (verified against Encode + Arc House rules)

| **Official criterion** | **Snapfall evidence** |
| --- | --- |
| Working prototype deployed on Arc | JobVault, FloatPool, AuditAnchor live on Arc testnet; real escrow, advance, waterfall, and anchor transactions with explorer links. |
| Clear use of Circle developer tools | USDC (escrow, gas, settlement); Gateway Nanopayments + x402 (agent purchases); Circle Wallets / EOA path (treasury + agent identities); Paymaster (P1); USYC idle-pool sweep (P1, mock fallback); CCTP customer funding (P2). |
| Real use case, path to production | Invoice factoring is a trillion-scale trade-finance category; solo operators universally front execution costs before revenue. Snapfall is the team's real SaaS direction — the "still building it in six months" answer is literally yes. |
| Quality of execution over complexity | ONE job workflow, four bounded agents, one paid API, one approval, one advance, one waterfall. Each piece is simple; the composition is the innovation. |
| Submission requirements | Functional MVP (frontend + backend), public repo, 3-minute video, deck — itemized in §15 checklist. |

**Dual-track legitimacy test (from organizer guidance):** a genuine Agentic entry = an agent that observes signals, decides, controls budgeted funds, transacts, and operates within limits ✅ (four agents + policy engine). A genuine DeFi entry = a real financial mechanism: escrow, pooled funds, collateral, revenue sharing, risk allocation ✅ (FloatPool vault, LP shares, first-loss reserve, waterfall, performance bond). The stated ideal — *an autonomous agent on top of a real financial primitive* — is exactly this build.

## 1.3 Product principles

**Local-first, not offline.** Sensitive memory, files, policies, and orchestration remain local; approved network calls and blockchain interactions are expected.

**Agents propose; deterministic systems authorize.** LLMs recommend; permissions, budgets, signatures, and high-risk execution are enforced by non-LLM code.

**Credit from proof, not promises.** The advance rate is a pure on-chain function of contract-visible delivery history. No oracle. No self-reported data.

**One complete business loop beats many disconnected features.** The MVP optimizes for a credible end-to-end outcome and an unforgettable three-minute demo.

**Privacy by minimization.** Only the minimum approved payload leaves the device; customer content never touches the chain.

**Financial accountability is task-native.** Every cent ties to the agent, job, reason, policy decision, purchased resource, result, advance, and settlement.

---

# 2. Product Strategy

## 2.1 The two gaps

**Gap 1 — the spending gap.** Autonomous workers need paid data and compute. Unrestricted wallet access for an LLM is unsafe; human approval for every sub-cent purchase kills autonomy.
→ *Snapfall's answer:* a deterministic policy engine returning AutoApprove / HumanApprovalRequired / Deny with machine-readable reasons. Agents submit PaymentIntents; they never see keys.

**Gap 2 — the working-capital gap.** Even with a signed, funded job, execution costs come due before revenue is released. Human SMEs use invoice factoring — slow, manual, inaccessible below certain sizes, advancing 70–90% of invoice value against payment risk [R11]. Autonomous agent businesses have nothing at all.
→ *Snapfall's answer:* the Float Protocol. The receivable is already escrowed on-chain, so payment risk ≈ 0; only performance risk remains, and the advance rate is a pure on-chain function of cryptographic delivery history.

**Advance-rate function (the flywheel):**
`rate(org) = clamp(50% + 5% × acceptedJobs − 15% × writeOffs, 30%, 85%)`

## 2.2 Why Arc and Circle, structurally

Arc is a stablecoin-native EVM Layer 1 with USDC as gas, predictable dollar-denominated fees, and sub-second deterministic finality [R2]. Circle Agent Stack (launched 11 May 2026) provides Agent Wallets, Agent Marketplace, Circle CLI, and Nanopayments so agents can hold funds, discover services, and transact within defined permissions and guardrails [R9]. Gateway Nanopayments make sub-cent purchases practical via the documented x402 flow: deposit → 402 payment requirements → EIP-3009 authorization → retry → resource [R5–R7].

Snapfall depends on Arc specifically: sub-second finality lets the advance land inside the job-start flow and the waterfall execute as one visible beat; USDC-native gas makes per-advance unit economics predictable; USYC provides yield on idle pool capital; Arc's roadmap of opt-in privacy and confidential compute is the natural home for future attested underwriting [R2][R10]. Momentum: x402 settled ~$24M in a recent 30-day window, ~99.8% in USDC [R10]; Aave, Curve, and Maple participate in Arc testnet — institutional credit is Arc's DeFi direction, and Snapfall is its agent-native expression.

## 2.3 Vision and positioning

| **Element** | **Definition** |
| --- | --- |
| Vision | Every small business can operate a private, accountable, self-financing AI workforce without surrendering sensitive context or treasury control. |
| Category | Local-first autonomous business operations + machine-native receivables financing. |
| User promise | Complete bounded customer work with less coordination, transparent job economics, and working capital available from job one. |
| Differentiator | Persistent local agents + permission boundaries + programmable task budgets + escrow-secured self-financing + end-to-end commercial audit. |
| Not the pitch | "Many agents chatting," "replace every employee," "another lending pool." |
| The pitch | "The first AI business that finances itself: escrowed customer jobs, policy-controlled machine spending, self-improving machine credit." |

## 2.4 Objectives

| **ID** | **Objective** | **MVP success condition** |
| --- | --- | --- |
| OBJ-01 | Autonomous commercial execution | One job completes from intake through acceptance and Arc settlement. |
| OBJ-02 | Safe machine purchasing | ≥1 x402 nanopayment auto-approved under policy and tied to a task receipt. |
| OBJ-03 | Human control | ≥1 unusual payment paused; the human decision changes the workflow. |
| OBJ-04 | Local-first privacy | Sensitive artifacts remain local; only approved payloads and hashes leave the device. |
| OBJ-05 | Financial accountability | Dashboard shows revenue, advance, fee, expenses, margin, evidence, settlement. |
| OBJ-06 | Polished artifact | Working frontend/backend, public repo, deployed contracts, 3-minute video. |
| OBJ-07 | Self-financing | A zero-balance org funds execution via a Float advance; the waterfall repays the pool before operator profit. |
| OBJ-08 | Credit flywheel | The advance rate visibly increases after an accepted job, derived purely from on-chain history. |

## 2.5 Non-goals (MVP)

External agent marketplace or subcontracting network · **unsecured** or revolving credit (Float is receivables-secured advances against escrowed jobs only) · full ERP/CRM/payroll/tax integration · autonomous production deployment or irreversible real-world actions · >4 agent roles · multi-company federation · third-party LPs with real funds · production dispute arbitration · fully decentralized identity/governance · guaranteed operation on every local model or OS.

---

# 3. Users, Jobs and Use Cases

## 3.1 Primary persona — solo service founder

Independent consultant, research agency owner, marketing operator, or small B2B service business; 1–5 humans; tools today are Telegram/email, spreadsheets, Notion, AI chat, web APIs, a stablecoin wallet. Pain: coordination overhead, repetitive research, manual purchasing, weak margin visibility, no safe way to let AI transact — and execution costs due before revenue arrives. Desired outcome: accept more work without more headcount or fronted cash, keeping privacy and financial control. Can install a local app; doesn't need to understand contracts.

## 3.2 Secondary personas

| **Persona** | **Need** | **Snapfall value** |
| --- | --- | --- |
| Freelancer / consultant | Automate research, drafting, job admin | Persistent team, job vault, profitability view |
| Micro-agency operator | Isolate client contexts, control tool spend | Per-client workspace, scoped memory, budgets, audit |
| Finance-conscious founder | Prevent runaway costs; avoid fronting capital | Policies, approvals, receipts, reconciliation, advances |
| Privacy-sensitive team | Keep artifacts off hosted platforms | Local execution, explicit egress |
| Customer | Transparent scope, delivery, payment | USDC escrow, acceptance step, verifiable settlement |
| Liquidity provider | Yield with bounded, understandable risk | Escrow-secured advances, factoring fees, USYC idle yield, explicit loss waterfall |

## 3.3 Jobs to be done

| **ID** | **When…** | **I want to…** | **So that…** |
| --- | --- | --- | --- |
| JTBD-01 | a customer requests work | convert it into a scoped, priced job | I respond fast without admin. |
| JTBD-02 | a job is funded | delegate to specialized agents | work progresses while I focus elsewhere. |
| JTBD-03 | an agent needs a paid resource | auto-approve safe routine purchases | the workflow doesn't stall on small spends. |
| JTBD-04 | an action is expensive or unusual | get an approval request with full context | I stay accountable without micromanaging. |
| JTBD-05 | the deliverable is ready | review, send, settle | the revenue loop closes cleanly. |
| JTBD-06 | I review a completed job | see the full action and money trail | I can explain profitability and decisions. |
| JTBD-07 | a job is funded but treasury is thin | draw capital against the escrowed payment | execution costs never block accepted work. |
| JTBD-08 | I deliver reliably over time | have financing terms improve automatically | performance compounds into cheaper capital. |

## 3.4 Primary end-to-end use case (the demo spine)

A customer requests a competitor analysis of three products for 25 USDC and funds the JobVault. The org treasury holds 0 USDC. The founder authorizes a Float advance: at the org's 50% rate, 12.50 USDC lands in the treasury sub-second. The Manager plans and assigns Research and Delivery. Research buys a 0.04 USDC company profile via x402 (policy auto-approves). A 4.00 USDC dataset request exceeds the threshold and escalates; the founder rejects it and requests a cheaper source; the agent adapts and buys a 0.06 USDC benchmark summary. The report is produced and reviewed locally. The customer accepts; the waterfall repays the pool 12.50 + 0.25 fee first, releases the remainder to the operator, and an audit root anchors on Arc. Accepted jobs increments; the advance rate rises to 55%.

## 3.5 User stories

| **ID** | **Story** |
| --- | --- |
| US-01 | As a founder, I define AI employee roles and permissions so no worker has unnecessary access. |
| US-02 | As a founder, customer requests become jobs without manual setup. |
| US-03 | As a customer, I fund a job in USDC and see status tied to delivery. |
| US-04 | As a manager agent, I decompose a job and assign subtasks. |
| US-05 | As a worker agent, I request paid services via structured intents and never touch keys. |
| US-06 | As a finance operator, deterministic policies approve routine spend and escalate exceptions. |
| US-07 | As a founder, I approve or reject requests from Telegram or the dashboard. |
| US-08 | As a customer, I accept a deliverable and trigger transparent settlement. |
| US-09 | As a founder, I get a receipt linking actions, expenses, advance, fees, output, settlement. |
| US-10 | As a security-conscious user, sensitive data stays local and egress is explicit. |
| US-11 | As a founder with an empty treasury, I draw an advance against a funded job so accepted work is never cash-blocked. |
| US-12 | As an LP, I deposit USDC and see utilization, fees, idle yield, and the loss waterfall so I understand my risk. |

---

# 4. MVP Scope (MoSCoW — LOCKED)

**MUST (P0):** local daemon supervisor · 4 bounded agent roles · job intake + human-confirmed quote · JobVault funding · **FloatPool advance against a Funded job** · **settlement waterfall (pool principal+fee first, operator remainder, one tx)** · **on-chain advance-rate function** · task graph · local memory namespaces · typed actions + action broker · deterministic payment policies · one x402 paid API purchase · approval workflow (approve/reject/request-alternative) · customer acceptance · audit receipt incl. advance economics · dashboard incl. Float page.

**SHOULD (P1):** Telegram approvals · request/response hashes · cost-centre budgets · explorer links everywhere · CSV export · failure recovery · operator performance bond (10% of principal) · LP deposit/withdraw UI · gas abstraction via Paymaster **or Circle Gas Station (developers.circle.com wallets/gas-station) — C verifies which is stable on Arc testnet and picks one** · USYC idle sweep (documented mock if testnet-unavailable).

**COULD (P2):** email intake · second paid API · policy simulator · per-agent Circle Agent Wallets · CCTP customer funding from another testnet · second job demoed at the improved advance rate.

**WON'T (MVP):** external agent marketplace · unsecured/revolving credit · third-party LPs with real funds · ERP integrations · autonomous real-world actions · dispute arbitration · >4 agent roles.

## 4.1 MVP agents

| **Agent / service** | **Responsibilities** | **Hard restrictions** |
| --- | --- | --- |
| Manager | Classify request; plan; assign; monitor deadlines; resolve ordinary conflicts; prepare updates. | Cannot sign payments, access keys, request advances, or bypass specialist vetoes. |
| Research | Collect approved public info; invoke paid data services; produce structured findings. | Job-workspace filesystem only; domain allowlist; no arbitrary shell. |
| Delivery | Synthesize findings; produce report; run checklist; prepare final artifact. | Tiny budget; cannot release customer funds. |
| Finance UI + Policy Service | Explain spend requests; enforce budgets/rules; create receipts; surface advance status. | Authorization is deterministic; LLM explanation never overrides policy; cannot initiate advances (human-only). |
| Human operator | Confirm quotes; decide exceptions; authorize advances; change policies; freeze; review delivery. | All decisions logged; no silent policy changes on active jobs. |

## 4.2 Definition of done (all must pass before recording)

1. Fresh install → services start → dashboard opens.
2. Job created and funded with testnet USDC.
3. Zero-balance treasury draws a Float advance; amount matches on-chain `advanceRate()` exactly.
4. Manager assigns ≥2 subtasks; task graph recorded.
5. Agent completes ≥1 x402 paid purchase from the advance, policy-auto-approved.
6. One request pauses for human approval; the decision changes the workflow.
7. Deliverable stored locally with content hash.
8. Acceptance executes the waterfall on Arc — both transfers visible on the explorer.
9. Advance rate visibly increases on-chain after acceptance.
10. Receipt links revenue, advance, fee, expenses, margin, approvals, hashes, explorer links.
11. Automated tests pass; README reproduces the demo.

---

# 5. Product Requirements

P0 = mandatory for MVP; P1 = if time permits; P2 = post-MVP. Every P0 requirement has ≥1 automated or scripted acceptance test.

## 5.1 Organization and workforce (FR-ORG)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-ORG-001 | P0 | Create a local organization with unique ID, owner identity, treasury configuration, and active policy version. |
| FR-ORG-002 | P0 | Create, update, activate, freeze, and remove AI employee definitions. |
| FR-ORG-003 | P0 | Each employee has role, model config, memory namespace, filesystem scope, command allowlist, network allowlist, budget, and escalation manager. |
| FR-ORG-004 | P0 | Display employee status: idle, working, waiting, approval-required, failed, frozen. |
| FR-ORG-005 | P1 | Import/export employee manifests as YAML. |
| FR-ORG-006 | P1 | Validate manifests before activation; report unsafe or contradictory permissions. |

## 5.2 Event intake and job creation (FR-EVT)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-EVT-001 | P0 | Accept a customer request from the dashboard or a seeded demo webhook. |
| FR-EVT-002 | P1 | Accept Telegram messages via bot integration. |
| FR-EVT-003 | P0 | Assign event ID, timestamp, source, org, dedup key, sensitivity label, payload hash. |
| FR-EVT-004 | P0 | Manager converts an accepted request into a draft job: scope, deliverable, deadline, price, operating budget, approval rules. |
| FR-EVT-005 | P0 | A human confirms the customer-facing quote before the job becomes fundable. |
| FR-EVT-006 | P0 | Duplicate events never create duplicate jobs. |

## 5.3 Customer funding and job lifecycle (FR-JOB)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-JOB-001 | P0 | Create a JobVault record; show the Arc address/transaction required to fund it. |
| FR-JOB-002 | P0 | No paid execution until funding confirmed (or seeded pre-funded demo job explicitly used). |
| FR-JOB-003 | P0 | Job states: Draft, AwaitingFunding, Funded, InProgress, AwaitingApproval, DeliveryReady, Delivered, Accepted, Refunded, Cancelled, Failed. |
| FR-JOB-004 | P0 | Enforce legal transitions; record actor and reason for every transition. |
| FR-JOB-005 | P0 | Track customer payment, operating budget, advance drawn, advance fee owed, committed spend, settled spend, remaining budget, expected margin. |
| FR-JOB-006 | P0 | On acceptance, execute the settlement waterfall: repay any open advance (principal + fee) to the pool first, then release the remainder to the operator. |
| FR-JOB-007 | P1 | Timeout refund path for never-started/cancelled jobs, triggering Float write-off handling where an advance is open. |

## 5.4 Task orchestration (FR-TSK)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-TSK-001 | P0 | Manager decomposes a job into typed tasks: owner, dependencies, inputs, expected outputs, deadline, budget allocation. |
| FR-TSK-002 | P0 | A task cannot start until dependencies complete or Manager explicitly waives with a logged reason. |
| FR-TSK-003 | P0 | Agents exchange structured artifacts, not unrestricted transcripts. |
| FR-TSK-004 | P0 | Preserve a task event timeline: proposals, tool calls, outputs, failures, reviews, approvals, payments. |
| FR-TSK-005 | P0 | Specialist veto rules are deterministic; a worker cannot override a finance or safety denial. |
| FR-TSK-006 | P1 | Bounded retries for transient failures, then escalate. |
| FR-TSK-007 | P1 | Track task duration and estimated vs actual cost. |

## 5.5 Local memory and context (FR-MEM)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-MEM-001 | P0 | Separate org, customer, agent, job, and task memory namespaces. |
| FR-MEM-002 | P0 | An agent retrieves only memory permitted by role and current job. |
| FR-MEM-003 | P0 | Sensitive artifacts and intermediates stored locally by default. |
| FR-MEM-004 | P0 | Record provenance: author, source event, task, timestamp, sensitivity, retention. |
| FR-MEM-005 | P1 | Semantic retrieval over local memory without hosted embeddings. |
| FR-MEM-006 | P1 | Workspace deletion removes content and derived memory except immutable audit hashes and retained accounting data. |

## 5.6 Permissions, sandboxing, typed actions (FR-ACT)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-ACT-001 | P0 | Model output never executes directly; every proposal becomes a typed action with validated parameters. |
| FR-ACT-002 | P0 | Action broker checks role permission, job scope, path scope, command allowlist, network allowlist, risk class, approval status. |
| FR-ACT-003 | P0 | Filesystem access restricted to the active job workspace and explicitly shared resources. |
| FR-ACT-004 | P0 | External calls pass through an egress proxy logging destination, method, sensitivity decision, payload hash, response hash. |
| FR-ACT-005 | P0 | Secrets referenced by opaque handles; injected only into approved tools; never in prompts or audit text. |
| FR-ACT-006 | P0 | Destructive actions require human approval; disabled in demo unless safely simulated. |
| FR-ACT-007 | P1 | Sandbox applies CPU/memory/time/output limits (restricted-subprocess implementation acceptable for MVP). |

## 5.7 Payment intents and policies (FR-PAY)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-PAY-001 | P0 | Agents never access keys; they submit authenticated PaymentIntents to the Treasury service. |
| FR-PAY-002 | P0 | PaymentIntent includes org, job, task, agent, merchant, endpoint/resource, amount, asset, purpose, request hash, expiry, nonce, policy version. |
| FR-PAY-003 | P0 | Policy engine enforces job budget, agent/task limit, per-transaction limit, merchant/domain allowlist, category allowlist, velocity limits, expiry, duplicate-nonce prevention. |
| FR-PAY-004 | P0 | Result is AutoApprove, HumanApprovalRequired, or Deny, with machine-readable reasons. |
| FR-PAY-005 | P0 | Treasury signs an x402/Gateway authorization only after an approved decision. |
| FR-PAY-006 | P0 | Reserve budget when signing; reconcile reserved vs actual after settlement. |
| FR-PAY-007 | P0 | Failed/expired purchases release reserved budget and record the failure. |
| FR-PAY-008 | P1 | Global/job/agent payment freeze. |
| FR-PAY-009 | P1 | Policy simulator evaluating proposed rules against historical intents. |

## 5.8 x402 / Nanopayment purchase flow (FR-X402)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-X402-001 | P0 | Buyer integration supports a paid API returning HTTP 402 payment requirements. |
| FR-X402-002 | P0 | Payment sidecar uses Circle's documented Gateway batching/x402 SDK or compatible implementation. |
| FR-X402-003 | P0 | Request retried with approved payment signature; response stored locally. |
| FR-X402-004 | P0 | Receipt connects payment requirements, signed authorization hash, merchant, request hash, response hash, task, accounting category. |
| FR-X402-005 | P0 | Demo uses the EOA-compatible signing path per Circle's buyer quickstart [R6]; Agent Wallets integrated where stable (FR-FLT-010). |
| FR-X402-006 | P1 | Server-support check with explicit-opt-in fallback to standard onchain payment. |

## 5.9 Human approval and control (FR-APR)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-APR-001 | P0 | Approval requests include agent, job, task, action/payment, amount, reason, policy violations, alternatives, deadline, budget impact. |
| FR-APR-002 | P0 | The user can approve, reject, or request a cheaper/safer alternative. |
| FR-APR-003 | P0 | Decisions are authenticated, idempotent, and bound to the exact intent hash. |
| FR-APR-004 | P0 | Changed price/destination/payload/parameters invalidate the approval; re-evaluation required. |
| FR-APR-005 | P0 | Global kill switch stops new tasks, signatures, advance requests, and high-risk execution; read-only inspection remains. |
| FR-APR-006 | P1 | Telegram approvals deep-link to the full dashboard record. |

## 5.10 Delivery, acceptance, settlement (FR-DEL)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-DEL-001 | P0 | Delivery agent creates the final artifact locally and computes a content hash. |
| FR-DEL-002 | P0 | Quality checklist runs before DeliveryReady. |
| FR-DEL-003 | P0 | Customer (or demo operator) accepts via the web interface. |
| FR-DEL-004 | P0 | Acceptance triggers the waterfall (FR-JOB-006) and updates the ledger with repayment, fee, operator net. |
| FR-DEL-005 | P0 | Compute gross revenue, advance fee, external spend, protocol fees, gross margin. |
| FR-DEL-006 | P1 | Rejection with revision reason and bounded revision count. |

## 5.11 Audit, receipts, reporting (FR-AUD)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-AUD-001 | P0 | Every event appended to a tamper-evident local log with monotonic sequence. |
| FR-AUD-002 | P0 | Job receipt links request, agents, tasks, actions, approvals, paid services, advance + fee, output hash, settlement, profitability. |
| FR-AUD-003 | P0 | Full audit stays local; only Merkle roots / content hashes anchor on Arc. |
| FR-AUD-004 | P0 | Dashboard links settlement and advance transactions to an Arc explorer. |
| FR-AUD-005 | P1 | CSV/JSON export for payments, tasks, advances, accounting. |
| FR-AUD-006 | P1 | Deterministic replay of state from the event log (excluding nondeterministic generation). |

## 5.12 Dashboard (FR-UI)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-UI-001 | P0 | Overview: org balance, active jobs, pending approvals, workforce status, open advances, recent financial events. |
| FR-UI-002 | P0 | Job page: lifecycle state, task graph, timeline, budgets, advance status, expenses, deliverable, settlement. |
| FR-UI-003 | P0 | Employee page: role, permissions, current work, limits, recent actions. |
| FR-UI-004 | P0 | Approval inbox: approve, reject, request-alternative. |
| FR-UI-005 | P0 | Receipt view comprehensible without reading raw transactions. |
| FR-UI-006 | P1 | UI updates within 2 s of local event or chain confirmation. |

## 5.13 Float financing (FR-FLT)

| **ID** | **Pri** | **Requirement** |
| --- | --- | --- |
| FR-FLT-001 | P0 | Treasury can request an advance against a job whose JobVault state is Funded, upon human authorization. |
| FR-FLT-002 | P0 | Advance = advanceRate(org) × customerPayment, computed on-chain (SC-FP-002). The operating budget does not cap the advance. *(Ruling 19 Jul 2026.)* |
| FR-FLT-003 | P0 | Advance funds transfer only to the org treasury signer address, never to an agent wallet. |
| FR-FLT-004 | P0 | Dashboard shows per job: principal, fee owed, repayment status; per org: current rate and its derivation. |
| FR-FLT-005 | P0 | The waterfall executes in the JobVault contract, not off-chain; local ledger reconciles to on-chain events to the cent. |
| FR-FLT-006 | P0 | Float page shows pool TVL, utilization, fees accrued, reserve balance, loss-waterfall order. |
| FR-FLT-007 | P1 | LP deposit/withdraw operable from the dashboard against the ERC-4626-style pool. |
| FR-FLT-008 | P1 | Operator performance bond locked at issuance, released on repayment (SC-FP-011). |
| FR-FLT-009 | P1 | Idle capital sweeps to USYC with balances shown; documented mock acceptable if testnet USYC unavailable. |
| FR-FLT-010 | P1 | Agent identities via Circle Agent Wallets where stable; EOA path is the tested fallback. |
| FR-FLT-011 | P2 | Customer funding via CCTP from another testnet chain. Implementation note: use Circle **App Kit** (`@circle-fin/app-kit` + viem adapter) — `kit.bridge({ from: Ethereum_Sepolia, to: Arc_Testnet, amount })` is a single call, far cheaper than raw CCTP integration [R13]. |
| FR-FLT-012 | P2 | Paid demo API listed on Circle Agent Marketplace and invoked via discovery. |

---

# 6. Software Architecture

## 6.1 Overview

Agent reasoning is isolated from permissions and financial authorization. The Float additions (FloatPool contract + advance workflow in the Treasury service) do not alter trust boundaries: agents never touch keys, advances land only in the treasury, and the waterfall is contract-enforced.

## 6.2 Technology stack

| **Layer** | **Technology** | **Owner** |
| --- | --- | --- |
| Contracts + chain indexer | Solidity + Foundry + OpenZeppelin; indexer in **Go** (shares types with the daemon; no third language) | Member A |
| Local runtime + agents | **Go daemon** (or Node — decided once on Day 1, then locked) · SQLite (WAL) · typed event bus + transactional outbox · Ollama/llama.cpp local models via OpenAI-compatible abstraction | Member B |
| Payments + frontend | TypeScript sidecar (`@circle-fin/x402-batching`, viem) · Next.js dashboard · Telegram Bot API | Member C |
| Chain | Arc Testnet (USDC-native gas, deterministic sub-second finality, EVM) | Member A |

**AI-assisted workflow:** feed Claude Code / ChatGPT the Circle LLM-optimized docs (`developers.circle.com/llms.txt`, `docs.arc.io/llms.txt`) plus this PRD §6–§8 as standing context. Fork `circlefin/arc-nanopayments` as the payments reference — study it; the final demo must not resemble it.

## 6.3 Component responsibilities

| **Component** | **Responsibilities** | **Trust boundary** |
| --- | --- | --- |
| Daemon Supervisor | Start/stop workers, health checks, scheduling, graceful recovery | Trusted local process |
| Event Gateway | Validate sources, deduplicate, classify sensitivity, normalize | Network-facing; untrusted input |
| Orchestrator | Task DAG, dispatch, dependencies, retries | Model-assisted; transitions validated |
| Agent Worker | Scoped retrieval, reasoning, structured proposals/artifacts | Untrusted model output |
| Action Broker | Validate and execute typed actions in sandbox | Critical enforcement boundary |
| Memory Service | Namespace isolation, retrieval, retention, provenance | Sensitive local data |
| Egress Proxy | Destination allowlist, minimization, request/response hashes | Network security boundary |
| Policy Engine | Deterministic decisions over action/payment intents | Critical, non-LLM |
| Treasury Signer | Key custody; signs after approval; submits human-authorized advance requests; reconciles repayment | Highest financial trust boundary |
| Payment Sidecar | x402/Gateway integration; receipt normalization | External protocol boundary |
| Chain Indexer | Watch JobVault, FloatPool, AuditAnchor events; update ledger | Untrusted until confirmed |
| Dashboard/API | Human visibility and authenticated control incl. Float views | Local/user boundary |

## 6.4 Deployment model

Daemon, SQLite, workspaces, policy engine, and model runtime run on the user's machine. Dashboard binds to localhost by default; auth token required for non-loopback. The TS payment sidecar runs as a child process with a narrow authenticated API. Treasury signer is isolated from agent processes; only validated payment intents and authenticated human advance commands reach it. JobVault, FloatPool, AuditAnchor run on Arc Testnet; business data remains local; only minimal public metadata and hashes are emitted. Telegram optional but recommended for the demo approval beat.

## 6.5 Agent execution loop

Claim runnable task → load role manifest, permitted memory, job inputs, policy summary → call model with constrained output schema → validate into typed proposals → execute low-risk permitted actions; route payments/high-risk to policy → persist outputs, append events → complete, wait, retry, or escalate.

## 6.6 State machines

| **Entity** | **States** | **Invariants** |
| --- | --- | --- |
| Job | Draft → AwaitingFunding → Funded → InProgress → DeliveryReady → Delivered → Accepted; alt Refunded/Cancelled/Failed | No paid execution before Funded; no release before Accepted; waterfall exactly once; terminal states immutable. |
| Task | Pending → Runnable → Running → WaitingApproval/WaitingDependency → Completed; alt Failed/Cancelled | One active owner; dependencies enforced; bounded retries. |
| Payment intent | Proposed → Evaluating → AutoApproved/HumanApproval/Denied → Signed → Submitted → Delivered/Failed/Expired → Reconciled | Approval bound to intent hash; unique nonce; no signing after expiry or policy change. |
| Advance | Requested → Issued → Repaid; alt WrittenOff | Only against Funded jobs; one per job; issued only via on-chain rate function; repaid only via waterfall; written off only from job terminal refund/cancel. |
| Agent | Idle → Assigned → Working → Waiting → Idle; alt Failed/Frozen | Frozen agents cannot start tasks or submit financial actions. |

---

# 7. Smart Contract Specification (Arc testnet)

Contracts provide escrow, advances, waterfall settlement, and audit anchoring. They store no customer documents, transcripts, private policies, or sensitive details.

## 7.1 JobVault.sol (P0)

```solidity
enum JobStatus { Created, Funded, InProgress, Delivered, Accepted, Refunded, Cancelled }

struct Job {
    address customer;
    address operator;          // organization treasury
    uint256 customerPayment;
    uint256 maxOperatingBudget;
    uint256 onchainExpenses;
    bytes32 termsHash;
    bytes32 deliveryHash;
    uint64  deadline;
    JobStatus status;
}
```

| **ID** | **Requirement** |
| --- | --- |
| SC-JV-001 | Only the designated customer funds the job (unless sponsored demo mode is explicit). |
| SC-JV-002 | Funded amount and asset immutable after work starts. |
| SC-JV-003 | Only the operator/authorized treasury records or releases approved onchain expenses, bounded by maxOperatingBudget. |
| SC-JV-004 | Delivery hash set before acceptance. |
| SC-JV-005 | Acceptance executes the waterfall (SC-JV-009), then releases the remainder to the operator. |
| SC-JV-006 | Refund/cancel constrained by state, deadline, spent amount; notifies FloatPool of open advances (SC-JV-010). |
| SC-JV-007 | All financial state changes emit events. |
| SC-JV-008 | SafeERC20, checks-effects-interactions, ReentrancyGuard. |
| SC-JV-009 | **Waterfall:** with an open advance, transfer principal + fee to FloatPool via repayAdvance() BEFORE any operator transfer, in the same transaction; emit `JobSettled(jobId, advanceRepaid, operatorNet)`. |
| SC-JV-010 | Refund/cancel with an open advance calls `FloatPool.writeOff(jobId)` after customer restitution per SC-JV-006. |

## 7.2 FloatPool.sol (P0)

ERC-4626-style USDC vault.

```solidity
enum AdvanceStatus { None, Issued, Repaid, WrittenOff }

struct Advance {
    bytes32 jobId;
    address operatorOrg;
    uint256 principal;
    uint256 fee;            // 200 bps of principal
    uint64  openedAt;
    AdvanceStatus status;
}

function deposit(uint256 assets, address receiver) external returns (uint256 shares);
function withdraw(uint256 assets, address receiver, address owner) external returns (uint256 shares);
function requestAdvance(bytes32 jobId) external returns (uint256 amount); // org treasury only
function repayAdvance(bytes32 jobId, uint256 amount) external;            // JobVault only
function writeOff(bytes32 jobId) external;                                // JobVault only
function advanceRate(address org) public view returns (uint16 bps);
```

| **ID** | **Requirement** |
| --- | --- |
| SC-FP-001 | Advances issued only against jobs whose JobVault state is Funded, verified by reading JobVault — never trusting the caller. |
| SC-FP-002 | **advance = advanceRate(org) × customerPayment.** No budget term. `maxOperatingBudget` is the SC-JV-003 *spend* bound and has no bearing on borrowing capacity. Solvency holds by construction: the most a job can owe back is CAP_BPS + fee on that principal = 85% × 1.02 = **86.7%** of an already-escrowed payment, so the waterfall can never come up short. *(Ruling 19 Jul 2026, supersedes the prior `min(maxOperatingBudget, …)` formulation — see docs/OPEN-SPEC-QUESTIONS.md SPEC-01.)* |
| SC-FP-003 | Exactly one advance per job; state recorded before transfer (CEI); duplicates revert. |
| SC-FP-004 | Funds transfer only to the registered org treasury address, never agent wallets. |
| SC-FP-005 | Fees accrue to the pool; 20% of fees accrue to a first-loss reserve. |
| SC-FP-006 | Per-org exposure ≤10% TVL; global utilization ≤80%. |
| SC-FP-007 | Idle USDC may sweep to USYC and redeem on demand; documented mock behind the same interface if testnet USYC is absent. |
| SC-FP-008 | Write-off loss waterfall: operator bond → reserve → LP shares, in order, events per stage. |
| SC-FP-009 | advanceRate(org) = clamp(base 50% + 5% × acceptedJobs − 15% × writeOffs, floor 30%, cap 85%); inputs read from contract-visible history only — trustless underwriting. |
| SC-FP-010 | repayAdvance and writeOff callable only by the registered JobVault. |
| SC-FP-011 | (P1) requestAdvance locks a 10%-of-principal operator bond; released on repayment; first-slashed on write-off. |
| SC-FP-012 | OZ SafeERC20, ReentrancyGuard, AccessControl; all state changes emit events; ERC-4626 share-accounting invariants hold under fuzzing. |

## 7.3 AuditAnchor.sol (P0)

```solidity
function anchorJob(bytes32 jobId, bytes32 eventRoot, bytes32 paymentReceiptRoot,
                   bytes32 deliveryHash, uint64 completedAt) external;
```

| **ID** | **Requirement** |
| --- | --- |
| SC-AA-001 | Only the operator authority anchors a job. |
| SC-AA-002 | Anchors immutable once finalized; corrections create a new version event. |
| SC-AA-003 | No plaintext customer names, prompts, files, report text, or secret policy data on-chain — ever. |
| SC-AA-004 | Emits `JobAnchored(jobId, roots)`. |

## 7.4 Test law

Unit tests: every happy path, unauthorized calls, duplicate acceptance, duplicate advance, over-budget expense, waterfall ordering, write-off ordering, expired refund, reentrancy assumptions. Fuzz tests: amount and share accounting, state invariants. Pin compiler and dependency versions; publish deployed addresses and verified source where available. Contracts are testnet MVP code — never represented as audited or production-ready.

---

# 8. Data and API Specification

## 8.1 Core entities

| **Entity** | **Key fields** | **Location** |
| --- | --- | --- |
| Organization | id, owner, name, policyVersion, treasuryRef, status, cached advanceRate | Local; selected hashes onchain |
| Agent | id, role, model, permissions, budget, manager, status, walletRef | Local |
| Customer | id, displayName, contactRef, sensitivity | Local; never plain onchain |
| Job | id, customer, terms, price, budget, state, vaultAddress, deliveryHash, advanceRef | Local + minimal contract state |
| Advance | id, jobId, principal, fee, status, txIssue, txRepayOrWriteOff | Local ledger + FloatPool state |
| PoolPosition | lp, shares, deposited, withdrawn | FloatPool + local cache |
| Task | id, jobId, owner, dependencies, inputs, outputs, state, budget | Local |
| Event | seq, id, source, type, actor, timestamp, payloadHash, encryptedPayload | Local append-only |
| ActionIntent | id, agent, task, type, parametersHash, risk, policyVersion | Local |
| PaymentIntent | id, agent, task, merchant, amount, purpose, requestHash, nonce, expiry | Local |
| Approval | intentHash, decision, approver, reason, timestamp | Local; optional hash anchor |
| Receipt | job, payments, advance, hashes, settlement, category, result | Local + root onchain |
| Artifact | id, job/task, pathRef, contentHash, sensitivity, createdBy | Local; final hash onchain |

## 8.2 Example agent manifest

```yaml
id: market-researcher
role: Market Researcher
manager: business-manager

model:
  provider: local-openai-compatible
  endpoint: http://127.0.0.1:11434/v1
  model: qwen3-coder-local

permissions:
  filesystem:
    read:  ["/workspaces/{{job_id}}/inputs", "/knowledge/public"]
    write: ["/workspaces/{{job_id}}/research"]
  commands: ["python", "pandoc"]
  network:
    allow: ["api.demo-marketdata.local", "agents.circle.com"]

finance:
  job_limit_usdc: 3.00
  transaction_limit_usdc: 0.10
  approval_above_usdc: 0.10
  categories: ["business-data", "research-tools"]

escalation:
  on_policy_denial: business-manager
  on_sensitive_egress: human-owner
```

## 8.3 Example payment intent

```json
{
  "intentId": "pi_01J...",
  "organizationId": "org_demo",
  "jobId": "job_104",
  "taskId": "task_research_01",
  "agentId": "market-researcher",
  "merchant": "api.demo-marketdata.local",
  "resource": "POST /v1/company-profile",
  "amount": "0.040000",
  "asset": "USDC",
  "purpose": "Purchase competitor company profile",
  "requestHash": "0xa81f...",
  "nonce": "0xf2c1...",
  "expiresAt": "2026-07-27T13:30:00Z",
  "policyVersion": "pol_7"
}
```

## 8.4 Local service APIs

| **Method** | **Endpoint** | **Purpose** | **Auth** |
| --- | --- | --- | --- |
| POST | /api/v1/jobs | Create draft job | Owner/session |
| POST | /api/v1/jobs/{id}/quote | Confirm scope and quote | Owner |
| POST | /api/v1/jobs/{id}/start | Start funded job | Owner/orchestrator |
| GET | /api/v1/jobs/{id} | Job, tasks, budget, advance, state | Owner/customer token |
| POST | /api/v1/jobs/{id}/advance | Request Float advance (human-authorized) | Owner |
| GET | /api/v1/pool | TVL, utilization, fees, reserve, org rate | Owner/LP session |
| POST | /api/v1/pool/deposit | LP deposit (P1) | LP session |
| POST | /api/v1/pool/withdraw | LP withdrawal (P1) | LP session |
| POST | /api/v1/payment-intents | Submit intent for evaluation | Agent mTLS/token |
| POST | /api/v1/approvals/{id} | Approve/reject/request alternative | Owner |
| POST | /api/v1/jobs/{id}/deliver | Mark delivery ready; attach hash | Delivery agent |
| POST | /api/v1/jobs/{id}/accept | Customer accepts (triggers waterfall) | Customer token/wallet proof |
| POST | /api/v1/control/freeze | Freeze org/job/agent | Owner |
| GET | /api/v1/receipts/{jobId} | Complete job receipt | Owner/customer scoped |
| GET | /api/v1/events/stream | SSE stream for dashboard | Owner |

## 8.5 Event taxonomy

| **Category** | **Event types** |
| --- | --- |
| Intake | customer.request.received, event.duplicate.detected, job.draft.created |
| Job | job.funded, job.started, job.delivery_ready, job.accepted, job.refunded |
| Float | advance.requested, advance.issued, advance.repaid, advance.written_off, pool.deposit, pool.withdraw, rate.updated |
| Task | task.created, task.assigned, task.started, task.completed, task.failed |
| Agent | agent.started, agent.waiting, agent.escalated, agent.frozen |
| Action | action.proposed, action.approved, action.denied, action.executed |
| Finance | payment.proposed, policy.evaluated, payment.signed, payment.delivered, payment.reconciled |
| Approval | approval.requested, approval.approved, approval.rejected, approval.expired |
| Audit | receipt.generated, audit.root.computed, audit.anchored |

---

# 9. Security and Privacy

## 9.1 Security model

Untrusted: model output, customer input, external API responses, chain/RPC data until confirmed. Trusted computing base: policy engine, action broker, state store, treasury signer — plus the contracts, whose invariants the chain enforces. Objective: an agent cannot exceed its data, execution, or financial authority even if the model is manipulated — and no off-chain component can bypass the contract-enforced waterfall or rate function.

## 9.2 Data classification and egress

| **Class** | **Examples** | **Default egress** |
| --- | --- | --- |
| Public | Public URLs, public company data | Allowed to approved destinations |
| Internal | Task plans, internal notes | Blocked unless destination + purpose approved |
| Confidential | Customer files, drafts, financials | Local only; human approval for necessary egress |
| Secret | Keys, API secrets, credentials | Never exposed to model or destination; opaque handle only |

## 9.3 Threat model

| **Threat** | **Impact** | **Mitigation** |
| --- | --- | --- |
| Prompt injection from customer/web data | Unauthorized actions or disclosure | Input labeling; tool isolation; typed actions; policy enforcement; untrusted-content boundaries |
| Agent attempts wallet access | Treasury loss | No keys in agent process; signer accepts only approved intent hashes |
| Agent attempts advance request | Unauthorized borrowing | Advance path is treasury+human-only (FR-FLT-001); contract pays only the treasury (SC-FP-004) |
| Approval replay/substitution | Executed action differs from approved | Approval bound to full intent hash, nonce, expiry, policy version (AT-05) |
| Duplicate event/payment/advance | Double work/spend/borrow | Dedup keys; idempotency; unique nonces; one advance per job (SC-FP-003) |
| Self-dealing fake jobs to farm rate | Inflated credit terms | Rate cap 85%; exposure cap; bond (P1); disclosed v1 limitation + roadmap (customer-diversity scoring, attested underwriting) |
| Waterfall bypass / reentrancy | Pool unpaid; theft | Waterfall inside acceptance tx (SC-JV-009); repay/writeOff JobVault-only (SC-FP-010); ReentrancyGuard; CEI |
| Sensitive data leakage | Confidentiality breach | Local storage; egress proxy; classification; minimization; redaction |
| Malicious paid API | Bad/oversized response | Allowlist; timeout/size caps; response hash; receipt status |
| Compromised agent model | Unsafe proposals | No direct execution; least privilege; sandbox; independent policy service |
| Dashboard exposure | Unauthorized control | Loopback binding; auth; CSRF; secure session |
| Contract bug | Locked/misdirected funds | Minimal scope; tests + fuzzing; caps; testnet disclaimer |
| RPC/indexer inconsistency | Wrong local state | Confirmation checks; idempotent indexing; re-query; reconciliation |

## 9.4 Security requirements

| **ID** | **Requirement** |
| --- | --- |
| SEC-001 | Secrets encrypted at rest or in the OS credential store. |
| SEC-002 | Treasury signer runs in a separate process, minimal API, no model dependencies. |
| SEC-003 | No key, seed, or raw secret in logs, prompts, telemetry, or UI. |
| SEC-004 | All mutable APIs authenticated with CSRF/replay protection. |
| SEC-005 | Policy and manifest changes versioned and audited. |
| SEC-006 | Approvals expire and invalidate when parameters or policy version change. |
| SEC-007 | Deny-by-default network access for agent workers. |
| SEC-008 | Sensitive payloads redacted or hashed in audit logs. |
| SEC-009 | Global freeze stops new payments and advance requests within 1 s of local command. |
| SEC-010 | Public demo uses testnet assets only, clearly labeled. |
| SEC-011 | Advance requests require authenticated human authorization distinct from routine payment auto-approval. |

## 9.5 Privacy commitments

No customer deliverable, prompt, file, or agent memory is written to Arc. Public-chain records contain addresses, amounts, contract state, hashes; metadata is publicly observable and users must understand this. External model use is disabled by default in the hackathon configuration. Telemetry is opt-in and never includes prompt or file contents. Workspace deletion removes local content and derived memory except immutable audit hashes and required accounting records.

---

# 10. Non-Functional Requirements

| **ID** | **Category** | **Target** |
| --- | --- | --- |
| NFR-001 | Availability | Supervisor recovers crashed workers; no task event lost after SQLite commit. |
| NFR-002 | Performance | Dashboard updates ≤2 s after local event/chain confirmation; policy evaluation ≤100 ms excl. external I/O. |
| NFR-003 | Performance | Payment approval-to-signature ≤2 s excl. network response. |
| NFR-004 | Performance | Advance request-to-treasury-credit ≤5 s end-to-end on testnet (sub-second on-chain component). |
| NFR-005 | Scalability | ≥10 active jobs, 20 employees, 10,000 audit events on a developer laptop. |
| NFR-006 | Reliability | Timeouts, bounded retries, idempotency keys on all external calls. |
| NFR-007 | Durability | SQLite WAL; transactional event/outbox writes; export available. |
| NFR-008 | Portability | Linux x86-64 primary; macOS P1; Windows post-MVP. |
| NFR-009 | Maintainability | Typed schemas, structured logs; ≥70% coverage mandatory for contracts + policy engine, aspirational elsewhere. |
| NFR-010 | Observability | Health status; correlation IDs for org, job, task, intent, advance. |
| NFR-011 | Usability | New user runs the seeded demo within 10 minutes from the README. |
| NFR-012 | Accessibility | Keyboard navigation, semantic headings, visible focus, sufficient contrast on demo screens. |
| NFR-013 | Compatibility | Pinned Solidity/Foundry versions on standard EVM tooling supported by Arc. |
| NFR-014 | Cost | Demo caps total testnet spend; all amounts displayed before signing/authorization. |

---

# 11. UX and Interface

## 11.1 Navigation

Overview · Jobs · Workforce · Approvals · Finance · **Float** (TVL, utilization, fees, reserve, org rate + derivation, LP deposit/withdraw P1, loss-waterfall explainer) · Audit · Settings (models, integrations, policies, wallet/chain, global freeze).

## 11.2 Key screens

| **Screen** | **Required content/actions** |
| --- | --- |
| Overview | Treasury/Gateway balance, active jobs, spend today, pending approvals, open advances, workforce cards, recent events, global freeze. |
| Job detail | Request, quote, vault state, advance card (rate, principal, fee, status), task graph, timeline, agents, budget waterfall, payments, deliverable, acceptance, receipt. |
| Approval detail | Intent summary, exact amount/destination/resource, policy result, sensitivity warning, budget impact, alternatives, three actions. |
| Employee detail | Role, model, permissions, domains/commands, budgets, manager, current task, recent events, freeze. |
| Float page | TVL, utilization %, fees, reserve, org rate + derivation (accepted jobs, penalties), open advances table, loss-waterfall order, LP actions (P1), explorer links. |
| Receipt | Revenue, advance principal + fee, expenses, margin, agents/tasks, approvals, paid resources, payment/response hashes, delivery hash, waterfall transactions. |

## 11.3 UX principles

Business language first; hashes are secondary. Every autonomous action answers who/what job/why/what authority/what cost/what result. Financial decisions visually distinct with a default-safe option. The advance is presented as working capital ("Advance available: 12.50 USDC at 50%"), not DeFi jargon. Deterministic services are not anthropomorphized. Local vs external processing always visible. Demo path linear and resilient with seeded data and a reset function.

---

# 12. Testing and Acceptance

## 12.1 Test strategy

| **Layer** | **Coverage** |
| --- | --- |
| Unit | Policy rules, state machines, manifest validation, budget accounting, hashing, dedup, advance-rate function. |
| Contract | Funding, transitions, acceptance/waterfall, refund/write-off, advance issuance, duplicate advance, caps, authorization, invariant/fuzz (incl. ERC-4626 shares). |
| Integration | Event→job→task; intent→policy→signing→paid API; advance→issuance→treasury credit; chain event→reconciliation. |
| Security | Prompt-injection fixtures; unauthorized path/command/network; replayed approval; duplicate nonce; secret redaction; agent-initiated advance attempt (must fail). |
| End-to-end | Seeded job completes: funding, advance, purchase, approval, delivery, acceptance, waterfall, receipt. |
| Demo rehearsal | Cold-start setup, faucet checks, network-failure fallback, reset script, video capture run. |

## 12.2 Acceptance scenarios

| **ID** | **Scenario** | **Criterion** |
| --- | --- | --- |
| AT-01 | Create and fund job | Confirmed quote + customer funds vault → dashboard shows Funded, start enabled. |
| AT-02 | Auto-approved purchase | Allowlisted merchant, 0.04 USDC under limits → signs, pays, receives data, receipt recorded, no human input. |
| AT-03 | Approval-required purchase | 4.00 USDC over threshold → task pauses, approval sent; no signature exists before approval. |
| AT-04 | Rejected alternative | Reject + request-cheaper → original intent terminal; Research proceeds with lower-cost source. |
| AT-05 | Substitution attack | Amount/merchant changed post-approval → signing denied; new approval required. |
| AT-06 | Egress protection | Confidential content to unapproved domain → blocked; redacted denial recorded. |
| AT-07 | Delivery and settlement | Acceptance of hashed deliverable → contract settles; job Accepted locally. |
| AT-08 | Audit completeness | Receipt contains all task/payment/advance/approval/output/chain evidence; root matches anchor. |
| AT-09 | Kill switch | Freeze stops new claims, signatures, advances; dashboard remains readable. |
| AT-10 | Restart recovery | Daemon killed mid-job → restart reconstructs state; no completed payment or advance repeats. |
| AT-11 | Advance issuance | Funded 25 USDC job at 50% → requestAdvance transfers exactly 12.50 to treasury; second request reverts. |
| AT-12 | Settlement waterfall | Acceptance with open advance → pool receives principal+fee before operator receives anything, one tx; explorer shows both; receipt reconciles to the cent. |
| AT-13 | Rate flywheel | After AT-12, advanceRate returns 55%; after a write-off, rate decreases per SC-FP-009. |
| AT-14 | Write-off waterfall | Refund with open advance → loss waterfall fires in order: bond → reserve → shares; events per stage. |
| AT-15 | Unauthorized advance | Agent-originated/non-treasury advance rejected at API (FR-FLT-001) and contract (SC-FP-004). |

## 12.3 Release gate

All P0 acceptance tests pass in CI · contracts deployed to Arc Testnet with documented addresses · no secrets in repo history/logs/screenshots/video · reset + seed script completes twice consecutively · README reproduces install and demo · video shows real UI, real advance, real payment, real waterfall, explorer evidence · limitations and testnet status disclosed.

---

# 13. Metrics

## 13.1 Hackathon success metrics

| **Metric** | **Target** |
| --- | --- |
| End-to-end demo completion | 100% across three consecutive rehearsals |
| Demo duration | ≤3:00 |
| P0 completion | 100% or explicit scope revision before recording |
| Real integrations | JobVault + FloatPool + AuditAnchor + x402 Nanopayment + USDC settlement minimum; Wallets/Paymaster/USYC/CCTP as achieved |
| Self-financing proof | One advance from a 0-balance treasury, repaid via on-chain waterfall, explorer-linked |
| Human-control proof | One real approval/rejection affecting the job |
| Flywheel proof | Advance rate increases post-acceptance, read from chain |
| Audit proof | One complete receipt + onchain anchor/settlement link |

## 13.2 Post-hackathon product metrics

Activation (time to first employee / first job / first advance) · Autonomy (% tasks without intervention; auto-approved payment share; escalation rate) · Safety (denied actions; prevented violations; egress blocks; replay prevention) · Economics (revenue and external cost per job; advance utilization; effective financing cost; margin) · Credit (rate distribution; repayment rate; write-off rate; reserve coverage; LP APY) · Quality (acceptance rate; revisions; failures; overrides) · Retention (weekly active orgs; jobs per org; repeat financing).

---

# 14. Delivery Plan

## 14.1 Ownership (fill names at first standup — one owner per workstream)

| **Role** | **Owns** | **Name** |
| --- | --- | --- |
| **Member A — Contracts & Chain** | JobVault, FloatPool, AuditAnchor + Foundry tests, deployment scripts, Go chain indexer, explorer plumbing. Owns FloatPool judge Q&A cold: caps, reserve, waterfall, bond. | ______ |
| **Member B — Runtime & Agents** | Daemon, event store + outbox, job/task state machines, manifests + worker loop, action broker, sandbox, policy engine, treasury signer, kill switch, restart recovery. | ______ |
| **Member C — Payments, Frontend & Story** | x402/Gateway sidecar, paid demo API, Next.js dashboard incl. Float page, Telegram bot, seed/reset scripts, README, deck, video production. | ______ |

Recommendation: Gnanasekaran = Member C (its code load is lightest in the final week, exactly when demo/deck work peaks). Helping is fine; ownership moves only by standup decision.

## 14.2 Calendar (today = Sunday 19 July 2026)

| **Dates** | **Phase** | **Exit criteria** |
| --- | --- | --- |
| Sun 19 – Wed 22 Jul | Rails & de-risk | All three registered on Encode + Arc House + Discord. Repo + CI live. Runtime language decided Day 1 and locked. Arc wallets funded; faucet limits, USYC availability, Paymaster availability verified and written into README. Three contract skeletons deployed **(A: read docs.arc.io/arc/references/evm-differences BEFORE the first Foundry deploy; note any quirks in the README)**. **C: full x402 buyer→seller loop against our own paid API by Tue 21 Jul.** Dashboard shell renders. Seeded demo request exists. Handles + domain secured. |
| Thu 23 – Sun 26 Jul | Contracts real + workforce kernel | A: JobVault + FloatPool full logic, unit tests passing; **first advance issued against a Funded job on testnet by Sun 26.** B: daemon + event store + manifests + Manager→Research→Delivery on a stub job. **ABI + local API schema FREEZE: Fri 24 Jul.** Post-freeze redeploys max every 48 h, announced. |
| Mon 27 Jul – Sun 2 Aug | Programmable money integration | Policy engine gating real intents; x402 purchase inside a job task; approval flow (dashboard + Telegram) incl. reject-and-adapt; indexer reconciling to the cent; waterfall executing on acceptance. **Daily demo-spine runs from Wed 29 Jul**, pass/fail logged in repo. Exit: DoD items 1–9 pass at least once by Sun 2 Aug. |
| Mon 3 – Wed 5 Aug | Second-order + polish | Rate flywheel visible on-chain; LP page; performance bond; Paymaster caption; USYC real-or-mock; CCTP only if all green; receipt page complete; visual polish. |
| Thu 6 – Fri 7 Aug | Hardening + story | Tests green in CI; restart recovery passes; secret audit; reset script clean twice; **record video Thu 6; edit + deck + README Fri 7.** |
| **Sat 8 Aug** | SUBMIT | Upload by evening IST; verify every link public from an incognito browser. |
| Sun 9 Aug | Buffer | Platform/network contingency only. Not build time. |

## 14.3 First 72 hours

**Everyone (tonight):** register on all platforms · runtime-language call (30 min, decide, lock) · create GitHub org `snapfall` + X handle + domain · fix daily 15-min standup time · **AI-context setup: `bun add -g @circle-fin/cli` → `circle skill install --tool claude-code` (Circle Agent Skills into each member's Claude Code) + Arc MCP server per docs.arc.io/ai/mcp; feed developers.circle.com/llms.txt + docs.arc.io/llms.txt as standing context** [R12].
**A:** Foundry scaffold with OZ; three contract skeletons compiling with events/signatures matching §7 exactly; deploy script; addresses committed; state-transition unit tests started.
**B:** Go module scaffold; SQLite schema for events/jobs/tasks; supervisor with one dummy worker; manifest loader for four roles; typed bus + outbox table.
**C (critical path):** Circle dev account + Gateway testnet setup; clone `circlefin/arc-nanopayments` **and `circlefin/agent-stack-starter-kits` — `packages/circle-tools` is REFERENCE ONLY: source of the x402 wire format (Gateway options identified by `extra.name`); our sidecar implements Arc-testnet-native wrappers directly** [R12]; stand up our paid demo API returning 402; complete one full buyer flow (402 → sign → retry → 200 + data) with our wallet; screen-record it immediately (insurance + deck asset). Then Next.js scaffold, Overview + Job pages stubbed.
**If C's x402 loop is not working by Tue 21 night → all three swarm it Wed 22. Nothing else matters until money moves.**

## 14.4 Operating rules

1. Daily 15-min standup: yesterday / today / blocked. Blocked >4 h → escalate immediately.
2. ABI + API schemas frozen 24 Jul; later changes need all-three agreement and a version bump.
3. Demo spine runs daily from 29 Jul; a red spine outranks any feature work.
4. Scope-cut order (§14.5) is law; cuts applied top-down at standup, never solo.
5. `main` is always demoable; feature branches + PR review by the touched workstream's owner.
6. No secrets in repo/logs/screenshots — checked in CI and re-audited 6 Aug.
7. Submit on the 8th. The 9th does not exist.

## 14.5 Scope-cut order (apply top-down when behind, never bottom-up)

1. Cut CCTP + Agent Marketplace + second-job demo beat (P2s)
2. Cut USYC sweep → documented mock
3. Cut LP UI (keep the vault contract)
4. Cut Telegram → dashboard-only approvals
5. Cut performance bond
6. Cut per-agent wallets → single treasury signer + virtual cost centres
7. **NEVER cut:** advance, waterfall, policy-controlled purchase, human approval, acceptance settlement, audit receipt, end-to-end $0-start demo.

---

# 15. Demo and Submission

## 15.1 Three-minute demo script

| **Time** | **Beat** | **Must be visible** |
| --- | --- | --- |
| 0:00–0:20 | Thesis | "We gave our AI business zero dollars and one customer. Watch it finance itself." Treasury: 0.00 USDC. |
| 0:20–0:40 | Setup | Workforce cards; 25 USDC job; customer funds JobVault (explorer flash). |
| 0:40–1:00 | **The snap** | Founder authorizes; 12.50 USDC (50% rate) lands sub-second from FloatPool; treasury 0 → 12.50. "Capital in a snap." |
| 1:00–1:25 | Autonomous work + safe spend | Manager assigns tasks; Research buys 0.04 USDC data via x402 — policy AutoApproves; receipt ties payment → task. |
| 1:25–1:50 | Human control | 4.00 USDC request exceeds threshold → Telegram approval → founder REJECTS, requests cheaper → agent adapts, buys 0.06 USDC alternative. |
| 1:50–2:10 | Delivery | Report produced, checklist passes, content hash shown; customer accepts. |
| 2:10–2:35 | **The fall — "watch the Snapfall"** | One Arc tx: pool repaid 12.50 + 0.25 fee FIRST, operator receives remainder; explorer proof. |
| 2:35–2:50 | The flywheel | Advance rate ticks 50% → 55% on-chain; "the business just earned cheaper capital by delivering." |
| 2:50–3:00 | Close | Tool-mapping flash + "Snapfall, built on Arc. The first AI business that finances itself." |

**Recording integrity rule:** scripted mode = deterministic **replay of a real prior run's agent outputs** (never hand-written dialogue); all transactions live on testnet during recording; one caption: "agent outputs replayed from a live run for timing; every transaction is live." Live-LLM toggle shown 5 seconds. The snap, the rejection, and the fall each get 10× rehearsal — judges remember moments, not features.

## 15.2 Demo seed data

| **Item** | **Value** |
| --- | --- |
| Customer | Acme Labs |
| Job | Competitor analysis, three AI coding products |
| Customer payment (escrow) | 25.00 testnet USDC |
| Max operating budget | 6.00 USDC |
| Org starting treasury | 0.00 USDC |
| Advance rate (job 1) | 50% → advance 12.50 USDC |
| Float fee (200 bps) | 0.25 USDC |
| Auto-approval threshold | 0.10 USDC |
| Approved purchase | Company profile API — 0.04 USDC |
| Escalated purchase | Premium dataset — 4.00 USDC (rejected) |
| Alternative purchase | Benchmark summary — 0.06 USDC |
| Total external spend | 0.10 USDC |
| Waterfall on acceptance | Pool receives 12.75; operator receives 12.25 from escrow |
| Operator net position | 12.25 + (12.50 − 0.10 unspent advance) = 24.65 USDC |
| Gross margin | 24.65 USDC (25.00 − 0.10 spend − 0.25 fee) |
| Post-job advance rate | 55% |
| Pool seed (demo LPs) | 100.00 testnet USDC |

## 15.3 Deck outline (≤10 slides)

1 Thesis · 2 The two gaps (spending + working capital) · 3 Product: the workforce · 4 Float Protocol + advance-rate function · 5 Architecture & trust boundaries · 6 Why Arc specifically (sub-second snap, USDC gas economics, USYC, privacy roadmap) · 7 Circle tool map · 8 **Circle DX feedback** (what worked / friction found — judges explicitly value this; lead with the concrete one: `packages/circle-tools` is reference-only for us — a closed `Chain` union of BASE|POLYGON with hardcoded mainnet RPCs and a CLI-session dependency meant we implemented Arc-testnet-native wrappers directly, and adding an Arc chain entry would make the kit genuinely reusable) · 9 Traction path: real SaaS, 6-month roadmap, "hire more employees" extensibility · 10 Team + ask.

## 15.4 Submission checklist (Encode platform)

- [ ] Functional MVP deployed on Arc testnet (addresses in README)
- [ ] Public repo (or judges added), reproducible README, tests green
- [ ] 3-min video: core functionality + Circle tools named aloud
- [ ] Deck uploaded
- [ ] Every link public and working — platform locks at deadline (AoE)
- [ ] Brand check: "Snapfall, built on Arc" everywhere; no Arc-prefixed naming

## 15.5 Judge Q&A preparation

| **Question** | **Answer** |
| --- | --- |
| Why blockchain? | Customer escrow, programmable settlement waterfalls, machine-native USDC purchases, trustless credit from delivery history, tamper-evident proof — all need a shared settlement layer; private work data stays local. |
| Why Arc? | USDC-native fees, EVM contracts, deterministic sub-second finality (the snap; the one-beat waterfall), USYC, Circle integration [R2]. |
| Just another lending pool? | No debtor-default risk exists — the receivable is pre-escrowed on-chain. Float prices only performance risk, from cryptographic delivery history. Structurally different and safer than unsecured agent credit. |
| You seeded your own pool — circular? | In the demo we act as LPs; in production LPs are third parties earning factoring fees + idle USYC yield, protected by the loss waterfall. Economics on the Float page. |
| Farm fake jobs to raise the rate? | v1: 85% cap, per-org exposure cap, one advance per job, bond (P1). Disclosed roadmap: customer-diversity scoring, TEE-attested underwriting on Arc confidential compute. |
| Is the Finance Agent an LLM? | It may explain; policy decisions, signing, and advance authorization are deterministic with human gates. |
| Everything offline? | Local-first, not offline: custody and orchestration local; approved services, Telegram, Circle, Arc need network. |
| Replace a business team? | Bounded, repeatable workflows only; uncertainty and risk escalate to the human. |
| Why not just a bank? | Escrow-secured, instant, permissionless, trustless rate function — no human SME gets a 5-second advance priced by cryptographic history. |
| Circle already launched a Settlement Advance Credit API (Mint, Apr 2026) — how are you different? | Circle's is off-chain, institutional, for Mint/CPN partners. Float is on-chain, trustless, for autonomous AI businesses — the rate is a pure contract function of delivery history. Circle validated the category; we built the machine-native version [R14]. |
| Production next? | Real customer integrations, hardened sandboxing, third-party LPs, richer underwriting, EURC/StableFX multi-currency, audited contracts. |

---

# 16. Risks and Decisions

## 16.1 Risk register

| **Risk** | **Prob** | **Impact** | **Mitigation** |
| --- | --- | --- | --- |
| Scope explosion | High | High | MoSCoW locked; scope-cut order is law; one vertical demo; roadmap the rest. |
| JobVault↔FloatPool coupling bugs | Med | High | FloatPool ~250 lines on OZ primitives; built in de-risk week alongside JobVault; integration + fuzz tests early; waterfall ordering asserted. |
| Local model quality | Med | High | Constrained tasks, structured outputs, seeded data, optional stronger local model. |
| x402/Gateway delay | Med | High | Quickstart spiked days 1–3; isolated sidecar; our own paid API. |
| Testnet surprises (USYC absent, faucet limits, congestion) | Med | High | Verify Day 1–2; mocks behind interfaces; pre-funded wallets; submit the 8th. |
| Contract bug/stuck state | Low/Med | High | Minimal scope; unit + fuzz; admin testnet recovery disclosed; no production claims. |
| Demo looks like agent theatre | Med | High | Real state transitions, advance, payment, waterfall, artifact, receipt; recording-integrity rule (§15.1). |
| Circular-economy critique | Med | Med | Pre-empted in Q&A and on the Float page. |
| Rate-gaming critique | Med | Med | Caps + bond + disclosure + roadmap; answered before asked. |
| Agent Wallets friction | Med | Med | EOA path [R6] is the tested fallback; swap-in only 3–5 Aug if green. |
| Money feels bolted on | Low | High | The $0-start opener makes financing the protagonist; the loop cannot complete without it. |
| Privacy overstated | Med | Med | "Local-first" language; documented egress; hashes-not-content onchain. |
| Dual-track dilution | Low | Med | One narrative, one build, one video; track forms emphasize different halves. |
| Brand misuse | Low | Med | "Snapfall, built on Arc"; Arc never in product identity. |
| Snap Inc. trademark gravity | Low (now) | Low (now) | Hackathon-irrelevant; revisit at commercialization; documented in §0. |

## 16.2 Architecture decision records

| **ADR** | **Decision** | **Why** |
| --- | --- | --- |
| ADR-001 | Go local runtime; TypeScript payment sidecar | Daemon strengths + official Circle JS path. |
| ADR-002 | One treasury signer; virtual per-agent budgets | Less key complexity, same accountability. |
| ADR-003 | Policies are deterministic code, not prompts | Model manipulation cannot bypass controls. |
| ADR-004 | Full audit local; only roots/hashes onchain | Privacy + cost + tamper evidence. |
| ADR-005 | JobVault + FloatPool are the only required financial contracts | Programmable money central without sprawl. |
| ADR-006 | Customer acceptance controls the waterfall | Clear commercial loop; pool seniority contract-enforced. |
| ADR-007 | Dual-track submission with one build | Float makes DeFi genuine; agents make Agentic genuine. |
| ADR-008 | Advance rate computed on-chain from contract-visible history (v1) | Trustless underwriting; no oracle surface; strongest Q&A position. |
| ADR-009 | Advances pay the org treasury only; agents cannot borrow | Shrinks fraud surface; preserves human-gated hierarchy. |
| ADR-010 | Waterfall executes inside JobVault acceptance, atomically | Seniority cannot be bypassed by off-chain ordering. |

---

# 17. Post-Hackathon Roadmap

| **Phase** | **Capabilities** | **Rationale** |
| --- | --- | --- |
| 1 — Hardened micro-agency + Float mainnet beta | Real intake, client workspaces, workflow packs, accounting exports; audited contracts on Arc mainnet beta | Convert demo into weekly-use product with real financing. |
| 2 — Open the pool | Third-party LPs; richer underwriting (customer diversity, category risk); TEE-attested scoring; policy simulation | A real two-sided market with institutional risk controls. |
| 3 — Multi-currency + privacy | EURC-escrowed jobs via StableFX; confidential advances via Arc opt-in privacy | Cross-border service businesses; commercial confidentiality. |
| 4 — Float as open protocol | Any agentic-commerce app with escrowed receivables plugs into the pool via a standard interface | The working-capital layer of the agentic economy. |
| 5 — Multi-machine private deployment | Company-hosted workers, hardware-backed keys, confidential compute, policy federation | Larger privacy-sensitive teams. |

**Expansion rules:** new agent roles only with distinct permission/data/review boundaries · new financial primitives only for observed workflow problems · human accountability for irreversible/high-value/regulated actions · local-first defaults persist · never claim production security or compliance without independent verification.

---

# Appendix A — Policy Examples

## A.1 Organization policy

```yaml
version: pol_7
currency: USDC

global:
  daily_spend_limit: 10.00
  approval_above: 1.00
  deny_unknown_merchants: true
  freeze: false

financing:
  advance_requires_human: true
  max_open_advances: 1
  advance_fee_bps_expected: 200

categories:
  business-data:
    daily_limit: 5.00
    approval_above: 0.50
  model-inference:
    daily_limit: 2.00
    approval_above: 0.25

rules:
  - deny_if: payload_contains_secret
  - deny_if: intent_expired
  - deny_if: nonce_seen
  - require_approval_if: merchant_not_previously_used
```

## A.2 Payment decision record

```json
{
  "intentHash": "0x91ab...",
  "decision": "AUTO_APPROVE",
  "policyVersion": "pol_7",
  "checks": [
    {"name": "job_budget", "result": "PASS", "remaining": "5.96"},
    {"name": "transaction_limit", "result": "PASS", "limit": "0.10"},
    {"name": "merchant_allowlist", "result": "PASS"},
    {"name": "sensitive_egress", "result": "PASS"},
    {"name": "nonce_unique", "result": "PASS"}
  ],
  "evaluatedAt": "2026-07-27T13:28:41Z"
}
```

## A.3 Advance decision record

```json
{
  "jobId": "job_104",
  "action": "ADVANCE_REQUEST",
  "authorizedBy": "human-owner",
  "jobState": "Funded",
  "customerPayment": "25.000000",
  "advanceRateBps": 5000,
  "advanceAmount": "12.500000",
  "feeBps": 200,
  "fee": "0.250000",
  "txHash": "0x5c2e...",
  "issuedAt": "2026-08-05T09:12:03Z"
}
```

# Appendix B — Repository Structure

```
/
├── cmd/snapfall/             # Go daemon entry point
├── internal/
│   ├── events/               # normalized events + transactional outbox
│   ├── jobs/                 # job state machine
│   ├── tasks/                # DAG and dispatcher
│   ├── agents/               # manifests and worker loop
│   ├── memory/               # namespaces, retrieval, retention
│   ├── actions/              # typed action broker
│   ├── sandbox/              # restricted execution
│   ├── policy/               # deterministic policy evaluator
│   ├── approvals/            # human approval lifecycle
│   ├── treasury/             # signer + advance authorization
│   ├── audit/                # event log, Merkle roots, receipts
│   └── chain/                # JobVault/FloatPool/AuditAnchor indexer
├── payment-sidecar/          # TypeScript x402/Gateway integration
├── paid-demo-api/            # x402 seller used in the demo
├── contracts/                # Foundry: JobVault, FloatPool, AuditAnchor
├── dashboard/                # Next.js app incl. Float page
├── configs/agents/           # employee manifests
├── configs/policies/         # policy YAML
├── demo/                     # seed, reset, rehearsal scripts
└── docs/                     # architecture, ADRs, threat model, deck
```

# Appendix C — Minimum Demo Receipt

| **Section** | **Required fields** |
| --- | --- |
| Job | ID, customer alias, scope, quote, funded amount, dates, final state |
| Workforce | Agents, tasks, duration, completion status |
| Financing | Advance rate, principal, fee, issuance tx, repayment tx, post-job rate |
| Financial | Revenue, each expense + category, budget, remaining, margin |
| Policy | Version, auto-approved count, human decisions, freeze status |
| Payment evidence | Merchant, amount, request/payment/response hashes, Gateway/x402 result, settlement ref |
| Delivery | Artifact name, content hash, checklist, acceptance timestamp |
| Onchain | JobVault, FloatPool addresses, waterfall tx(s), AuditAnchor tx/root |
| Privacy | Statement: full contents local; onchain holds only minimal financial data and hashes |

---

# References

**[R1]** Arc Programmable Money Hackathon — official event brief and submission criteria (community.arc.io, accessed 19 Jul 2026).
**[R2]** Arc Network documentation — USDC gas, deterministic finality, EVM compatibility, use cases (docs.arc.io, accessed 19 Jul 2026).
**[R3]** Circle Agent Stack overview (developers.circle.com/agent-stack, accessed 19 Jul 2026).
**[R4]** Circle Agent Wallets overview (developers.circle.com/agent-stack/agent-wallets, accessed 19 Jul 2026).
**[R5]** Circle Gateway Nanopayments overview (developers.circle.com/gateway/nanopayments, accessed 19 Jul 2026).
**[R6]** Circle quickstart — pay for resources with nanopayments (buyer), accessed 19 Jul 2026.
**[R7]** Circle quickstart — accept payments with nanopayments (seller), accessed 19 Jul 2026.
**[R8]** Arc brand guidelines — "Your brand leads. Arc is the infrastructure." (community.arc.io, 16 Jul 2026).
**[R9]** Circle press release — Agent Stack launch (Circle CLI, Agent Wallets, Agent Marketplace, Nanopayments, Circle Skills), 11 May 2026.
**[R10]** Circle Q1 2026 earnings coverage — x402 ~$24M settled over 30 days, ~99.8% USDC; USDC circulation ~$77B (May 2026).
**[R11]** Chainlink — "Onchain Factoring: Transforming Trade Finance"; trillion-scale trade-finance gap; traditional advances 70–90% of invoice value (Feb 2026).
**[R12]** circlefin/agent-stack-starter-kits — kits for LangChain/Claude Agent SDK/Mastra/OpenAI/Vercel/Google ADK; shared `packages/circle-tools`; Circle CLI + Agent Skills install flow (github.com, accessed 19 Jul 2026). **Verified on read: `circle-tools` is REFERENCE ONLY — source of the x402 wire format (Gateway options identified by `extra.name`); our sidecar implements Arc-testnet-native wrappers directly.** It cannot serve as the sidecar base: `src/chains.ts` declares a closed `type Chain = 'BASE' | 'POLYGON'` with hardcoded mainnet RPCs (no Arc), and it shells out to the `circle` CLI requiring an authenticated session.
**[R13]** Circle App Kits — Bridge/Swap/Send/Unified Balance SDK; Arc_Testnet supported (docs.arc.io/app-kit, accessed 19 Jul 2026).
**[R14]** Circle changelog — "Credit API launched for Settlement Advance" (developers.circle.com, Apr 2026).

---

**Document end. Snapfall v4.1 FINAL · 19 July 2026.**

*Capital in a snap. Settlement in a waterfall. The analysis phase is closed — everything after this document is commits.*
