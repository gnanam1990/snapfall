# Snapfall H3 — Payment Sidecar Interface Contract (v1.0-rc, adversarially reviewed)

**Status:** Design proposal for the Wed 22 Jul handshake session. **Freezes:** Fri 24 Jul 2026.
**After freeze:** any change needs all-three sign-off + an `x-h3-version` bump.
**Owner:** Vasanth (V4 sidecar) · **Sole consumer:** Funding agent (Gnanam, V6) · **Upstream:** H2 approval decision (Gnanam) · **Downstream:** the existing x402 loop in `sidecar/src/{buyer,x402,seller}.ts` against Circle's facilitator.

> This version incorporates a 3-lens adversarial review (implementer / security / sidecar-fit).
> The review found that a naive "thin wrapper that calls `purchase()` as-is" **does not** defend
> AT-05 and is open to a crash-window double-pay. The contract below closes both. Read §6 first
> if you own `buyer.ts`: **this requires a bounded refactor of `buyer.ts`, not just a wrapper.**

---

## 0. Vocabulary and invariants

- **Atomic USDC:** every amount is a decimal string of 6-dp atomic units (`"40000"` = 0.04 USDC), matching `AcceptOption.amount` / `Authorization.value`. Never floats; `bigint` is always a string on the wire.
- **Network:** CAIP-2, `"eip155:5042002"` (Arc testnet), derived as `eip155:${chainId}`.
- **Two nonces, never conflated** (the review flagged the naming collision):
  - `idempotencyNonce` = the PaymentIntent nonce. It is the H3 idempotency key **and** a hashed term of the intent (§3.3). One per approved intent.
  - `authNonce` = the 32-byte EIP-3009 `TransferWithAuthorization` nonce actually signed on-chain. In this contract it is **derived deterministically** from the intent (§4.4), not random, so a re-execution reproduces the same on-chain nonce and the seller's replay guard can catch a duplicate.
- **THE LAW placement:** H3 is not an inter-agent channel. Only the Funding-agent process holds the credential; Workers and Brain have no token and no code path (G3).
- **Two-layer authorization:** H3 expresses the *inner* guard (deterministic policy + owner approval + pre-sign equality checks). Circle's Agent-Wallet spend policy is the *outer* guard (payee/amount allowlist enforced at the wallet layer). Neither alone is sufficient; see §8 on why the outer guard is load-bearing for "Circle-facilitator-only."
- **The substitution defense is enforcement, not just a hash.** `intentHash` binds `merchant/amount/asset/network` (§3.3), but a hash is inert unless the signer actually consumes those exact values. So `pay` compares the **live challenge** against the approved intent and signs against that one validated object (§2.2, §6). The hash and the live-equality check are the two defenses, and both run before any signature.

---

## 1. Transport + auth model

### 1.1 Binding
- The sidecar binds **`127.0.0.1` (loopback only)** on **`SIDECAR_PORT` (default `4020`)**. It MUST NOT bind `0.0.0.0` or any external interface. Non-loopback packets are dropped by the kernel, so no off-host caller can reach it regardless of credentials.
- The paid-resource server (`seller.ts`, `PAID_API_PORT=4021`) and Circle's facilitator are upstream of the sidecar; Funding never talks to them directly.

### 1.2 Authentication — bearer token
- Every request MUST carry `Authorization: Bearer <SIDECAR_AUTH_TOKEN>`.
- `SIDECAR_AUTH_TOKEN` is a >=32-byte random secret provisioned into **only** the Funding-agent and sidecar process environments (new env var, §9). Compared constant-time (`crypto.timingSafeEqual`); missing/blank/mismatch gives `401 UNAUTHENTICATED`. Never logged.
- **Token over mTLS** because the channel is loopback-only and single-consumer; mTLS adds a cert/CA lifecycle with no attacker surface to close on `127.0.0.1`. mTLS is the documented upgrade path if the sidecar ever moves off-host (which the freeze forbids without a version bump).

### 1.3 Reachability
Reaching `pay` requires **both** a loopback socket on this host **and** the secret. A leaked token is inert off-host; loopback alone is inert without the token.

**Load-bearing assumption (from security review):** on-host process isolation and env-var confidentiality are part of the trust boundary. Any same-host process that can read the Funding process environment can call `pay`. This is acceptable for the demo threat model but must be stated, not assumed away.

### 1.4 Common wire rules
- `application/json; charset=utf-8` on all bodies. Every response carries `x-h3-version: 1.0`.
- Every non-2xx body is the uniform error envelope (§2.0). Clients branch on the stable `code` enum, never on prose.
- Unknown request fields are ignored; missing required fields give `400 BAD_REQUEST`; non-canonical atomic amounts give `400 BAD_REQUEST`.

---

## 2. The three operations

