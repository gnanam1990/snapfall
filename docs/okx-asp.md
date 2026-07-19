# Snapfall Research Copilot — OKX.AI ASP PRD (v1.1 FINAL)

**Side-submission for the OKX.AI Genesis Hackathon. The Arc hackathon (Snapfall) remains the primary mission; this PRD reuses Snapfall components and must never touch its critical path.**

| Field | Value |
|---|---|
| ASP name | **Snapfall Research Copilot** (keeps the Snapfall brand for cross-event traction) |
| Service type | **A2MCP** — standardized pay-per-call API service (no negotiation, no escrow) |
| Event | OKX.AI Genesis Hackathon (HackQuest × OKX) — $100K pool, paid in USDT |
| Target categories | **Finance Copilot** ($7.5K, 3 winners × 2,500 USDT) primary · **Software Utility** ($7.5K) secondary · Social Buzz ($10K across 10) opportunistic |
| Deadline | Google form by **Jul 27, 23:59 UTC** (per live HackQuest page — re-verify in Discord; OKX pages earlier said Jul 17, appears extended) |
| Review gate | ASP must pass OKX internal review and go live — **review turnaround: within 24 hours** (verified on okx.ai/tutorial/asp), result via email + Agent chat; **Agent ID is usable immediately, even pre-approval** |

**One-liner:** Ask for any company, token, or market topic — get back a structured, sourced due-diligence brief in minutes, produced by Snapfall's autonomous research pipeline. Pay per report in stablecoin; no subscription.

---

## 1. What it is

The Research→Delivery slice of the Snapfall workforce, exposed as a standalone OKX.AI service:

- **Input:** a topic ("competitor analysis: X vs Y vs Z", "due-diligence brief on <project/token>", "market overview: <sector>")
- **Pipeline (existing Snapfall components):** Research Agent gathers approved public sources → Delivery Agent synthesizes a structured report (summary, key findings, comparisons, risks, sources) → quality checklist → content hash
- **Output:** clean Markdown/JSON report returned by the endpoint
- **Not included:** JobVault, FloatPool, waterfall, approvals UI — the OKX listing is the *service output only*. Zero new financial contracts.

Finance framing (for the Finance Copilot category): default report templates are finance-flavored — project due-diligence, token/market landscape, competitor teardown — matching the team's market-analysis background.

## 2. Event-fit map

| OKX asks | We show |
|---|---|
| Real-world use case, real users | Anyone researching a purchase, investment, or competitor gets a structured brief without hours of tab-hopping |
| ASP live on okx.ai | A2MCP listing with free tier + x402 paid tier |
| Monetizable service | Pay-per-report pricing; "Revenue Rocket" story: every call is revenue |
| 90-sec demo on X (#OKXAI) | Screen capture: prompt → agents working → report appears → payment receipt |
| Not another chatbot | Deterministic pipeline with roles, checklist, and sourced output — a service, not a conversation |

## 3. Service spec

**Endpoints (A2MCP-compliant):**
- `GET /health` — liveness (required for staying listed)
- `POST /brief` (free tier) — 1-topic, capped-length teaser report; returns result directly (compliant free-endpoint form)
- `POST /brief/full` (paid tier) — full report behind an **x402 payment challenge** (compliant paid-endpoint form; OKX Payment SDK recommended — evaluate vs our existing x402 seller from the Arc build and use whichever passes OKX review cleanly)

**Pricing (initial):** full brief 0.50–2.00 USDT per call depending on depth tier; free tier as funnel. Tune after listing.

**SLA target:** report in ≤5 minutes; timeout returns partial + refund-safe failure (no charge on failure).

**Hosting:** the Snapfall local daemon is NOT the production host — deploy the research pipeline as a small hosted service (any VPS/cloud) with the same code, since an ASP must stay reachable. This is the one genuinely new piece of work.

## 4. Scope (MoSCoW)

- **Must:** hosted endpoint (health + free brief + x402 paid brief) · OKX Agentic Wallet + ASP registration via the okx.ai agent flow (name, description, service list, pricing) · listing submitted for review · one polished sample report · 90-sec demo video · X post with #OKXAI · Google form submitted
- **Should:** 2–3 report templates (due-diligence / competitor / market map) · Snapfall branding + link (cross-traction for the Arc deck: "live users on OKX.AI") · basic rate limiting + input validation
- **Won't:** A2A negotiated mode · escrow/disputes · OKX trading integrations · anything requiring changes to Snapfall's Arc-critical code · custom dashboard

## 5. Build reuse map

| ASP piece | Source |
|---|---|
| Research pipeline | Snapfall Research + Delivery agents (workforce kernel) |
| x402 paid endpoint | Snapfall's week-1 paid demo API (C's critical-path deliverable) — same pattern, OKX-compliant form |
| Report quality checklist | Snapfall FR-DEL checklist |
| New work | Hosted deployment + OKX wallet/registration + pricing config + demo video (~1–2 person-days total) |

