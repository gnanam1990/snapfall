/**
 * Snapfall H3 payment sidecar — the loopback HTTP service the Funding agent (V6) calls.
 *
 * Wraps the x402 buyer behind three authenticated operations:
 *   POST /v1/quote            price a resource (read-only, no key)
 *   POST /v1/pay              execute an owner-approved intent (idempotent by intent nonce)
 *   GET  /v1/status/{id}      read a payment's state
 *
 * Contract: docs/handshakes/H3-sidecar-api.md. The security-critical bits, per the review:
 *   - bind loopback only; bearer-token auth; only the Funding process holds the token.
 *   - `pay` verifies the approval HMAC, recomputes the intent hash, and — via buyer.ts —
 *     probes ONCE and asserts the live challenge matches the approved terms before signing.
 *   - idempotency is a DURABLE record keyed by the intent nonce, written BEFORE signing, so a
 *     crash cannot cause a second execution; the on-chain authNonce is deterministic too.
 *
 * SETTLEMENT: inherited dry run — the seller verifies but does not broadcast, so a successful
 * pay rests at DELIVERED (committed-final here). SETTLED needs a real facilitator (see H3 §8).
 */

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { Hex, LocalAccount } from 'viem';
import {
  loadSigner,
  probeChallenge,
  assertAcceptMatchesIntent,
  signAndSubmit,
  PolicyViolation,
  PaymentFailed,
  provenNotSent,
  type ApprovedIntent,
} from './buyer.js';
import { formatUsdc } from './x402.js';
import {
  computeIntentHash,
  computePaymentId,
  computeAuthNonce,
  verifyApproval,
  constantTimeEquals,
  type WireIntent,
  type ApprovalToken,
} from './h3.js';
import { PaymentStore, type PaymentRecord, type PaymentState } from './store.js';

const PORT = Number(process.env.SIDECAR_PORT ?? 4020);
const AUTH_TOKEN = process.env.SIDECAR_AUTH_TOKEN ?? '';
const H2_SECRET = process.env.H2_APPROVAL_SECRET ?? '';
const STORE_PATH = process.env.SIDECAR_STORE_PATH ?? '.data/payments.json';

if (!AUTH_TOKEN) throw new Error('SIDECAR_AUTH_TOKEN is not set (>=32-byte secret; see sidecar/.env.example).');
// The spec (§1.2) requires a >=32-byte secret; enforce it rather than only checking
// non-empty, so a weak token fails at boot instead of protecting `pay` in name only.
if (Buffer.byteLength(AUTH_TOKEN, 'utf8') < 32) {
  throw new Error('SIDECAR_AUTH_TOKEN must be at least 32 bytes (§1.2); refusing to start with a weak bearer secret.');
}
if (!H2_SECRET) throw new Error('H2_APPROVAL_SECRET is not set (shared with the approval service).');

const signer: LocalAccount = await loadSigner();
const store = new PaymentStore(STORE_PATH);
/** In-memory per-nonce lock: prevents two concurrent `pay` calls executing the same intent. */
const inFlight = new Set<string>();
/**
 * The paymentIds of those same in-flight executions, so `status` can see them.
 *
 * Between accepting a pay and its durable write-ahead record there is an await on the seller's
 * 402 probe. Without this, `status` answered PAYMENT_NOT_FOUND for that whole window, and the
 * daemon reads PAYMENT_NOT_FOUND as "nothing was or will be signed" and releases the hold -- while
 * this process was about to sign. In memory is enough: if the process dies, the pending execution
 * dies with it before signing, and PAYMENT_NOT_FOUND becomes true.
 */
const inFlightIds = new Set<string>();

const TERMINAL: ReadonlySet<PaymentState> = new Set(['SETTLED', 'FAILED', 'EXPIRED']);

interface H3Receipt {
  resource: string;
  amount: string;
  asset: string;
  network: string;
  payer: string;
  payee: string;
  authNonce: string;
  settlement: string;
}