### 2.0 Uniform error envelope
```json
{
  "error": {
    "code": "STRING_ENUM",          // stable, see §2.4
    "message": "human-readable, safe to log",
    "paymentId": "pay_… | null",    // non-null ONLY when a durable record exists (§4); null for pre-sign failures
    "retriable": false,             // true only for transient transport/facilitator faults
    "details": { }                  // optional, code-specific (e.g. quoted, reserved, sellerReason)
  }
}
```
The envelope MUST carry `paymentId` for every error raised at or after the durable-record write (§2.2 step 4), including `409 PAYMENT_IN_PROGRESS`, so Funding can always reconcile via `status` even when a `pay` response is lost.

### 2.1 `quote` — price a resource, fetch the payment challenge
Read-only. No key touched, no reservation, no side effect. Output feeds a `PaymentIntent` for approval.

- **`POST /v1/quote`**

**Request**
```json
{ "resource": "http://127.0.0.1:4021/v1/company-profile", "chainId": 5042002 }
```
Note the scheme is `http://` — `seller.ts` serves plain `node:http`. Using `https://` would fail to connect.

**Response `200`**
```json
{
  "resource": "http://127.0.0.1:4021/v1/company-profile",
  "network": "eip155:5042002",
  "accept": {                        // the selected AcceptOption, verbatim from x402.ts
    "scheme": "exact", "network": "eip155:5042002",
    "amount": "40000", "asset": "0x…", "payTo": "0x…",
    "maxTimeoutSeconds": 300,
    "description": "Competitor company profile (0.040000 USDC)",
    "extra": { "name": "USD Coin", "version": "2" }
  },
  "price": "40000",
  "priceDisplay": "0.040000",
  "quotedAt": "2026-07-22T10:00:00Z",
  "quoteExpiresAt": "2026-07-22T10:05:00Z"   // ADVISORY only; pay re-probes live and does not enforce this
}
```
Funding copies `accept.payTo → intent.merchant`, `accept.asset → intent.asset`, `accept.amount → intent.amount`, `accept.network → intent.network`, sends the intent through H2, then calls `pay`.

**Errors**

| HTTP | `code` | Cause |
|---|---|---|
| 401 | `UNAUTHENTICATED` | Missing/invalid bearer token |
| 400 | `BAD_REQUEST` | Missing `resource`/`chainId`, malformed URL |
| 404 | `RESOURCE_NOT_FOUND` | Probe did not return `402` |
| 422 | `NO_MATCHING_NETWORK` | No `accept` for `eip155:${chainId}` |
| 502 | `CHALLENGE_UNAVAILABLE` | `readChallenge` returned `null` or `accepts.length === 0` (retriable) |
| 502 | `UPSTREAM_UNREACHABLE` | Resource host refused/timed out (retriable) |

### 2.2 `pay` — execute an owner-approved intent
Verifies the approval, binds it to the exact intent by hash, **compares the live challenge to the approved terms before signing**, signs one EIP-3009 authorization, submits `X-PAYMENT`, returns the receipt. Idempotent by `idempotencyNonce` (§4).

- **`POST /v1/pay`**

**Request**
```json
{ "intent": { /* ApprovedIntent — §3.1 */ }, "approvalToken": { /* §3.2 */ } }
```

> **Reconciled with the implementation (review, 22 Jul).** The earlier draft wrote a
> `RECEIVED` record *before* validation. The implementation instead persists nothing
> until every pre-sign check passes, then write-aheads a `SIGNED` record. That is the
> ratified behavior: **pre-sign failures persist no record**, so their error envelope
> carries `paymentId: null` (a non-null id that `status` would 404 on is worse than
> none). `paymentId` is non-null in an error envelope ONLY when a durable record exists
> — i.e. an idempotency mismatch on an existing record, or a post-write-ahead fault.

**Execution order (normative; the first failure short-circuits).** No signature is produced before step 7.
1. Bearer auth → `401 UNAUTHENTICATED`.
2. Shape/type validation of `intent` + `approvalToken` → `400 BAD_REQUEST`.
3. **Durable idempotency lookup by `idempotencyNonce`** (§4.2): if a record exists and is `DELIVERED`/terminal, return it (`idempotentReplay:true`); if it exists but is unresolved (`SIGNED`/`SUBMITTED` after a crash) → `409 PAYMENT_IN_PROGRESS` (reconcile via `status`, never a `200` that reads as success); if an in-flight lock is held → `409 PAYMENT_IN_PROGRESS`. Never re-execute.
4. Approval HMAC verify (§3.4) → `401 APPROVAL_TOKEN_INVALID` (`paymentId: null`).
5. Recompute `intentHash(intent)` and compare to `approvalToken.intentHash` → `409 INTENT_HASH_MISMATCH` (**AT-05, hash defense**). Also enforce `approvalToken.decision == intent.decision`, `approvalToken.approvedAmount == intent.amount`, `approvalToken.expiresAt == intent.expiresAt`. (all `paymentId: null`)
6. Decision gate (Gate 1): `intent.decision ∈ {AUTO_APPROVE, HUMAN_APPROVED}` → else `403 INTENT_NOT_APPROVED`. Expiry: `now < intent.expiresAt` AND `intent.expiresAt` parses to a finite instant → else `410 APPROVAL_EXPIRED` (an unparseable timestamp fails closed).
7. **Probe the resource exactly once** to obtain the live `accept`. Then, **before signing**, assert against the approved intent (**AT-05, live-equality defense**):
   - `accept.network == intent.network` else `422 NO_MATCHING_NETWORK`
   - `accept.payTo == intent.merchant` else `409 MERCHANT_CHANGED`
   - `accept.asset == intent.asset` else `409 ASSET_CHANGED`
   - `accept.amount == intent.amount` else `409 PRICE_CHANGED`
   - `BigInt(accept.amount) <= BigInt(intent.maxAmount)` else `402 PRICE_EXCEEDS_RESERVED` (Gate 2; `details:{quoted,reserved}`)
