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
  refuses at startup — and when the token is set it is **enforced on every request**
  (`Authorization: Bearer <token>`, constant-time compare, else `401 UNAUTHENTICATED`), on
  every route including the stream. A token that only gates startup while requests go
  unauthenticated would be an auth bypass, not auth. `by` on a decision is a recorded
  label; the bearer token is the authenticated identity when configured.
- **Request guard, in both modes, on every route (owner and customer).** "Anyone who can
  execute code on the owner's machine" does not include a web page open in the owner's
  browser, which can reach loopback too. Before any auth check the daemon refuses:
  a `Host` that is not loopback (`127.0.0.0/8`, `::1`, `localhost`, any port) or listed in
  `SNAPFALL_OWNER_ALLOWED_HOSTS` (`403 BAD_HOST`, defeats DNS rebinding); a `Sec-Fetch-Site`
  other than `same-origin`/`none`, or an `Origin` that is not the request's own host
  (`403 CROSS_ORIGIN`); and a non-GET request whose body is not `application/json`
  (`415 UNSUPPORTED_MEDIA_TYPE`). A bodyless POST with no `Content-Type` is accepted.
  Callers without `Origin` or fetch metadata (curl, Go clients, the dashboard's server-side
  proxy) are unaffected. Tokenless mode stays the local default and logs a startup warning.
- **Non-loopback deployments.** `SNAPFALL_OWNER_ALLOWED_HOSTS` (comma-separated, `host` for
  any port or `host:port`) names the Host a reverse proxy forwards or a LAN name clients use;
  it requires the token. A specific non-loopback bind address is allowed automatically; a
  wildcard bind (`0.0.0.0`, `::`, `:port`) without `SNAPFALL_OWNER_ALLOWED_HOSTS` is refused
  at startup.

## 2. The events stream — ONE stream, TWO sources (decided)

`GET /api/v1/events/stream` (SSE, `text/event-stream`). Message = one JSON `StreamMessage`.