// ── error envelope ──────────────────────────────────────────────────────────

interface Reply {
  status: number;
  json: unknown;
}

function errorReply(
  status: number,
  code: string,
  message: string,
  paymentId: string | null = null,
  extra: { retriable?: boolean; details?: unknown } = {},
): Reply {
  return {
    status,
    json: {
      error: {
        code,
        message,
        paymentId,
        retriable: extra.retriable ?? false,
        ...(extra.details !== undefined ? { details: extra.details } : {}),
      },
    },
  };
}

const POLICY_STATUS: Record<string, number> = {
  INTENT_NOT_APPROVED: 403,
  NETWORK_MISMATCH: 422,
  MERCHANT_CHANGED: 409,
  ASSET_CHANGED: 409,
  PRICE_CHANGED: 409,
  PRICE_EXCEEDS_RESERVED: 402,
};
const POLICY_CODE_RENAME: Record<string, string> = { NETWORK_MISMATCH: 'NO_MATCHING_NETWORK' };

const PAYMENT_STATUS: Record<string, number> = {
  RESOURCE_NOT_FOUND: 404,
  CHALLENGE_UNAVAILABLE: 502,
  NO_MATCHING_NETWORK: 422,
  PAYMENT_REJECTED: 402,
  UPSTREAM_UNREACHABLE: 502,
  FACILITATOR_ERROR: 502,
};
const RETRIABLE_PAYMENT = new Set(['CHALLENGE_UNAVAILABLE', 'UPSTREAM_UNREACHABLE', 'FACILITATOR_ERROR']);

/** Map a buyer.ts error onto the H3 status + error code. */
function mapBuyerError(e: unknown, paymentId: string | null): Reply {
  if (e instanceof PolicyViolation) {
    const code = POLICY_CODE_RENAME[e.code] ?? e.code;
    return errorReply(POLICY_STATUS[e.code] ?? 409, code, e.message, paymentId);
  }
  if (e instanceof PaymentFailed) {
    return errorReply(PAYMENT_STATUS[e.code] ?? 502, e.code, e.message, paymentId, {
      retriable: RETRIABLE_PAYMENT.has(e.code),
    });
  }
  return errorReply(500, 'INTERNAL', (e as Error).message, paymentId);
}

// ── helpers ─────────────────────────────────────────────────────────────────

function toBuyerIntent(w: WireIntent): ApprovedIntent {
  return {
    intentId: w.intentId,
    jobId: w.jobId,
    taskId: w.taskId,
    agentId: w.agentId,
    resource: w.resource,
    maxAmount: BigInt(w.maxAmount),
    decision: w.decision,
    policyVersion: w.policyVersion,
    merchant: w.merchant,
    amount: BigInt(w.amount),
    asset: w.asset,
    network: w.network,
  };
}

function chainIdOf(network: string): number {
  return Number(network.split(':')[1] ?? '0');
}

function isWireIntent(x: unknown): x is WireIntent {
  const s = ['intentId', 'jobId', 'taskId', 'agentId', 'resource', 'network', 'asset', 'merchant', 'amount', 'maxAmount', 'purpose', 'nonce', 'decision', 'policyVersion', 'createdAt', 'expiresAt'];
  return !!x && typeof x === 'object' && s.every((k) => typeof (x as Record<string, unknown>)[k] === 'string');
}
function isApprovalToken(x: unknown): x is ApprovalToken {
  const s = ['intentHash', 'decision', 'approvedAmount', 'approver', 'policyVersion', 'issuedAt', 'expiresAt', 'signature'];
  return !!x && typeof x === 'object' && s.every((k) => typeof (x as Record<string, unknown>)[k] === 'string');
}

/** A canonical atomic-USDC amount: a base-10 non-negative integer string with no sign,
 *  decimal, leading zeros (except "0"), or `0x`. Anything else must not reach BigInt(). */