8. **Write-ahead** a durable `SIGNED` record keyed by `idempotencyNonce` (with the precomputed `paymentId`, §4.1). From here the error envelope carries `paymentId`. Then sign the EIP-3009 authorization **against that exact validated `accept`** with the deterministic `authNonce` (§4.4). Do **not** re-probe.
9. Submit `X-PAYMENT`; on seller `200` → `DELIVERED`; on seller non-200 → `PAYMENT_REJECTED`; on transport failure after submit → `RECONCILING` (non-terminal, `FACILITATOR_ERROR`, `retriable:true`).

**Response `200`** (executed or idempotent replay)
```json
{
  "paymentId": "pay_9f3c…",
  "state": "DELIVERED",
  "idempotentReplay": false,
  "amountPaid": "40000",
  "receipt": {
    "resource": "http://127.0.0.1:4021/v1/company-profile", // sidecar echoes intent.resource (absolute); note seller-native receipt.resource is a PATH — see §6
    "amount": "40000", "asset": "0x…", "network": "eip155:5042002",
    "payer": "0x…", "payee": "0x…",           // payee == intent.merchant, verified in step 8
    "authNonce": "0x…",                        // the EIP-3009 authorization nonce (renamed from receipt.nonce)
    "settlement": "NOT_BROADCAST"              // dry run: "NOT_BROADCAST" | "SETTLED" | "FAILED"
  },
  "authorizationSignature": "0x…",             // EIP-712 signature, audit evidence (FR-X402-004), NOT a key
  "data": { },                                 // resource payload (opaque to Funding)
  "intentHash": "0x…",
  "executedAt": "2026-07-22T10:00:02Z"
}
```

**Errors** (evaluated in the step order above)

| HTTP | `code` | Rule |
|---|---|---|
| 401 | `UNAUTHENTICATED` | bad bearer token |
| 400 | `BAD_REQUEST` | malformed intent/token, non-atomic amount, bad hex |
| 409 | `PAYMENT_IN_PROGRESS` | same nonce executing elsewhere (§4.3); `retriable:true`, carries `paymentId` |
| 401 | `APPROVAL_TOKEN_INVALID` | approval HMAC verify failed |
| 409 | `INTENT_HASH_MISMATCH` | recomputed hash != token hash — **AT-05 hash defense**, pre-sign |
| 403 | `INTENT_NOT_APPROVED` | decision ∉ {AUTO_APPROVE, HUMAN_APPROVED} — Gate 1, pre-sign |
| 410 | `APPROVAL_EXPIRED` | `now >= intent.expiresAt` |
| 409 | `APPROVED_AMOUNT_MISMATCH` | `approvalToken.approvedAmount != intent.amount` |
| 422 | `NO_MATCHING_NETWORK` | live `accept.network != intent.network` |
| 409 | `MERCHANT_CHANGED` | live `accept.payTo != intent.merchant` — **AT-05 live defense**, pre-sign |
| 409 | `ASSET_CHANGED` | live `accept.asset != intent.asset` — pre-sign |
| 409 | `PRICE_CHANGED` | live `accept.amount != intent.amount` — **AT-05 live defense**, pre-sign |
| 402 | `PRICE_EXCEEDS_RESERVED` | live `accept.amount > intent.maxAmount` — Gate 2, pre-sign |
| 402 | `PAYMENT_REJECTED` | seller non-200 to `X-PAYMENT`; `details.sellerReason` = challenge `error` |
| 502 | `FACILITATOR_ERROR` | transport failure **after submit**; record → `RECONCILING` (non-terminal), `retriable:true` |
| 500 | `INTERNAL` | unexpected sidecar fault |