## 6. Submission pipeline (verified, in order)

**Registration is prompt-driven through a coding agent. OKX's supported agent list includes OpenClaw — use our own tool and say so publicly.**

**Phase A — OKX-side registration (~1 hour, run inside OpenClaw or Claude Code):**
1. Install Onchain OS skills: `npx skills add okx/onchainos-skills --yes -g` → open a new session
2. Prompt: `Log in to Agentic Wallet on Onchain OS with my email` (have the email ready)
3. Prompt: `Help me register an A2MCP ASP on OKX.AI using OKX Agent Identity from Onchain OS` — follow the agent's guidance: name, description, service list, default pricing. Paid endpoint must be x402-compliant (OKX Payment SDK recommended); free endpoint just returns the result
4. Prompt: `Help me list my ASP on OKX.AI using Onchain OS` → review within 24h (result via email + Agent chat). **Copy the Agent ID now** — it works even before approval
5. A2MCP is fully automatic once live: user agents call the service, paid calls are billed and settled in real time

**Phase B — hosted endpoint:** deploy the research pipeline to a small VPS/cloud (health + free `/brief` + x402 `/brief/full`); verify one free and one paid call end-to-end from a clean client

**Phase C — hackathon submission:**
1. Record the 90-sec demo; post on X with **#OKXAI** (intro + use case + demo). Include the OpenClaw angle: "registered via OpenClaw — the open-source agent we maintain"
2. HackQuest: create a project, then **Start Submit** form (verified fields): select project · **prize tracks multi-select — tick Finance Copilot + Software Utility + Revenue Rocket + Social Buzz** (all that apply, zero extra cost) · ASP Name · **Agent ID** · ASP Description (300 chars) · X handle · X post link · Telegram handle
3. Submit at least 24h before the Jul 27 23:59 UTC deadline; confirm in Discord whether the HackQuest form replaces the Google form mentioned in the overview

## 7. Guardrails & go/no-go

**Hard rule:** this work happens ONLY in slack time of the one team member whose Snapfall workstream is green. If the Snapfall demo spine is red, OKX work stops immediately — the Arc accelerator outranks any OKX prize.

**Go/no-go gate (decide before starting):** proceed only if (a) Snapfall's x402 seller flow already works, (b) the workforce kernel produces a usable report on a stub job, and (c) the deadline on HackQuest still reads Jul 27. Any one false → skip the event, zero guilt.

**A2MCP operational note:** if a delivery dispute somehow arises, ASPs may file arbitration within one day with a 5% bounty deposit (refunded on success) — largely an A2A concern; keep the free tier generous to avoid disputes entirely.

**Risks:** listing rejected (mitigate: 2 review cycles of buffer; free-tier-only fallback is a compliant form) · deadline ambiguity Jul 17 vs 27 (verify in OKX/HackQuest Discord before spending a day) · hosted service costs/keys (use throwaway API keys, small VPS) · report quality variance (constrain templates; cap scope of free tier).

## 8. 90-second demo beats

1. (0–10s) "Research that used to take a day — as a pay-per-call service." Show okx.ai listing.
2. (10–40s) Fire a due-diligence request; show the agent pipeline log lines running.
3. (40–70s) Report appears — scroll the structure: findings, comparison table, risks, sources.
4. (70–85s) Paid call: x402 challenge → stablecoin payment → full report. "No subscription. Pay only for answers."
5. (85–90s) "Snapfall Research Copilot — live on OKX.AI." #OKXAI