function isCanonicalAtomic(v: unknown): v is string {
  return typeof v === 'string' && /^(0|[1-9][0-9]*)$/.test(v);
}

function h3Receipt(w: WireIntent, r: { receipt: { amount: string; payer: string; payee: string; nonce: string; settlement: string } }): H3Receipt {
  return {
    resource: w.resource,
    amount: r.receipt.amount,
    asset: w.asset,
    network: w.network,
    payer: r.receipt.payer,
    payee: r.receipt.payee,
    authNonce: r.receipt.nonce,
    settlement: r.receipt.settlement,
  };
}

function payResponse(rec: PaymentRecord, idempotentReplay: boolean): unknown {
  return {
    paymentId: rec.paymentId,
    state: rec.state,
    idempotentReplay,
    amountPaid: rec.amountPaid,
    receipt: rec.receipt,
    authorizationSignature: rec.authorizationSignature,
    data: rec.data,
    intentHash: rec.intentHash,
    executedAt: rec.updatedAt,
  };
}

function statusResponse(rec: PaymentRecord): unknown {
  return {
    paymentId: rec.paymentId,
    state: rec.state,
    terminal: TERMINAL.has(rec.state),
    intentHash: rec.intentHash,
    idempotencyNonce: rec.idempotencyNonce,
    amountReserved: rec.amountReserved,
    amountPaid: rec.amountPaid,
    receipt: rec.receipt,
    reason: rec.reason,
    createdAt: rec.createdAt,
    updatedAt: rec.updatedAt,
  };
}

// ── handlers ────────────────────────────────────────────────────────────────