**Release safety (see §7):** every code **above** `FACILITATOR_ERROR` is a pre-sign or pre-submit failure — nothing settled, so Funding releases the full reservation immediately. `FACILITATOR_ERROR` is post-submit: the signed authorization may still settle, so the record goes to `RECONCILING` and Funding MUST reconcile via `status` before releasing.

### 2.3 `status` — read a payment's state
- **`GET /v1/status/{paymentId}`**

```json
{
  "paymentId": "pay_9f3c…",
  "state": "DELIVERED",
  "terminal": false,
  "intentHash": "0x…",
  "idempotencyNonce": "0x…",          // the intent nonce (renamed from status.nonce)
  "amountReserved": "40000",          // == intent.maxAmount
  "amountPaid": "40000",              // present once SIGNED+, else null
  "receipt": { /* as §2.2, or null before DELIVERED */ },
  "reason": "human string | null",    // set on FAILED/EXPIRED (last error code + message)
  "createdAt": "2026-07-22T10:00:00Z",
  "updatedAt": "2026-07-22T10:00:02Z"
}
```

| HTTP | `code` | Cause |
|---|---|---|
| 401 | `UNAUTHENTICATED` | bad bearer token |
| 404 | `PAYMENT_NOT_FOUND` | no record for `paymentId` |

`status` is safe to poll. **Dry-run reality:** with the current `settlement: "NOT_BROADCAST"` seller, `DELIVERED` is the committed-final success state (§5, §7). `SETTLED` and any `DELIVERED → SETTLED` / `RECONCILING → SETTLED|FAILED` facilitator reconciliation are **UNIMPLEMENTED** until a real Circle facilitator broadcast is wired. Consumers MUST NOT treat `SETTLED` as reachable in the dry run.

### 2.4 Stable error-code enum (frozen)
`UNAUTHENTICATED`, `BAD_REQUEST`, `RESOURCE_NOT_FOUND`, `NO_MATCHING_NETWORK`, `CHALLENGE_UNAVAILABLE`, `UPSTREAM_UNREACHABLE`, `PAYMENT_IN_PROGRESS`, `APPROVAL_TOKEN_INVALID`, `INTENT_HASH_MISMATCH`, `INTENT_NOT_APPROVED`, `APPROVAL_EXPIRED`, `APPROVED_AMOUNT_MISMATCH`, `MERCHANT_CHANGED`, `ASSET_CHANGED`, `PRICE_CHANGED`, `PRICE_EXCEEDS_RESERVED`, `PAYMENT_REJECTED`, `FACILITATOR_ERROR`, `PAYMENT_NOT_FOUND`, `INTERNAL`.

Additive-only after freeze: new codes may be introduced with a version bump; existing codes never change meaning.

---

## 3. `ApprovedIntent`, `approvalToken`, and hash binding (AT-05)

### 3.1 `ApprovedIntent` (wire form)
Superset of `buyer.ts::ApprovedIntent` with `bigint → string` and the quote-derived `merchant/asset/network/amount` added, so the owner approves the exact terms and `pay` can compare them to the live challenge.
```json
{
  "intentId":     "pi_9f3c1a2b",
  "jobId":        "job_104",
  "taskId":       "task_research_01",
  "agentId":      "market-researcher",
  "resource":     "http://127.0.0.1:4021/v1/company-profile",
  "network":      "eip155:5042002",
  "asset":        "0x…",            // USDC contract, from quote.accept.asset
  "merchant":     "0x…",            // payTo, from quote.accept.payTo
  "amount":       "40000",          // atomic USDC — the approved price
  "maxAmount":    "40000",          // atomic USDC — policy-reserved ceiling, >= amount (FR-PAY-006)
  "purpose":      "Competitor profile for job_104",
  "nonce":        "0x…",            // 32-byte hex idempotencyNonce (§4); unique per intent
  "decision":     "AUTO_APPROVE",   // 'AUTO_APPROVE'|'HUMAN_APPROVED'|'HUMAN_APPROVAL_REQUIRED'|'DENY'
  "policyVersion":"pol_7",
  "createdAt":    "2026-07-22T10:00:00Z",
  "expiresAt":    "2026-07-22T10:05:00Z"
}
```
`amount` is what the owner approved; `maxAmount` is the reserved ceiling. **Both are enforced pre-sign:** `PRICE_CHANGED` rejects any live `amount != intent.amount`, and `PRICE_EXCEEDS_RESERVED` rejects `> maxAmount`. Choosing `maxAmount == amount` for the demo eliminates the FR-PAY-006 overpay window entirely; a wider ceiling is only for cases where a small live increase is tolerable, and even then `PRICE_CHANGED` fires unless the policy re-approves.

