# Snapfall H2 — Owner API: Events Stream + Approvals (v1.0-rc)

**Status:** Design for ratification, written against Vasanth's merged V5 mock. **Freezes:** Fri 24 Jul 2026.
**After freeze:** changes require all-three sign-off and an `x-h2-version` bump.
**Producer:** Gnanam's daemon (`cmd/snapfall`, HTTP on localhost). **Consumers:** Vasanth's dashboard (V5 Overview, V8 Approvals, V10/V11 via the chain relay), the portal later.

H2 is the owner surface's server half: one SSE stream the dashboard renders, and the
approval endpoints that stand between an escalated intent and a signature. The V5 mock
(`dashboard/app/api/events/stream/route.ts`) already renders this stream's shape; this
document is the missing half of the done-criterion ("schema committed").

## 1. Transport, base, and the auth posture (decided, not assumed)

- Base: `http://127.0.0.1:4010/api/v1` (port via `--api-addr`; the daemon binds **127.0.0.1 only**).
- **Auth: NONE, as a written decision.** This is a local-first daemon; the API binds
  loopback and never another interface. The consequence is stated plainly: *anyone who can
  execute code on the owner's machine can approve payments* — accepted for the demo, in
  line with the sidecar's on-host trust posture. **Any non-loopback bind MUST add bearer
  auth first** (a `SNAPFALL_OWNER_TOKEN` env, ≥32 bytes, same rules as the sidecar's
  `SIDECAR_AUTH_TOKEN`). Binding non-loopback without it is a misconfiguration the daemon
  refuses at startup.

## 2. The events stream — ONE stream, TWO sources (decided)

`GET /api/v1/events/stream` (SSE, `text/event-stream`). Message = one JSON `StreamMessage`.