async function handleQuote(body: unknown): Promise<Reply> {
  const b = body as { resource?: unknown; chainId?: unknown };
  if (typeof b?.resource !== 'string' || typeof b?.chainId !== 'number') {
    return errorReply(400, 'BAD_REQUEST', 'quote requires { resource: string, chainId: number }');
  }
  // A malformed URL is a client error (400), not an upstream fault (502) — validate it
  // here before probeChallenge turns a bad URL into UPSTREAM_UNREACHABLE (spec §2.1).
  let parsed: URL;
  try {
    parsed = new URL(b.resource);
  } catch {
    return errorReply(400, 'BAD_REQUEST', `resource is not a valid URL: ${b.resource}`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return errorReply(400, 'BAD_REQUEST', `resource must be http(s), got ${parsed.protocol}`);
  }
  let accept;
  try {
    accept = await probeChallenge(b.resource, b.chainId);
  } catch (e) {
    return mapBuyerError(e, null);
  }
  const now = Date.now();
  return {
    status: 200,
    json: {
      resource: b.resource,
      network: accept.network,
      accept,
      price: accept.amount,
      priceDisplay: formatUsdc(accept.amount),
      quotedAt: new Date(now).toISOString(),
      quoteExpiresAt: new Date(now + accept.maxTimeoutSeconds * 1000).toISOString(),
    },
  };
}

async function handlePay(body: unknown): Promise<Reply> {
  const b = body as { intent?: unknown; approvalToken?: unknown };
  if (!isWireIntent(b?.intent) || !isApprovalToken(b?.approvalToken)) {
    return errorReply(400, 'BAD_REQUEST', 'pay requires { intent, approvalToken } with all string fields');
  }
  const intent = b.intent;
  const token = b.approvalToken;

  // Semantic validation of the atomic-amount fields BEFORE any BigInt() conversion:
  // isWireIntent is type-only, so a non-canonical amount ("4.0", "0x5", "") would reach
  // BigInt() and surface as a 500 instead of the documented 400 (review fix).
  for (const [field, value] of [
    ['intent.amount', intent.amount],
    ['intent.maxAmount', intent.maxAmount],
    ['approvalToken.approvedAmount', token.approvedAmount],
  ] as const) {
    if (!isCanonicalAtomic(value)) {
      return errorReply(400, 'BAD_REQUEST', `${field} is not a canonical atomic USDC string: ${JSON.stringify(value)}`);
    }
  }

  const intentHash = computeIntentHash(intent);
  const paymentId = computePaymentId(intentHash, intent.nonce);

  // ── Idempotency: a durable record for this nonce means we already executed (or tried). ──
  const existing = store.getByNonce(intent.nonce);
  if (existing) {
    if (existing.intentHash !== intentHash) {
      return errorReply(409, 'INTENT_HASH_MISMATCH', 'nonce reused for different terms', existing.paymentId);
    }
    // A COMPLETED payment replays as the stored 200. But a record stuck mid-flight
    // (SIGNED/SUBMITTED after a crash between the write-ahead and completion) must NOT
    // return a 200 that reads as success with amountPaid:null — that would tell Funding
    // the payment is done when its outcome is unknown. Report it as in-progress and
    // point the caller at `status` to reconcile (review fix).
    if (existing.state === 'DELIVERED' || TERMINAL.has(existing.state)) {
      return { status: 200, json: payResponse(existing, true) };
    }
    return errorReply(
      409,
      'PAYMENT_IN_PROGRESS',
      `a prior execution for this nonce is unresolved (state ${existing.state}); reconcile via status before retrying`,
      existing.paymentId,
      { retriable: true },
    );
  }
  if (inFlight.has(intent.nonce)) {
    // No durable record yet (the holder is still pre-write-ahead), so carry null, consistent
    // with the envelope rule below. `status` on the precomputed paymentId answers the same
    // PAYMENT_IN_PROGRESS for this window (inFlightIds), never PAYMENT_NOT_FOUND.
    return errorReply(409, 'PAYMENT_IN_PROGRESS', 'an execution for this nonce is in flight', null, { retriable: true });
  }

  // Registered BEFORE the first await, so from here until the durable record exists `status`
  // reports this paymentId as in progress, never as not found.
  inFlight.add(intent.nonce);
  inFlightIds.add(paymentId);
  try {
    // ── Pre-sign checks persist NO record (H3 §2.2), so the error envelope carries a
    //    NULL paymentId: `status` would 404 on the deterministic id, and a non-null id
    //    that does not resolve is worse than none. The envelope's paymentId is non-null
    //    ONLY once a durable record exists (review fix — closes the spec/code divergence).
    //    A failure here means nothing was signed; Funding releases its reservation. ──
    // AUTHENTICATE the token before trusting any of its fields (review fix — the token's
    // HMAC must verify before we act on its claimed intentHash/decision/amount). This is
    // the order the spec §2.2 now documents.
    if (!verifyApproval(H2_SECRET, token)) {
      return errorReply(401, 'APPROVAL_TOKEN_INVALID', 'approval signature failed verification', null);
    }
    if (token.intentHash !== intentHash) {
      return errorReply(409, 'INTENT_HASH_MISMATCH', 'approval hash does not match the intent (AT-05)', null);
    }
    // decision/expiresAt divergence between token and intent means the token authorizes a
    // DIFFERENT deal than the one presented → APPROVAL_TOKEN_INVALID (§2.4; the team may
    // split these into DECISION_MISMATCH/EXPIRES_AT_MISMATCH at the H3 session).
    if (token.decision !== intent.decision || token.expiresAt !== intent.expiresAt) {
      return errorReply(401, 'APPROVAL_TOKEN_INVALID', 'approval decision/expiry does not match the intent', null);
    }
    if (token.approvedAmount !== intent.amount) {
      return errorReply(409, 'APPROVED_AMOUNT_MISMATCH', 'approved amount does not match the intent amount', null);
    }
    // NaN-safe expiry: Date.parse of a malformed timestamp is NaN, and `NaN <= now` is
    // false, so an unparseable expiresAt would never expire. Fail closed (review fix).
    const expiresAtMs = Date.parse(intent.expiresAt);
    if (!Number.isFinite(expiresAtMs) || expiresAtMs <= Date.now()) {
      return errorReply(410, 'APPROVAL_EXPIRED', 'the approval window has elapsed or its timestamp is unparseable', null);
    }
    if (intent.decision !== 'AUTO_APPROVE' && intent.decision !== 'HUMAN_APPROVED') {
      return errorReply(403, 'INTENT_NOT_APPROVED', `intent is ${intent.decision}; the treasury signs only approved intents`, null);
    }

    const buyerIntent = toBuyerIntent(intent);
    const chainId = chainIdOf(intent.network);

    // Probe ONCE, and assert the live challenge matches the approved terms — before signing.
    let accept;
    try {
      accept = await probeChallenge(intent.resource, chainId);
      assertAcceptMatchesIntent(accept, buyerIntent);
    } catch (e) {
      return mapBuyerError(e, null); // pre-sign: still no record persisted
    }

    // The probe awaited a seller-timed response, so the approval checked above may have lapsed
    // while it ran. Re-check immediately before the write-ahead and the signature: nothing is
    // awaited between this line and signAndSubmit starting, so the approval is live when the key
    // is used. Still pre-sign, so still no record and a null paymentId.
    if (Date.parse(intent.expiresAt) <= Date.now()) {
      return errorReply(410, 'APPROVAL_EXPIRED', 'the approval window elapsed while the seller challenge was being fetched; nothing was signed', null);
    }

    // ── Write-ahead the durable record, THEN sign the same validated accept. A crash after
    //    this point recovers via the record + deterministic authNonce, never a second pay. ──
    const nowIso = new Date().toISOString();
    let rec: PaymentRecord = {
      paymentId,
      idempotencyNonce: intent.nonce,
      intentHash,
      state: 'SIGNED',
      amountReserved: intent.maxAmount,
      amountPaid: null,
      receipt: null,
      data: null,
      authorizationSignature: null,
      reason: null,
      createdAt: nowIso,
      updatedAt: nowIso,
    };
    store.upsert(rec);

    try {
      const result = await signAndSubmit(accept, buyerIntent, signer, chainId, computeAuthNonce(intentHash));
      rec = {
        ...rec,
        state: 'DELIVERED',
        amountPaid: result.amountPaid.toString(),
        receipt: h3Receipt(intent, result),
        data: result.data,
        authorizationSignature: result.authorizationSignature,
        updatedAt: new Date().toISOString(),
      };
      store.upsert(rec);
      return { status: 200, json: payResponse(rec, false) };
    } catch (e) {
      // Past the write-ahead. The question is whether the signed authorization may still settle,
      // because the daemon reads FAILED as "nothing settled, release" (h3.Unresolved, h3.go) and
      // RECONCILING as "unknown, the hold stands".
      //
      // FAILED is reserved for the one PROVEN non-settlement: the signature was never produced,
      // so nothing left this process (buyer.provenNotSent). Everything else is RECONCILING. Once
      // X-PAYMENT is sent the seller holds a bearer instrument, and no shape of its reply proves
      // otherwise: a rejection, a 200 with no receipt, a null body, a v2 PAYMENT-RESPONSE header,
      // a parse error, or a bug in our own handling of the reply. This used to be decided by
      // isPostSign, a positive list of two PaymentFailed codes, so every OTHER exception after
      // the send -- a TypeError on a receipt-less 200, say -- became FAILED and released the
      // budget behind a delivered authorization.
      const unsent = provenNotSent(e);
      store.upsert({ ...rec, state: unsent ? 'FAILED' : 'RECONCILING', reason: (e as Error).message, updatedAt: new Date().toISOString() });
      if (unsent || e instanceof PaymentFailed) return mapBuyerError(e, paymentId);
      // An unclassified throw after the send must still read as post-sign on the wire, not as the
      // ambiguous INTERNAL.
      return errorReply(502, 'FACILITATOR_ERROR', `outcome unknown after the authorization was submitted: ${(e as Error).message}`, paymentId, {
        retriable: true,
      });
    }
  } finally {
    inFlight.delete(intent.nonce);
    inFlightIds.delete(paymentId);
  }
}

function handleStatus(paymentId: string): Reply {
  const rec = store.getById(paymentId);
  if (rec) return { status: 200, json: statusResponse(rec) };
  if (inFlightIds.has(paymentId)) {
    // Accepted but not yet written ahead (the seller probe is still running). NOT "not found":
    // the daemon releases its hold on PAYMENT_NOT_FOUND, and this execution may yet sign. Same
    // code and envelope rule as the in-flight branch of `pay` (null paymentId until a durable
    // record exists); the daemon keeps the hold on it and polls again.
    return errorReply(409, 'PAYMENT_IN_PROGRESS', `payment ${paymentId} is in flight and has not been written ahead yet; poll again`, null, {
      retriable: true,
    });
  }
  return errorReply(404, 'PAYMENT_NOT_FOUND', `no payment record for ${paymentId}`);
}

// ── HTTP plumbing ─────────────────────────────────────────────────────────────

function bearerOk(req: IncomingMessage): boolean {
  const h = req.headers['authorization'];
  if (typeof h !== 'string' || !h.startsWith('Bearer ')) return false;
  // Reuse the shared h3.constantTimeEquals rather than a second local copy (review fix).
  return constantTimeEquals(h.slice(7), AUTH_TOKEN);
}

function readBody(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on('data', (c: Buffer) => {
      size += c.length;
      if (size > 1_000_000) reject(new Error('body too large'));
      else chunks.push(c);
    });
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf8');
      if (!raw) return resolve({});
      try {
        resolve(JSON.parse(raw));
      } catch {
        reject(new Error('invalid JSON body'));
      }
    });
    req.on('error', reject);
  });
}