### 3.2 `approvalToken` — the H2 approval-decision object
Gnanam's approval-decision POST shape, consumed unchanged by `pay`:
```json
{
  "intentHash":     "0x…",          // keccak256 over the canonical intent (§3.3)
  "decision":       "AUTO_APPROVE", // MUST equal intent.decision
  "approvedAmount": "40000",        // MUST equal intent.amount
  "approver":       "policy-engine",// or ownerId for HUMAN_APPROVED
  "policyVersion":  "pol_7",
  "issuedAt":       "2026-07-22T10:00:01Z",
  "expiresAt":      "2026-07-22T10:05:00Z", // MUST equal intent.expiresAt
  "signature":      "0x…"           // HMAC-SHA256, lowercase hex (§3.4)
}
```

### 3.3 What is hashed — `intentHash`
```
intentHash = "0x" + keccak256( utf8( canonicalJSON ) )
```
`canonicalJSON` = JSON of exactly these fields, keys sorted lexicographically, no whitespace, string values verbatim:
```
agentId, amount, asset, expiresAt, intentId, jobId, maxAmount,
merchant, network, nonce, policyVersion, purpose, resource, taskId
```
Concretely:
```
{"agentId":"market-researcher","amount":"40000","asset":"0x…","expiresAt":"2026-07-22T10:05:00Z","intentId":"pi_9f3c1a2b","jobId":"job_104","maxAmount":"40000","merchant":"0x…","network":"eip155:5042002","nonce":"0x…","policyVersion":"pol_7","purpose":"Competitor profile for job_104","resource":"http://127.0.0.1:4021/v1/company-profile","taskId":"task_research_01"}
```
`decision` and `createdAt` are excluded (decision is bound by equality; `createdAt` is not a term of the deal). `expiresAt` **is** hashed and is also equality-checked between intent and token.