**Decision: the daemon RELAYS Anandan's indexed chain events; the dashboard subscribes to
one stream.** Verified basis: the A2 indexer and this daemon open the **same SQLite store**
(the indexer's own flag: "shared SQLite database"; one `store/schema.sql` holds both the
daemon's `events` table and the `chain_*` tables). Relaying is a read of `chain_events`
in the shared store — no second subscription, no cross-process protocol, one reconnect
story for the dashboard.

Two event **sources**, each keeping its already-frozen vocabulary — H2 renames nothing:

| `source` | Vocabulary | Origin | Feeds |
|---|---|---|---|
| `"daemon"` | the daemon event log kinds, verbatim: `policy.evaluated`, `approval.requested`, `approval.approve\|reject\|request_alternative`, `approval.expired`, `payment.executing\|executed\|failed`, `purchase.pending_settlement`, `task.withheld`, `freeze.engaged\|lifted`, `brain.msg.*` (incl. `worker.qa_verdict`, `brain.job_report`) | daemon `events` table / bus | activity feed, approvals inbox, QA beat |
| `"chain"` | the **eight frozen H1 kinds**, verbatim: `JobFunded, AdvanceIssued, ExpenseRecorded, DeliverySubmitted, JobSettled, AdvanceRepaid, AdvanceWrittenOff, RateChanged` (per H1 + the PR #7 ruling: contract names win) | `chain_events` in the shared store, relayed in `(blockNumber, logIndex)` order | V10 Money Graph, V11 Score Ring, Float page |

The dashboard's internal display names (`rate.updated`, `payment.delivered`, …) are
presentation-layer and stay out of the wire contract; V11's WORK-SPLIT note already
migrates the Score Ring to `RateChanged`.

### 2.1 Envelope

```jsonc
StreamMessage =
  | { "kind": "snapshot", "snapshot": Snapshot }          // first message on connect
  | { "kind": "event",
      "source": "daemon" | "chain",
      "seq": 123,                       // daemon: events.seq | chain: cursor "block:logIndex"
      "event": {
        "kind":  "policy.evaluated" | "RateChanged" | ...,   // per the source's vocabulary
        "jobId": "job_demo_1",          // daemon events; chain job events carry entityId
        "entityId": "0x…",              // chain only (bytes32 jobId, or org address for RateChanged)
        "actor": "approval" | "worker:due-diligence" | ...,
        "at":    "2026-07-24T10:00:00Z",
        "payload": { ... }              // the recorded payload, verbatim — amounts are
      },                                //   ALWAYS base-10 atomic-USDC strings (H1 §2 rule)
      "aggregates": {                   // OPTIONAL — present only when computable from the
        "treasuryUsdc": "12500000",     //   shared store's chain projections; the dashboard
        "pool": { ... },                //   keeps last-known when absent (mock parity: the
        "openAdvances": [ ... ],        //   V5 mock always sends these; the real feed sends
        "activeJobs": [ ... ],          //   them when the indexer's tables are populated)
        "pendingApprovals": 2
      } }
```

`Snapshot` is the V5 mock's `OverviewSnapshot` shape with every field **nullable/zero when
its source is not yet present** (e.g. chain aggregates before the indexer has run) — the
mock's fixture values are placeholders, not contract.

Reconnect: SSE `Last-Event-ID` carries the last daemon `seq`; the daemon replays daemon
events with `seq >` it and re-sends a fresh snapshot first. Chain relay resumes from the
relay cursor; replays are harmless (idempotent by `seq`/cursor on the client).

## 3. Approvals — the decision surface (V8's server half)

### 3.1 `GET /api/v1/approvals`
Pending approval requests, oldest first:

```jsonc
{ "approvals": [ {
    "requestId":  "apr_05779ad27ff0",
    "jobId":      "job_demo_1",
    "intentHash": "0x…",               // the FULL-intent hash the decision binds to (AT-05)
    "merchant":   "api.research-data.example",
    "resource":   "GET /v1/premium-dataset",
    "amountUsdc": "4000000",           // atomic string
    "purpose":    "premium market dataset",
    "expiresAt":  "2026-07-24T10:05:00Z",
    "alternativeTo": ""                 // non-empty when this intent replaces a rejected one
} ] }
```

### 3.2 `POST /api/v1/approvals/{requestId}/decision`

```jsonc
{ "kind": "approve" | "reject" | "request_alternative",
  "by": "gnanam",                      // required; the recorded decider
  "reason": "too expensive — find a cheaper source",
  "intentHash": "0x…" }                // REQUIRED: the hash the owner was SHOWN
```

**Decision: `intentHash` is required and checked.** AT-05 already establishes a decision
binds to an exact intent; omitting the hash would let an owner approve a stale view and
discover the mismatch only at execution time. Money is safe either way (Execute re-verifies)
— this is the UX property: reject at click time with the current view, not silently later.

Responses (all JSON):
| HTTP | code | when |
|---|---|---|
| 200 | — | decision recorded; body = the terminal request (state, decidedBy, reason) |
| 409 | `STALE_VIEW` | body `intentHash` ≠ the request's hash; body carries the CURRENT approval view so the UI re-renders and re-asks |
| 409 | `ALREADY_DECIDED` | terminal request + a conflicting decision (same-decision repeat is a recognized 200 no-op, G7 idempotency) |
| 410 | `APPROVAL_EXPIRED` | the window elapsed before the decision |
| 404 | `UNKNOWN_REQUEST` | no such requestId |
| 400 | `BAD_REQUEST` | malformed body / unknown kind / empty `by` |

`request_alternative` + a worker adaptation produces a NEW intent whose `alternativeTo`
names this request (G7 validates the link) — the activity feed renders rejection and
replacement as one causal story.

## 4. What ships when

- **Daemon-source stream + approvals endpoints:** implemented with this document, on the
  G8 stack (`Lifecycle.Pending`/`Decide` are the primitives).
- **Chain-source relay:** the contract above; the code feature-detects the `chain_*`
  tables (present on `main` since PR #6; the G8 stack gains them on merge) and begins
  relaying when they exist. Until then `source:"chain"` messages simply do not occur —
  the dashboard needs no special case.
- The V5 mock is retired by pointing the dashboard's `EventSource` at
  `127.0.0.1:4010/api/v1/events/stream` — same envelope, real data.