**Decision: the daemon RELAYS Anandan's indexed chain events; the dashboard subscribes to
one stream.** **Implemented 2 Aug 2026** in `daemon/internal/ownerapi/chainrelay.go`. Until then
only the daemon half existed: the indexer wrote `chain_events` and the stream read `events`, two
different tables, so no chain event ever reached a browser and nothing went red about it. Verified basis: the A2 indexer and this daemon open the **same SQLite store**
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
        "chainRef": "0x…",              // daemon events only, OPTIONAL, added 2 Aug 2026: the
                                        //   bytes32 vault job id this event's job maps to on
                                        //   chain. Daemon events are keyed by the business job
                                        //   id and chain events by the bytes32, and neither is
                                        //   derivable from the other (the vault id is generated
                                        //   at createJob, not hashed from the business id), so
                                        //   only the daemon can join the two vocabularies.
                                        //   Absent when the job has no chain presence yet.
        "actor": "approval" | "worker:due-diligence" | ...,
        "at":    "2026-07-24T10:00:00Z",
        "payload": { ... }              // the recorded payload, verbatim — amounts are
      },                                //   ALWAYS base-10 atomic-USDC strings (H1 §2 rule)
      "aggregates": {                   // OPTIONAL — present only when computable from the
                                        //   IMPLEMENTED 2 Aug 2026 (ownerapi/aggregates.go).
                                        //   Previously never sent at all: OPTIONAL had been read
                                        //   as "absent always" rather than "absent until the
                                        //   indexer has run", so the treasury hero and the V11
                                        //   ring rendered an em dash against a real daemon while
                                        //   looking correct against the V5 mock.
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

## 4. Invoices — the owner's read of the chain record (G12)

Two methods, one resource. Generation is a state change and reads are idempotent, so
they are not the same verb:

### 4.1 `POST /api/v1/jobs/{jobId}/invoice` — generate now

The owner-request trigger. The daemon builds a fresh invoice **exclusively from
on-chain rows** (FR-BRN-005; amounts are ALWAYS base-10 atomic-USDC strings, chain
kinds verbatim), records it durably, and returns it. Response `200`:

```jsonc
{ "version": 2,                       // monotonic per job, starting at 1
  "invoice": {                        // the OWNER copy — see the serving decision below
    "copy": "owner", "jobId": "job_demo_1", "vaultJobId": "0x…",
    "status": "complete" | "partial — awaiting chain records",
    "lines":  [ { "kind": "JobFunded", "txHash": "0x…", "block": 100, "logIndex": 2,
                  "payload": { "amountAtomic": "25000000" } } ],
    "gaps":   [ { "stage": "settlement", "cause": "no on-chain settlement", "detail": "…" } ],
    "totals": { "fundedAtomic": "25000000" },   // absent field = NO RECORD, never zero
    "generatedAt": "2026-07-24T10:00:00Z", "disclaimer": "…" },
  "reconciliation": { "outcomes": [ { "receiptHash": "0x…", "outcome": "matched" } ] },
  "alerts": [ /* projection-divergence | expense-outside-policy — owner-only */ ] }
```

`404 UNKNOWN_JOB` for a job the daemon does not know.

**Decision: regeneration is APPEND-ONLY VERSIONED.** Every generation appends a durable
`billing.invoice` event (`version` = 1, 2, …) and replaces nothing — a job whose chain
record is still filling regenerates from `partial` toward `complete`, and a surface can
render "v1 partial → v2 complete" as history. Two generations can never leave two
conflicting records claiming to be *the* invoice: the latest version is the current one,
prior versions are the audit trail.

The daemon MAY also append versions on its own when it observes a settlement for a
tracked job (`JobSettled` reaching the shared store) — same event, same versioning, no
separate vocabulary.

### 4.2 `GET /api/v1/jobs/{jobId}/invoice` — latest recorded

Returns the most recent recorded version, same shape as 4.1. `404 NO_INVOICE` when no
version was ever generated. Never generates.

**Decision: the daemon serves the OWNER copy only, behind §1's auth posture.** A
customer copy exists in the invoice model (plain-language gaps, internals stripped),
but **no daemon route serves it**: which reader gets which copy — Vasanth's layer
scoping by magic link server-side, or a customer-scoped auth path in the daemon — is an
open cross-stream seam, and both sides assuming the other handled it is how gaps
happen. Until that is decided all-three, the customer copy stays unserved; serving it
will be a versioned addition to this contract, not a silent one.

### 4.3 Customer acceptance: `settlement` status

Customer routes are gated by the per-job accept credential (`Authorization: Bearer act_…`),
never by the owner token.

`GET /api/v1/customer/jobs/{jobId}/acceptance`: response `200`:

```jsonc
{ "jobId": "job_demo_1",
  "stage": "accepted",               // the job's stage, verbatim
  "accepted": true,                  // stage == "accepted": the CLAIM happened, nothing more
  "settlement": "none" | "settled" | "pending" | "retry" | "unknown",
  "vaultJobId": "0x…" }              // present only once a chain identity is bound
```

`POST /api/v1/customer/jobs/{jobId}/accept`: response `200`:

```jsonc
{ "jobId": "job_demo_1",
  "state": "accepted-settled" | "accepted-pending-chain" | "accepted-settlement-reverted",
  "settlement": "none" | "settled" | "pending" | "retry" | "unknown" }   // read AFTER the attempt
```

`accepted: true` does **not** mean settled. `settlement` is what says whether pressing
Accept again does anything. It always holds exactly one of five values:

| `settlement` | meaning | portal |
|---|---|---|
| `none` | not accepted yet | show Accept |
| `settled` | a `settlement.executed` is recorded | accepted and settled; hide Accept |
| `pending` | accepted, and nothing the customer does changes anything right now: an attempt is in flight, a broadcast `acceptDelivery` has no outcome yet, the job has no chain identity or no settle lane is wired, or the chain has not reached `Delivered` | Accept is safe but will most likely report pending again |
| `retry` | accepted, and the last attempt reverted, failed, was dropped or was never recorded | show Accept again |
| `unknown` | the status could not be read: a wired chain oracle (settled on chain? `Delivered`?) or the settlement record was unreadable. The rest of the response is accurate. | say the status is unavailable for now; Accept is safe (it re-reads everything and fails closed) |

It comes from the same rules the retried Accept acts on (`brain.nextSettlementStep`). It
is not a second copy of them. When a chain oracle is wired the read consults it (settled
on chain? `Delivered`?). A failed status read is never a guessed value and never fails the
route: both responses stay `200` with `"settlement": "unknown"`, the acceptance read keeps
`jobId`/`stage`/`accepted`/`vaultJobId`, and the Accept keeps its `state` (the Accept was
processed). The detail is logged server-side against the job, never echoed: a chain error
can carry the RPC URL and its API key, and these routes are reachable by any credential
holder.

When the chain shows the job settled and no `settlement.executed` is recorded yet (the
customer's own wallet settled it, or the oracle read failed during the Accept that would
have recorded it), the status read itself records the settlement, as an observation and
never a transaction, and answers `settled`. The `settlement-resolver` records the same
observation on its next pass whether or not anyone reads the status. It is the same record
an Accept writes: `settlement.executed`, signer derived from the indexed `JobSettled` hash
(a hash the daemon's lane recorded is `daemon_custodial`, any other is `customer`; not yet
indexed is `customer` with `tx_hash_pending`). A self-custody customer has no Accept left to
press, so nothing waits on one. A frozen job reads and records the same way: the kill switch
stops settlement transactions, not the record of one the chain already shows (a frozen job
can read `retry`, and the Accept answers `423 FROZEN`).

A failed Accept is `500 INTERNAL` with a generic message (`"the acceptance could not be
processed; try again shortly"`), the detail logged server-side. `401 UNAUTHENTICATED` (bad
or missing credential), `404 UNKNOWN_JOB`, `409 NOT_READY`, `423 FROZEN`, `503 NOT_WIRED`
(any customer seam, `settlement` included, unwired).

**Settlement recovery, as recorded (daemon event log, per job):**

- `settlement.submitted` is written **write-ahead**: once the lane's `acceptDelivery` is
  signed and before it is sent. The lane signs locally, so the record holds `tx_hash`,
  `nonce`, `signer_address` (the signing account), `raw_tx` (the signed bytes) and
  `sent_head` (the chain head it was signed at) before any node sees the transaction. If
  the record cannot be written, nothing is sent and the Accept fails with nothing out.
  Once it is written, a send error (a timed-out `eth_sendRawTransaction` the node accepted,
  or an error from one backend of a load-balanced RPC) is never read as "not sent": the
  job is `pending` and the record is resolved by hash. Only a submission refused before
  signing (gas estimation reverts) is retried as never sent.
- Open broadcasts are resolved at boot (a pass bounded to 20 s; whatever it does not reach
  waits for the next one), every minute by the `settlement-resolver` worker, and on every
  Accept. All three run the same code. It records outcomes and never
  submits at a new nonce. It may re-send an evicted transaction's same bytes, or
  fee-replace a stalled one at its own nonce (both below). A receipt gives
  `settlement.executed` or `settlement.reverted`. The mined nonce compared is always the
  signer's: `signer_address`, or the account `raw_tx` recovers to. A record with neither is
  unresolvable and is never called dropped. Rotating the key across a restart cannot
  redirect it. A recorded `raw_tx` is verified before any use: it must decode to the
  recorded `tx_hash` at the recorded `nonce`, signed by `signer_address` when one is
  recorded. A record that fails gives no signer (unresolvable, never dropped), no previous
  fee and no bytes to re-send.
- Every transaction at one nonce is resolved together: the original, plus each fee
  replacement linked to it by `replaces`. A receipt for any one decides the nonce. Every
  other transaction at that nonce is recorded `settlement.dropped` with `taken_by`.
- A transaction with no receipt that the node does not know (`eth_getTransactionByHash`
  null) is one observation, recorded as `settlement.unseen` with `verdict` and chain
  `head`. It acts only when a later observation with the same verdict is taken at a
  strictly higher head. A load-balanced RPC whose nonce backend is ahead of its receipt
  backend makes a mined transaction look exactly like this.
  - verdict `dropped` (signer nonce past it): confirmed → `settlement.dropped`, and the
    job becomes `retry`.
  - verdict `evicted` (signer nonce still open): confirmed → the newest transaction's
    recorded `raw_tx` is re-sent unchanged (`settlement.rebroadcast`). It has the same nonce
    and the same hash, so it cannot settle twice. Nothing is re-sent while the job is frozen.
    Each re-send starts the observations over, so one transaction is re-sent at most 3
    times; after that it is left to fee replacement, like one whose re-send the node refuses.
- **Fee replacement.** A transaction the node holds but does not mine (priced under what
  the chain takes) never sees its nonce move. The same applies to an evicted one whose
  re-send the node refuses or that has no `raw_tx`. Either one would stay `pending` forever.
  After it has sat 240 blocks (about two minutes of Arc's 500 ms blocks) since its
  `sent_head`, `acceptDelivery` is re-signed at the **same nonce** with a higher fee. The
  fee is the highest of a fresh `eth_gasPrice`, the latest block's fresh base fee (never
  cached), and the previous price (read only from verified `raw_tx`) plus a quarter, and
  never more than 3× the fresh price, max(`eth_gasPrice`, base fee). The replacement is
  recorded write-ahead as a new `settlement.submitted` carrying `replaces`. At most one
  transaction per nonce mines, so replacing cannot settle twice. Nothing is replaced while
  the job is frozen.
- **`settlement.stuck`** is recorded once per transaction, logged at ERROR, and after it
  nothing more is replaced automatically (the transaction is still resolved). Its `reason`
  is one of: `max_replacements` (a nonce got 3 replacements and is still unmined),
  `signer_mismatch` (another key signed it, typically after a key rotation, so this lane
  cannot sign at that account's nonce), `fee_cap` (the previous price plus a quarter is
  over 3× the fresh price), `record_mismatch` (its `raw_tx` is not the recorded
  transaction), `signer_unknown` (the record names no signer and has no `raw_tx`).
  `signer_unknown` and `record_mismatch` are also recorded when the lane cannot resolve such
  a transaction at all (no receipt, unknown to the node, and no signer to compare a nonce
  against): nothing is sent, and the job stays `pending` until the owner acts.
- **Manual recovery** applies to a `settlement.stuck` job, or to a record the lane cannot
  sign for (another key signed it). The operator sends any transaction from that record's
  signer at its nonce, for example a zero-value self-transfer at a fee the chain takes. The
  broadcasts then confirm as dropped and the next Accept resubmits. JobVault refuses a
  second `acceptDelivery` for a settled job, so this cannot double-settle.

## 5. What ships when

- **Daemon-source stream + approvals endpoints:** implemented with this document, on the
  G8 stack (`Lifecycle.Pending`/`Decide` are the primitives).
- **Invoice endpoints (§4):** implemented with G12 Step 2. Honesty note: the
  settlement-observed generation path cannot fire until the chain gap closes (no
  deployment → no `JobSettled` rows → nothing to observe); the owner-request trigger is
  the exercisable one today.
- **Chain-source relay:** the contract above; the code feature-detects the `chain_*`
  tables (present on `main` since PR #6; the G8 stack gains them on merge) and begins
  relaying when they exist. Until then `source:"chain"` messages simply do not occur —
  the dashboard needs no special case.
- The V5 mock is retired by pointing the dashboard's `EventSource` at
  `127.0.0.1:4010/api/v1/events/stream` — same envelope, real data.