**Canonicalization is exactly JS `JSON.stringify` byte-output (normative — cross-language landmine).** The reference is `JSON.stringify` over an object whose keys are inserted in the sorted order above. Two escaping details bite a Go implementation and MUST be matched:
- Go's `encoding/json` HTML-escapes `&`, `<`, `>` by default. Disable it: `enc.SetEscapeHTML(false)`. A `resource` query string (`?a=1&b=2`) otherwise diverges.
- Go **also** escapes U+2028 (LINE SEPARATOR) and U+2029 (PARAGRAPH SEPARATOR) even with HTML-escaping off; `JSON.stringify` emits them **literally**. A Go implementation MUST post-process ` `/` ` back to the literal characters. (Gnanam's Go side, `internal/approval/hash.go`, does exactly this, with a test.)

**Golden vectors (pin these byte-exact in a test on both sides).** Both sides must produce the identical `intentHash` for the same intent — a single agreeing case is not enough, so the second vector exercises the escaping path:

| # | intent | expected `intentHash` |
|---|---|---|
| GV-1 | the concrete example above (`purpose:"Competitor profile for job_104"`) | _fill from `JSON.stringify` + keccak256; Gnanam to cross-check against Go_ |
| GV-2 | GV-1 but `purpose:"line1 line2 end"` and `resource:"http://x/v1?a=1&b=2"` | _fill; this is the case that catches the HTML-escape and line-separator divergences_ |

> **AGENDA — H1/H2/H3 session (tonight):** finalize both golden vectors and commit a matching JS test in the sidecar and Go test on Gnanam's side. Without GV-2 the escaping bug ships silently: two implementations that agree on GV-1 can still diverge on any intent whose `purpose`/`resource` contains `&`, `<`, `>`, U+2028, or U+2029, producing an `INTENT_HASH_MISMATCH` that rejects a legitimate payment.

**AT-05 guarantee (two independent pre-sign defenses):**
1. `merchant/amount/maxAmount/asset/network/resource/nonce` are all inside the hash, so any post-approval change to the intent diverges the hash and `pay` rejects `INTENT_HASH_MISMATCH`.
2. Independently, `pay` compares the **live** `accept.payTo/amount/asset/network` to the approved intent (§2.2 step 8) and signs against that validated object, so a re-quoting or compromised seller that leaves the intent untouched is still caught by `MERCHANT_CHANGED` / `PRICE_CHANGED` / `ASSET_CHANGED` / `PRICE_EXCEEDS_RESERVED`. Without this step the hash would bind fields the signed transfer never consumes — cosmetic binding. This step is what makes the binding real.

### 3.4 Approval signature
```
approvalToken.signature = HMAC-SHA256(
  key = H2_APPROVAL_SECRET,
  msg = intentHash + "|" + decision + "|" + approvedAmount + "|" + expiresAt
)   // lowercase hex
```
`H2_APPROVAL_SECRET` is shared between Gnanam's approval service and the sidecar (new env var, §9). Verified constant-time; failure gives `APPROVAL_TOKEN_INVALID`. Output MUST be **lowercase hex** (constant-time compare is byte-sensitive). If H2 later signs with an owner keypair this becomes ECDSA verify, which is a version bump.

---

## 4. Idempotency

### 4.1 `paymentId` (one canonical preimage — pin this)
```
paymentId = "pay_" + keccak256( utf8( intentHash + "|" + idempotencyNonce ) ).slice(2, 18)
```
Inputs are the `0x`-prefixed lowercase hex strings joined by a literal `"|"`; `.slice(2,18)` takes 16 hex chars after the `0x`. It is fully determined by the intent, so **Funding can precompute `paymentId` at reserve time** and always poll `status`, even if a `pay` response is lost to a timeout.

### 4.2 Replay (no double-pay)
The sidecar keeps a **durable** record keyed by `idempotencyNonce`, write-ahead as `SIGNED` immediately before signing (§2.2 step 8). The record file is written atomically (temp + rename) and load fails closed on corruption, so the record cannot silently vanish under a crash mid-write.
- **First call:** all pre-sign checks pass, write-ahead `SIGNED`, sign+submit, advance to `DELIVERED`, return `idempotentReplay:false`.
- **Later call, same nonce + matching `intentHash`, record `DELIVERED`/terminal:** return the **stored** record, `idempotentReplay:true`, HTTP `200`, **without re-calling execution and without contacting the seller**.
- **Later call, same nonce, record UNRESOLVED (`SIGNED`/`SUBMITTED` after a crash):** `409 PAYMENT_IN_PROGRESS` (`retriable:true`) — never a `200` that reads as success with `amountPaid:null`. Reconcile via `status`.
- **Same nonce, different `intentHash`:** `409 INTENT_HASH_MISMATCH` (a nonce may not be reused for different terms).

Because the record is durable and written before signing, a crash between signing and the response cannot cause a second execution: the retry finds the record and reconciles instead of re-signing.

### 4.3 Concurrency
A per-`idempotencyNonce` lock guards execution. A concurrent second request for the same nonce returns `409 PAYMENT_IN_PROGRESS` (`retriable:true`, carries `paymentId`); it MUST NOT start a parallel execution. Funding retries `pay` or polls `status`; the retry hits the stored record.

### 4.4 Deterministic `authNonce` (backstop)
The EIP-3009 authorization nonce is derived, not random:
```
authNonce = keccak256( utf8( intentHash + "|auth" ) )   // 32-byte hex
```
So even if the durable record were lost and execution somehow repeated, the **same** on-chain nonce is produced, and `seller.ts::spentNonces` rejects it as a replay. This makes the seller-side guard an actual last line of defense rather than a claim. It requires `signAuthorization` to accept a supplied nonce instead of calling `freshNonce()` (§6).

---

## 5. Status state machine

```
 (RECEIVED) ─▶ (APPROVED) ─▶ SIGNED ─▶ SUBMITTED ─▶ DELIVERED ─▶ (SETTLED*)
                                │           │            │
                                └── FAILED  └── FAILED   └── RECONCILING* ─▶ SETTLED* | FAILED*
```
`*` = requires a real facilitator; UNIMPLEMENTED in the dry run.

### 5.1 Meanings
> **Reconciled (review):** `RECEIVED`/`APPROVED` are **conceptual only — never persisted**. Validation happens in memory and persists nothing (pre-sign failures leave no record, §2.2). The **first persisted state is `SIGNED`**, write-ahead immediately before signing. `(parenthesized)` states above are logical stages, not stored values.
- **RECEIVED / APPROVED** — logical pre-sign stages; not written to the store. A failure here returns an error with `paymentId: null`.
- **SIGNED** — EIP-3009 authorization signed once (deterministic `authNonce`). A key has been used exactly once. **First persisted state.**
- **SIGNED** — EIP-3009 authorization signed once (deterministic `authNonce`). A key has been used exactly once.
- **SUBMITTED** — `X-PAYMENT` sent; awaiting seller/facilitator response.
- **DELIVERED** — seller returned `200` with `{data, receipt}`; goods in hand; `settlement == "NOT_BROADCAST"`. **In the dry run this is the committed-final success state** (see §7).
- **RECONCILING** — a transport failure occurred **after** submit; the authorization may or may not have settled. Non-terminal; requires facilitator reconciliation before any budget release. (Unreachable until a real facilitator is wired; in the pure dry run a post-submit failure is impossible because the seller call is local.)
- **SETTLED** — facilitator confirms on-chain settlement. Terminal success. Unimplemented in the dry run.
- **FAILED** — pre-sign/pre-submit failure, or a reconciled post-submit failure. Terminal. `reason` set.
- **EXPIRED** — approval window elapsed before completion. Terminal. `reason` set.

### 5.2 Legal transitions
```
RECEIVED    → APPROVED | FAILED
APPROVED    → SIGNED | FAILED | EXPIRED
SIGNED      → SUBMITTED | FAILED
SUBMITTED   → DELIVERED | RECONCILING | FAILED
DELIVERED   → SETTLED | FAILED            (facilitator only; dry run stops here)
RECONCILING → SETTLED | FAILED            (facilitator only)
Terminal    : SETTLED, FAILED, EXPIRED
```
Transitions are monotonic; `status` never moves backward. `terminal:true` iff state ∈ {SETTLED, FAILED, EXPIRED}.

---

## 6. Mapping onto `buyer.ts` / x402 — what is reused vs. what must change

**The review's key correction:** `pay` **cannot** call `buyer.ts::purchase()` as-is. `purchase()` probes the resource and signs in one call and performs **only** Gate 1 (decision) and Gate 2 (`price > maxAmount`). It never compares the live `accept.payTo/amount/asset` to the approved intent, so a naive wrapper would sign funds to a substituted payee (AT-05 hole). And a wrapper that validates with its own probe and then lets `purchase()` re-probe reintroduces a TOCTOU window. So `pay` owns the merchant/price/asset equality checks, and the probe-then-sign must be a **single** probe.

**Reused unchanged (all of x402.ts; the seller):**

| Concern | Reused |
|---|---|
| Challenge parse + transport precedence (`payment-required` header, then body) | `x402.ts::readChallenge`, `PaymentChallenge`, `AcceptOption` |
| Base64 wire codec | `x402.ts::encodeBase64Json` / `decodeBase64Json` |
| Amount formatting | `x402.ts::formatUsdc` |
| EIP-712 types | `x402.ts::TRANSFER_WITH_AUTHORIZATION_TYPES`, `Authorization`, `PaymentPayload` |
| Seller-side verify + replay guard | `seller.ts::verifyTypedData`, `spentNonces` — untouched |
| Signer load | `buyer.ts::loadSigner()` (`TREASURY_PRIVATE_KEY`) |

**Bounded `buyer.ts` refactor required (security-critical — must be reviewed to preserve the never-sign-before-gates ordering):**

1. **Split `purchase()`** into `probeChallenge(resource, chainId) → accept` and `signAndSubmit(accept, intent, account, authNonce) → PurchaseResult`. `pay` calls `probeChallenge` once, runs the §2.2 step-8 equality checks on that `accept`, then calls `signAndSubmit` with **that same object**. No second probe.
2. **Add the equality checks** (`payTo==merchant`, `amount==intent.amount`, `asset==intent.asset`, `network==intent.network`) alongside the existing decision + `price>maxAmount` gates. Existing gates stay unchanged.
3. **Accept a supplied `authNonce`** in `signAuthorization` instead of always calling `freshNonce()` (for the deterministic nonce, §4.4). `freshNonce()` remains the default for any non-H3 caller.
4. **Typed error codes.** `purchase()` currently throws one untyped `PaymentFailed` for four distinct outcomes (non-402 probe, null challenge, no-network, seller non-200) and lets `fetch` throw raw `TypeError` for transport faults. Give these stable codes (subclasses or a `code` field) so `pay` can map to §2.4 without string-matching prose.

**Preserved demo-loop behavior + one new test.** The existing `demo-loop.ts` assertions (AT-02 auto-approve, AT-03 over-threshold refused pre-sign, AT-04 cheaper alternative, replay rejected, AT-05 `price>maxAmount`, spend total 0.10) must still pass through the sidecar. **Add a merchant-swap test:** a seller that keeps `amount <= maxAmount` but returns a different `payTo` must be rejected `MERCHANT_CHANGED` before signing. The current AT-05 test only exercises the `price>maxAmount` path, so this hole is presently untested.

**Circle-facilitator-only** is discussed honestly in §8.

---

## 7. Reserve / reconcile / release — Funding (V6) lifecycle

```
1. QUOTE      quote(resource) → price P, payTo M.
              Funding builds ApprovedIntent{ amount=P, maxAmount>=P (P for the demo),
              merchant=M, asset, network, nonce=fresh idempotencyNonce },
              precomputes paymentId (§4.1), sends the intent to H2.

2. RESERVE    On decision AUTO_APPROVE / HUMAN_APPROVED, hold intent.maxAmount
   (pre-sign) against the per-job budget, then call pay(intent, approvalToken).
                • 4xx BEFORE FACILITATOR_ERROR (hash/decision/expiry/merchant/
                  price/asset/exceeds) → nothing signed → RELEASE the full
                  reserved maxAmount immediately.
                • 200 DELIVERED → COMMIT (step 3).
                • 502 FACILITATOR_ERROR (post-submit) → do NOT release →
                  RECONCILE (step 4).

3. COMMIT     On DELIVERED (dry-run committed-final): convert the hold to a spend
   (on         at receipt.amount (amountPaid); release the remainder
    DELIVERED)  (maxAmount − amountPaid). Do not wait for SETTLED — it never arrives
              in the dry run.

4. RECONCILE  On RECONCILING, poll status(paymentId) until terminal:
   (post-        • SETTLED  → commit at amountPaid, release remainder.
    submit)      • FAILED   → the authorization did not settle → release full maxAmount.
              Never release-then-remint on a bare FACILITATOR_ERROR: the signed
              authorization is a bearer instrument and may still settle, so releasing
              early and re-minting a fresh intent risks a double-spend of the budget.

5. RETRY      Any re-evaluation after MERCHANT_CHANGED / PRICE_CHANGED / ASSET_CHANGED /
              PRICE_EXCEEDS_RESERVED / INTENT_HASH_MISMATCH requires a FRESH
              idempotencyNonce and a NEW approvalToken. The old nonce is dead
              (it is a hashed term; reusing it gives INTENT_HASH_MISMATCH).

PAYMENT_IN_PROGRESS (409): do NOT release; retry pay or poll status — the reservation
still backs an in-flight payment. The envelope carries paymentId (§2.0) so status is
always reachable.
```
This is exactly the AT-03 (`INTENT_NOT_APPROVED`, immediate full release, no signature) and AT-05 (`MERCHANT_CHANGED` / `PRICE_CHANGED` / `PRICE_EXCEEDS_RESERVED`, immediate full release) behavior the demo asserts, expressed as an explicit reserve → release path.

**AT-03 branch (decide with Gnanam):** Funding only reaches step 2 on an approved decision, so in normal flow it never sends a `HUMAN_APPROVAL_REQUIRED`/`DENY` intent to `pay`. The `INTENT_NOT_APPROVED` gate is a defense-in-depth check for a mis-wired caller. The demo can assert it directly by calling `pay` with a non-approved intent in a test. Chosen default: **Funding short-circuits before `pay` on non-approved decisions; the gate exists as belt-and-suspenders and is covered by a direct unit test, not the live spine.**

---

## 8. Circle-facilitator-only — the honest boundary

A signed EIP-3009 `transferWithAuthorization` is a bearer instrument: once `pay` hands the `X-PAYMENT` to the resource/seller host, that host can submit it to any facilitator/relayer. H3 at the buyer layer **cannot** constrain which facilitator settles. Two consequences the spec states plainly rather than papering over:

1. **"Circle-facilitator-only" is enforced by the OUTER guard**, not H3: Circle's Agent-Wallet spend policy (payee/amount allowlist at the wallet layer) is what actually binds settlement to the approved merchant and Circle's rails. H3's contribution is the pre-sign payee-equality check (§2.2 step 8), which ensures the treasury only ever signs an authorization payable to the approved `intent.merchant`. Fixing the AT-05 payee hole is therefore a **prerequisite** for this invariant to mean anything.
2. **Facilitator reconciliation is unimplemented in the dry run.** `DELIVERED → SETTLED` and `RECONCILING → SETTLED|FAILED` require a real broadcast; until it is wired, `settlement` stays `NOT_BROADCAST` and `SETTLED` is unreachable. §2.3/§5 mark this; consumers must not treat `SETTLED` as reachable yet.

This matches the two-layer authorization model in §0: inner guard (H3 + policy + approval) proposes and signs only approved terms; outer guard (Agent-Wallet policy) is the settlement-route and payee enforcer.

---

## 9. Env additions (append to `sidecar/.env.example`)
```
SIDECAR_AUTH_TOKEN=      # >=32-byte secret; Funding + sidecar only. Never commit.
H2_APPROVAL_SECRET=      # shared with Gnanam's approval service; HMAC key for approvalToken. Never commit.
# existing, reused: SIDECAR_PORT=4020, PAID_API_PORT=4021, TREASURY_PRIVATE_KEY,
#                   ARC_TESTNET_RPC, ARC_USDC_ADDRESS (verifyingContract), and the seller pay-to address
```

---

## 10. Open decisions to ratify at the handshake session

1. **Who owns the `buyer.ts` refactor (§6)?** It is security-critical and touches V1's file. Proposal: Vasanth owns it since it is inside the sidecar; Anandan/Gnanam review the never-sign-before-gates ordering. It must land before the H3 wrapper (V4) can be correct.
2. **`approvalToken` shape (§3.2) is H2 (Gnanam).** Confirm: `intentHash` construction (§3.3) byte-for-byte, lowercase-hex HMAC (§3.4), and the three equality requirements (`decision`, `approvedAmount`, `expiresAt`).
3. **`maxAmount` policy (§3.1).** Confirm the demo uses `maxAmount == amount` so there is no overpay window; document the rule for choosing a wider ceiling if any pipeline needs it.
4. **AT-03 branch (§7).** Confirm Funding short-circuits before `pay` on non-approved decisions and the gate is unit-tested rather than exercised live.
5. **Deterministic `authNonce` (§4.4).** Confirm this is acceptable (it changes `signAuthorization`); it is what lets the seller replay guard survive a sidecar restart.

---

*H3 v1.0-rc. Every field, state, and error code above is normative once ratified. Freeze Fri 24 Jul 2026.*
