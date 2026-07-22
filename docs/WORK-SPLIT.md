# SNAPFALL — WORK SPLIT (FINAL)

**Vasanth · Gnanam · Anandan — three equal build streams, ~12–13 build-days each against 16 working days. Pure engineering; shared story strip at the end counts against nobody. Convert to GitHub issues tonight: labels `vasanth` `gnanam` `anandan`, milestones = the four phases.**

**One structural note:** the three columns are swappable as whole units — if the person who built the contracts isn't Gnanam, swap the Gnanam/Anandan columns entirely. The balance holds either way. One owner per task; helping ≠ owning; ownership moves only at standup.

---

## 1. THE THREE HANDSHAKES — design together Wed 22 Jul evening (90 min), FREEZE Fri 24 Jul

| # | From → To | Contents | Done when |
|---|---|---|---|
| **H1** | Anandan → Gnanam | Indexer event schema written to shared SQLite: `JobFunded, AdvanceIssued, ExpenseRecorded, DeliverySet, JobSettled(advanceRepaid, operatorNet), AdvanceWrittenOff, RateUpdated` | Schema file committed; both sides parse a sample fixture |
| **H2** | Gnanam → Vasanth+Anandan | REST endpoints (jobs, advances, pool, approvals, receipts) + SSE stream schema + approval-decision POST shape | OpenAPI-ish schema committed; dashboard renders a mocked stream |
| **H3** | Vasanth → Gnanam | Payment sidecar API: `quote(resource)`, `pay(intent, approvalToken)`, `status(paymentId)` — Funding agent is the only consumer | Schema committed; Funding calls a mocked sidecar successfully |

After Friday: schema changes need all-three agreement + version bump. This is what lets three people build in parallel without blocking.

---

## 2. VASANTH — Money Rails & Product Face (~12.5 build-days)

### Phase 1 · Tue 21 – Sun 26 Jul
- **V1. 🚨 x402 buyer loop — TONIGHT.** `@circle-fin/x402-batching` via **Circle's facilitator**: 402 → sign → retry → 200 against our own paid API. *Done when:* a real four-cent purchase completes on testnet and the raw request/response pair is committed as a fixture. Screen-record the first success. **Red by Wed morning → all three swarm.** *(1.5–2d)*
- **V2. Paid demo API (seller).** Three endpoints: `$0.04` company profile, `$0.06` benchmark, `$4.00` premium (the rejection-beat bait). Returns 402 payment-required correctly, serves JSON after payment. *Done when:* V1's client buys all three end-to-end. *(0.5d)*
- **V3. Circle stack setup.** Gateway testnet deposit, Agent Wallet with spend-policy configured (outer guard), Circle CLI installed and hitting our wallet. *Done when:* CLI lists the agent wallet and a policy rule demonstrably blocks an over-limit test payment at the wallet layer. *(0.5d)*
- **V4. Sidecar service.** Wrap V1 behind the H3 API with auth; idempotent `pay` via nonce. *Done when:* Gnanam's Funding stub completes a purchase through it. *(0.5d)*
- **V5. Dashboard scaffold.** Next.js layout, nav, SSE client per H2, Overview page with live treasury/pool balances. *Done when:* a fired test event appears on screen ≤2s. *(1d)*