function send(res: ServerResponse, reply: Reply): void {
  const json = JSON.stringify(reply.json, null, 2);
  res.writeHead(reply.status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(json),
    'x-h3-version': '1.0',
  });
  res.end(json);
}

const server = createServer(async (req: IncomingMessage, res: ServerResponse) => {
  const method = req.method ?? 'GET';
  const path = (req.url ?? '').split('?')[0] ?? '';

  try {
    if (method === 'GET' && path === '/health') {
      return send(res, { status: 200, json: { ok: true, service: 'snapfall-h3-sidecar' } });
    }

    // Every /v1 route requires the bearer token.
    if (path.startsWith('/v1/')) {
      if (!bearerOk(req)) {
        return send(res, errorReply(401, 'UNAUTHENTICATED', 'missing or invalid bearer token'));
      }
    }

    if (method === 'POST' && path === '/v1/quote') {
      return send(res, await handleQuote(await readBody(req)));
    }
    if (method === 'POST' && path === '/v1/pay') {
      return send(res, await handlePay(await readBody(req)));
    }
    if (method === 'GET' && path.startsWith('/v1/status/')) {
      // decodeURIComponent throws on a malformed %-escape; keep that a 400, not a 500.
      let id: string;
      try {
        id = decodeURIComponent(path.slice('/v1/status/'.length));
      } catch {
        return send(res, errorReply(400, 'BAD_REQUEST', 'malformed paymentId in path'));
      }
      return send(res, handleStatus(id));
    }

    return send(res, errorReply(404, 'BAD_REQUEST', `no such route: ${method} ${path}`));
  } catch (e) {
    const msg = (e as Error).message;
    const status = msg === 'invalid JSON body' || msg === 'body too large' ? 400 : 500;
    return send(res, errorReply(status, status === 400 ? 'BAD_REQUEST' : 'INTERNAL', msg));
  }
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[h3] Snapfall payment sidecar on http://127.0.0.1:${PORT} (loopback only)`);
  console.log(`[h3] signer ${signer.address} · store ${STORE_PATH}`);
});