### Phase 2 · Mon 27 Jul – Sun 2 Aug
- **V6. Funding agent.** Wraps the treasury signer; accepts only Brain-relayed owner-approved instructions; reserves budget on sign, reconciles on settle, releases on fail/expiry; calls sidecar via H3. *Done when:* AT-15 passes (non-Brain caller rejected) and a reserved-then-failed payment releases budget correctly. *(1.5d)*
- **V7. Job detail page.** Lifecycle state, task timeline, budget bar, advance card (rate/principal/fee/status), expense rows with explorer links (Anandan's helper). *Done when:* a full spine run renders every state transition live. *(1d)*
- **V8. Approvals inbox.** Approve / Reject / Find-cheaper wired to Gnanam's approval lifecycle; decision bound to intent hash. *Done when:* the rejection beat works end-to-end from a browser click. *(1d)*
- **V9. F7 Customer magic-link portal.** Tokenized customer view: status → **Accept** (fires the waterfall via H2) → receipt. Privacy boundary: no internal memory, prompts, policies, or other jobs ever served on this route. *Done when:* clicking Accept produces the on-chain settlement tx and the receipt renders from real data. *(1d)*

### Phase 3 · Mon 3 – Wed 5 Aug
- **V10. F1 Live Money Graph.** The "watch the Snapfall" screen: fund → snap → spend droplets → waterfall filling pool-first, operator-second, driven by real SSE events (replayable for rehearsal). *Done when:* a live spine run animates correctly with zero manual triggers. *(1.5d)*
- **V11. F2 Score Ring.** Advance-rate ring animating 50%→55% on settlement + "next job unlocks 13.75" line, value read from chain via H2. *Done when:* the ring updates from the real RateUpdated event. *(0.5d)*
- **V12. Seed + reset scripts.** One command: demo org, funded customer wallet, 100 USDC pool seed, clean state. *Done when:* reset → full spine → reset → full spine, twice in a row, no manual fixes. *(0.5d)*

**Vasanth total ≈ 12.5d**

---

## 3. GNANAM — Brain Runtime & Agents (~13 build-days)

### Phase 1 · Tue 21 – Sun 26 Jul
- **G1. Daemon skeleton.** Supervisor, process lifecycle, structured logs with correlation IDs (org/job/task/intent/advance), config load. *Done when:* a killed worker restarts cleanly with state intact. *(1d)*
- **G2. Event store + transactional outbox.** SQLite WAL; append-only events, monotonic sequence; outbox pattern for side effects. *Done when:* a crash between write and side-effect replays exactly once. *(1d)*
- **G3. Brain router core.** Message envelope, routing table, THE LAW in code: agent→Brain is the only channel that exists — Worker→Funding has no code path, not a blocked one. *Done when:* AT-16 passes (the call is impossible, not denied). *(1d)*
- **G4. Per-job memory files.** Create/update/replay: scope, stage %, assigned worker, timestamped owner confirmations, escrow state. *Done when:* two concurrent jobs show zero context bleed in a scripted test. *(1d)*
- **G5. Owner chat surface.** Brain scopes a request → presents quote → records confirmation before any work starts. Stub LLM acceptable this week. *Done when:* stub DD job routes scope→confirm→assign→report end-to-end. **This is the Sun 26 exit gate.** *(1d)*

### Phase 2 · Mon 27 Jul – Sun 2 Aug
- **G6. Policy engine.** Pure-code evaluation: job budget, per-tx, daily cumulative, merchant allowlist, **blocked categories** (`token-trading`, `gambling` default-blocked). Returns AutoApprove / HumanApproval / Deny + machine-readable reasons. *Done when:* the full policy fixture table (12+ cases incl. every deny reason) passes. *(1.5d)*
- **G7. Approval lifecycle.** Request → decision → execution binding to exact intent hash; parameter change invalidates (AT-05); expiry; idempotent decisions. *Done when:* AT-03/04/05 pass. *(1d)*
- **G8. DD-worker.** Task loop, structured outputs, source purchases via PaymentIntents, **compliance step**: Circle Compliance Engine call wrapped with confidence + "evidence, not a guarantee." *Done when:* full DD task produces a report from real purchased data on testnet. *(1.5d)*
- **G9. QA-worker.** Independent review pass: completeness, source coverage, unsupported-claim detection, customer-data-leakage check; bounce-with-reasons loop. *Done when:* AT-19 passes (a planted unsupported claim blocks DeliveryReady until revised). *(1d)*
- **G10. Discovery.** Embedding match of fuzzy need against Agent Marketplace catalog; suggest-never-authorize boundary (selection output feeds a PaymentIntent, never a payment). *Done when:* DD-worker finds V2's API by description, not hardcoded name. *(1d)*

### Phase 3 · Mon 3 – Wed 5 Aug
- **G11. Kill switch + restart recovery.** Freeze org/job/agent ≤1s stops new tasks, signatures, advances; Brain memory replays from event log after restart. *Done when:* AT-09 + extended AT-10 pass (kill mid-job, restart, no repeated payment or advance). *(1d)*
- **G12. Billing agent.** Deterministic invoice formatter from indexer data + per-job memory; owner + vendor copies. *Done when:* invoice totals reconcile to chain to the cent on a real spine run. *(1d)*
- **G13. Approval-fatigue digest (P1 — first cut).** Similarity triage → daily digest + "always allow pattern"; novel interrupts immediately; policy engine still authorizes. *(1d)*

### Phase 4 · Thu 6 – Sat 8 Aug
- **G14. AT-01..19 green in CI**; fix-forward on spine failures. *(reserve)*

**Gnanam total ≈ 13d (12 effective — G13 is first-cut)**

---

## 4. ANANDAN — Chain Layer, Integration & Product Surface (~12.5 build-days)

### Phase 1 · Tue 21 – Sun 26 Jul
- **A1. Deployment config export.** Addresses, ABIs, chain config, funded-wallet set committed as machine-readable config (this is build config, not docs — everything downstream imports it). *Done when:* Gnanam's and Vasanth's code read chain config from it. *(0.25d)*
- **A2. Go indexer.** Subscribe/poll JobVault, FloatPool, AuditAnchor; parse all seven H1 events; confirmation-depth handling. *Done when:* a testnet spine run appears as normalized rows. *(1.5d)*
- **A3. Indexer → event store.** Idempotent writes per H1 (re-run safe, no dupes); backfill from block N. *Done when:* running the indexer twice produces identical state. *(1d)*
- **A4. Reconciliation engine.** Local ledger vs chain to the cent; mismatch = structured alert + dashboard flag. *Done when:* an injected fake local row triggers the alert in a test. *(1d)*

### Phase 2 · Mon 27 Jul – Sun 2 Aug
- **A5. Explorer helper.** tx/address → explorer URL, exposed through H2 for every financial row. *(0.25d)*
- **A6. CI + test wiring.** Contracts' 84 tests in CI; wire AT-16/17/18 harnesses (17: second advance on prior job reverts; 18: facilitator endpoints asserted to be Circle's from V1's fixtures). *Done when:* one CI run executes contract + integration suites green. *(1d)*
- **A7. Testnet ops kit.** Wallet funding script, balance checks, redeploy script honoring the 48h cadence, gas/faucet guards. *Done when:* one command reports all wallets healthy or tops them up. *(0.5d)*
- **A8. USYC sweep.** Interface + mock implementation; timebox 0.5d to investigate real testnet USYC — integrate if trivially available, else mock ships. *(0.5–1d)*
- **A9. F3 Humanized activity feed.** Named agents (Brain, workers) rendered as team chat from the event stream; hosts the approval moment surface Vasanth's inbox actions land in. *Done when:* the rejection beat reads as a conversation on screen during a live run. *(1d)*
- **A10. Float page.** TVL, utilization, fees, reserve, org rate + derivation (acceptedJobs/writeOffs from chain), open advances table. *Done when:* every number matches a manual chain query. *(1d)*

### Phase 3 · Mon 3 – Wed 5 Aug
- **A11. Build-Monitor worker + fresh-job orchestration.** Repo watcher reporting completion % to Brain before any release; each milestone spins a fresh JobVault instance through the identical loop (runs inside Gnanam's worker framework; Anandan owns the worker + the milestone-cycle orchestration). *Done when:* AT-17 passes live — milestone 2 creates job 2, advance 2, settlement 2, and the rate ticks again. *(2d)*
- **A12. F4 Hire cards.** "Grow your team" gallery over worker manifests; hire click activates the manifest. *Done when:* hiring the Build-Monitor from the UI makes it start watching. *(0.5d)*
- **A13. Telegram approvals (P1 — cut to dashboard-only if behind).** Bot mirrors the approval inbox; decisions deep-link + post back through H2. *(0.5d)*

### Phase 4 · Thu 6 – Sat 8 Aug
- **A14. Secret + integrity audit.** Repo history, logs, screenshots scanned for keys/secrets; recording-integrity check on video fixtures (all txs live). *(0.5d)*
- **A15. Integration firefighting reserve** — spine-run debugging is everyone's job, but Anandan owns the reconciliation truth, so chain-vs-local disputes route to him first. *(reserve)*

**Anandan total ≈ 12.5d**

---

## 5. SHARED STORY STRIP — Thu 6 → Sat 8 Aug (counts against nobody's build load)

All three, together, roughly half a day each — this is not a workstream, it's the finish line:
- **Thu 6:** record the demo (spine + generalization beat; integrity rule: replay of real runs, live transactions, disclosed caption). Vasanth drives screen, Gnanam drives narration takes, Anandan verifies every tx on explorer live.
- **Fri 7:** edit video · deck (10 slides incl. Circle DX feedback) · README final pass — split the three artifacts one each, two hours apiece.
- **Sat 8:** submit evening IST; every link verified from an incognito browser by a different person than the one who uploaded it. Sun 9 = AoE contingency only.

---

## 6. RITUALS, GATES, TRIGGERS

- **Daily standup, 15 min, fixed time.** Yesterday / today / blocked. Blocked >4h → escalate, don't grind.
- **Wed 22 evening:** H1/H2/H3 design session (all three, 90 min). **Fri 24:** freeze — changes after need all-three + version bump.
- **Sun 26 exit gate:** Gnanam's stub DD job routes end-to-end; Vasanth's V1–V4 money loop done; Anandan's indexer showing real events.
- **Wed 29 Jul onward: daily spine run** — full DD loop on testnet, pass/fail logged in repo. **Red spine outranks all feature work for all three.**
- **Swarm triggers (pre-agreed, no debate needed):** V1 red Wed morning · spine red 2 days running · reconciliation mismatch unexplained >4h.
- **Scope-cut order:** G13 digest → A13 Telegram → A8 stays mock → A11 beat compresses to 15s → A12 hire cards. **Never cut:** snap, fall, rejection beat, QA beat, portal accept, money graph, $0-start.

**Load check: Vasanth 12.5 · Gnanam 13 (12 effective) · Anandan 12.5 — equal thirds, ~3.5 days margin each for the integration tax. The margin will be eaten. That's what it's for.**

*Ratify at standup → GitHub issues → build.*
