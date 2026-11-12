/**
 * Snapfall x402 BUYER — the payment sidecar's signing path (FR-X402-003, FR-PAY-005).
 *
 * Loop: request -> 402 challenge -> sign EIP-3009 authorization -> retry with
 * `X-PAYMENT` -> 200 + data + receipt.
 *
 * ARCHITECTURE LAW (PRD §6.1, FR-PAY-005): the treasury signs ONLY after a deterministic
 * policy decision has approved the intent. That is enforced structurally here — `purchase()`
 * demands an `ApprovedIntent`, and refuses to sign anything whose decision is not
 * AUTO_APPROVE or HUMAN_APPROVED. An agent cannot call this with a bare URL and an amount.
 *
 * AT-05 SUBSTITUTION DEFENCE (why this file was refactored): the treasury signs the LIVE
 * challenge's payee/amount/asset. Binding the approval to a hash is not enough — the signer
 * has to actually consume the approved terms. So the flow probes ONCE, asserts the live
 * `accept` still matches the approved merchant/amount/asset BEFORE signing, and signs that
 * exact object. A re-quoting or compromised seller that keeps amount <= maxAmount but swaps
 * the payee (or raises the price toward the ceiling) is rejected before any key is used, with
 * no second probe and therefore no TOCTOU window. See docs/handshakes/H3-sidecar-api.md §6.
 *
 * `purchase()` runs probe -> validate -> sign as one call. The H3 service (V4) instead calls
 * `probeChallenge` + `assertAcceptMatchesIntent` + `signAndSubmit` separately, so it can write
 * a durable idempotency record BETWEEN validation and signing without a second probe.
 *
 * The signer is EOA-based per FR-X402-005 (Circle's buyer quickstart path, [R6]);
 * Agent Wallets swap in later behind the same function (FR-FLT-010).
 */

import { privateKeyToAccount } from 'viem/accounts';
import { circleConfigFromEnv, ENV_WALLET_ID, loadCircleAccount } from './circle-wallet.js';
import { toHex, type Address, type Hex, type LocalAccount } from 'viem';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import {
  encodeBase64Json,
  decodeBase64Json,
  readChallenge,
  formatUsdc,
  TRANSFER_WITH_AUTHORIZATION_TYPES,
  type AcceptOption,
  type PaymentPayload,
} from './x402.js';

/**
 * A payment intent that has already cleared the policy engine.
 * Mirrors the decision record in PRD Appendix A.2 — the sidecar never re-decides,
 * it only checks that a decision exists and that the challenge still matches it.
 *
 * The approved-term fields (`merchant`/`amount`/`asset`/`network`) are optional in this
 * type so existing callers keep working, but the H3/V4 layer ALWAYS supplies them (copied
 * from the `quote` challenge the owner approved). When present, the challenge MUST match
 * them before signing — that is the AT-05 defence.
 */
export interface ApprovedIntent {
  intentId: string;
  jobId: string;
  taskId: string;
  agentId: string;
  /** Resource URL the agent asked for. */
  resource: string;
  /** Atomic USDC the policy engine reserved as the spend ceiling (FR-PAY-006). */
  maxAmount: bigint;
  decision: 'AUTO_APPROVE' | 'HUMAN_APPROVED' | 'HUMAN_APPROVAL_REQUIRED' | 'DENY';
  policyVersion: string;

  // ── Approved terms. When set, the live challenge MUST match or the sign is refused. ──
  /** Approved payee (== quote `accept.payTo`). Mismatch -> MERCHANT_CHANGED. */
  merchant?: string;
  /** Approved price in atomic USDC (== quote `accept.amount`). Mismatch -> PRICE_CHANGED. */
  amount?: bigint;
  /** Approved token contract (== quote `accept.asset`). Mismatch -> ASSET_CHANGED. */
  asset?: string;
  /** Approved CAIP-2 network (== quote `accept.network`). Mismatch -> NETWORK_MISMATCH. */
  network?: string;
}

export interface PurchaseResult {
  data: unknown;
  receipt: {
    resource: string;
    amount: string;
    payer: string;
    payee: string;
    nonce: string;
    settlement: string;
    /**
     * Which facilitator broadcast this, when one did.
     *
     * Carried so evidence can name its own source: a transaction hash proves money moved, not
     * WHO moved it, and V1's claim is specifically that Circle's facilitator settled the payment.
     * Absent when no broadcast was attempted, in which case `settlement` is a dry-run marker.
     */
    facilitator?: { verify: string; settle: string };
  };
  /** Hash of the signed authorization, for the audit receipt (FR-X402-004). */
  authorizationSignature: string;
  amountPaid: bigint;
}

/** Stable reasons a purchase was refused BEFORE any signature (policy/substitution). */
export type PolicyCode =
  | 'INTENT_NOT_APPROVED'
  | 'NETWORK_MISMATCH'
  | 'MERCHANT_CHANGED'
  | 'ASSET_CHANGED'
  | 'PRICE_CHANGED'
  | 'PRICE_EXCEEDS_RESERVED';

/** Stable reasons a purchase failed on the wire (probe/challenge/seller/transport). */
export type PaymentCode =
  | 'RESOURCE_NOT_FOUND'
  | 'CHALLENGE_UNAVAILABLE'
  | 'NO_MATCHING_NETWORK'
  | 'PAYMENT_REJECTED'
  | 'UPSTREAM_UNREACHABLE'
  | 'FACILITATOR_ERROR';

/**
 * Thrown when the intent was never approved, or the live challenge no longer matches the
 * approved terms. NO signature is produced in any of these cases. `code` is a stable enum so
 * the H3 wrapper maps to an error code without string-matching the message.
 */
export class PolicyViolation extends Error {
  readonly code: PolicyCode;
  constructor(code: PolicyCode, message: string) {
    super(message);
    this.name = 'PolicyViolation';
    this.code = code;
  }
}

/** Thrown on a wire/transport/seller failure. POST-SIGN codes (the signed authorization
 *  was already submitted, so it may still settle and MUST be reconciled, not released):
 *  `FACILITATOR_ERROR` and `PAYMENT_REJECTED` (a non-200 to X-PAYMENT still means the
 *  seller received the bearer authorization). PRE-SIGN codes are safe to release:
 *  `RESOURCE_NOT_FOUND`, `CHALLENGE_UNAVAILABLE`, `NO_MATCHING_NETWORK`,
 *  `UPSTREAM_UNREACHABLE`. (Dry run: no broadcast, so PAYMENT_REJECTED is effectively
 *  terminal; the distinction becomes load-bearing once a real facilitator is wired.) */
/**
 * The post-sign codes as a VALUE, not just as prose in the comment above.
 *
 * The doc comment had the taxonomy right and service.ts implemented half of it: it special-cased
 * FACILITATOR_ERROR and let PAYMENT_REJECTED fall through to FAILED, which the daemon reads as
 * "nothing settled, release" (h3.Unresolved). That releases the reservation backing a signed
 * bearer authorization the seller has already received. Exporting the set means the rule has one
 * home and the two cannot drift again.
 */
export const POST_SIGN_CODES: ReadonlySet<PaymentCode> = new Set<PaymentCode>([
  'FACILITATOR_ERROR',
  'PAYMENT_REJECTED',
]);

/** True when the signed authorization left this process and may still settle. */
export function isPostSign(e: unknown): boolean {
  return e instanceof PaymentFailed && POST_SIGN_CODES.has(e.code);
}

export class PaymentFailed extends Error {
  readonly code: PaymentCode;
  constructor(code: PaymentCode, message: string) {
    super(message);
    this.name = 'PaymentFailed';
    this.code = code;
  }
}

/**
 * Thrown by `signAndSubmit` when producing the signature itself failed, so NO authorization
 * exists and nothing was sent. The one post-write-ahead failure that is a proven non-settlement.
 */
export class SigningFailed extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SigningFailed';
  }
}

/**
 * True ONLY when a failure raised after the write-ahead record is PROVEN not to have let a signed
 * authorization out of this process: the signature was never produced. That is the only case the
 * sidecar may record as FAILED, which the daemon reads as "nothing settled, release the hold".
 *
 * Everything else is indeterminate and must be recorded RECONCILING (hold, reconcile): once
 * X-PAYMENT has been sent the seller holds a bearer instrument, and no shape of its answer -- a
 * 200 with no receipt, a null body, a v2 PAYMENT-RESPONSE header, a parse error, a bug in our own
 * post-processing -- proves it will not be settled. This is deliberately a positive test for
 * "provably unsent" rather than a negative test for "known post-sign codes": an error type nobody
 * anticipated must default to keeping the hold, never to releasing it.
 */
export function provenNotSent(e: unknown): boolean {
  return e instanceof SigningFailed;
}

/** Default bound on the 402 probe (headers + challenge body), in ms. SIDECAR_PROBE_TIMEOUT_MS
 *  overrides it; anything unparseable or non-positive falls back to the default. */
const DEFAULT_PROBE_TIMEOUT_MS = 10_000;
function probeTimeoutMs(): number {
  const v = Number(process.env.SIDECAR_PROBE_TIMEOUT_MS);
  return Number.isFinite(v) && v > 0 ? v : DEFAULT_PROBE_TIMEOUT_MS;
}

/** Fresh 32-byte nonce. Replay protection lives with the seller; uniqueness lives here.
 *  H3 supplies a deterministic nonce instead (derived from the intent hash) so the seller's
 *  replay guard survives a sidecar restart; this random default is for non-H3 callers. */
function freshNonce(): Hex {
  return toHex(crypto.getRandomValues(new Uint8Array(32)));
}

/**
 * Probe a resource for its x402 challenge and select the accept option for `chainId`.
 * Read-only: no key touched, nothing signed. Shared by `purchase()` and the H3 `quote`
 * endpoint (V4), so the probe/select logic cannot drift between the two paths.
 */
export async function probeChallenge(resource: string, chainId: number): Promise<AcceptOption> {
  // Validate the URL BEFORE fetching: `intent.resource` is caller-supplied, so an
  // unvalidated GET is an SSRF surface. Reject anything that is not a well-formed
  // http(s) URL. (A configurable host allowlist is the production hardening; noted for
  // the H3 session. Here we at least refuse non-http schemes and unparseable URLs so a
  // malformed or file://-style resource cannot trigger a fetch — review fix.)
  let url: URL;
  try {
    url = new URL(resource);
  } catch {
    throw new PaymentFailed('RESOURCE_NOT_FOUND', `resource is not a valid URL: ${resource}`);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new PaymentFailed('RESOURCE_NOT_FOUND', `resource must be http(s), got ${url.protocol}`);
  }

  // Bounded. The seller chooses how long an unbounded probe lasts, and the H3 caller is holding a
  // budget reservation for exactly that long; the signal covers the headers AND the body read in
  // readChallenge, so a seller that trickles its challenge is cut off too (a body aborted mid-read
  // parses as no challenge: CHALLENGE_UNAVAILABLE, still pre-sign).
  let probe: Response;
  try {
    probe = await fetch(resource, { method: 'GET', signal: AbortSignal.timeout(probeTimeoutMs()) });
  } catch (e) {
    throw new PaymentFailed('UPSTREAM_UNREACHABLE', `cannot reach ${resource}: ${(e as Error).message}`);
  }
  if (probe.status !== 402) {
    throw new PaymentFailed('RESOURCE_NOT_FOUND', `expected HTTP 402 from ${resource}, got ${probe.status}`);
  }

  const challenge = await readChallenge(probe);
  if (!challenge || challenge.accepts.length === 0) {
    throw new PaymentFailed('CHALLENGE_UNAVAILABLE', `${resource} returned 402 with no usable payment options`);
  }

  const accept = challenge.accepts.find((a) => a.network === `eip155:${chainId}`);
  if (!accept) {
    throw new PaymentFailed(
      'NO_MATCHING_NETWORK',
      `seller offers [${challenge.accepts.map((a) => a.network).join(', ')}], ` +
        `we can only pay on eip155:${chainId}`,
    );
  }
  return accept;
}

/**
 * Assert the live `accept` still matches the approved intent, BEFORE signing.
 *
 * This is the AT-05 substitution defence. Every approved term the caller pinned is compared
 * against the live challenge on the SAME object that will be signed — never a re-fetch, so
 * there is no time-of-check/time-of-use gap. Terms the caller did not pin are skipped; the
 * `maxAmount` ceiling (Gate 2) is always enforced even when `amount` was not pinned.
 */
export function assertAcceptMatchesIntent(accept: AcceptOption, intent: ApprovedIntent): void {
  const sameAddr = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

  if (intent.network !== undefined && accept.network !== intent.network) {
    throw new PolicyViolation(
      'NETWORK_MISMATCH',
      `challenge network ${accept.network} != approved ${intent.network}`,
    );
  }
  if (intent.merchant !== undefined && !sameAddr(accept.payTo, intent.merchant)) {
    throw new PolicyViolation(
      'MERCHANT_CHANGED',
      `seller payee ${accept.payTo} != approved merchant ${intent.merchant}; re-approval required (FR-APR-004)`,
    );
  }
  if (intent.asset !== undefined && !sameAddr(accept.asset, intent.asset)) {
    throw new PolicyViolation(
      'ASSET_CHANGED',
      `seller asset ${accept.asset} != approved asset ${intent.asset}; re-approval required (FR-APR-004)`,
    );
  }
  if (intent.amount !== undefined && BigInt(accept.amount) !== intent.amount) {
    throw new PolicyViolation(
      'PRICE_CHANGED',
      `seller price ${formatUsdc(accept.amount)} != approved ${formatUsdc(intent.amount)} USDC; ` +
        're-approval required (FR-APR-004)',
    );
  }
  // ── Gate 2: the reserved ceiling. Always enforced (AT-05 amount half). ──
  if (BigInt(accept.amount) > intent.maxAmount) {
    throw new PolicyViolation(
      'PRICE_EXCEEDS_RESERVED',
      `seller wants ${formatUsdc(accept.amount)} USDC but policy reserved ${formatUsdc(intent.maxAmount)}; ` +
        're-evaluation required (FR-APR-004)',
    );
  }
}

/**
 * Sign an EIP-3009 `transferWithAuthorization` for one accept option.
 * This is the only place a key is used, and it is reached only past the guards above.
 * `nonce` is supplied by H3 (deterministic) or defaults to a fresh random one.
 */
/** Cap on how long a signed authorization stays valid, regardless of what the seller
 *  asks for. The authorization is a bearer instrument once signed; a hostile seller
 *  requesting `maxTimeoutSeconds: 10_years` should not get a decade-lived signature.
 *  Amount and payee are already bound, so this bounds only the time window. */
const MAX_AUTH_LIFETIME_SECONDS = 3600; // 1 hour

async function signAuthorization(
  account: LocalAccount,
  accept: AcceptOption,
  chainId: number,
  nonce?: Hex,
): Promise<PaymentPayload> {
  const now = Math.floor(Date.now() / 1000);
  const lifetime = Math.min(Math.max(0, accept.maxTimeoutSeconds), MAX_AUTH_LIFETIME_SECONDS);
  const authorization = {
    from: account.address,
    to: accept.payTo as Address,
    value: accept.amount,
    validAfter: '0',
    validBefore: String(now + lifetime),
    nonce: nonce ?? freshNonce(),
  };

  const signature = await account.signTypedData({
    domain: {
      name: accept.extra?.name ?? USDC_EIP712_DOMAIN.name,
      version: accept.extra?.version ?? USDC_EIP712_DOMAIN.version,
      chainId,
      verifyingContract: accept.asset as Address,
    },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: authorization.from,
      to: authorization.to,
      value: BigInt(authorization.value),
      validAfter: BigInt(authorization.validAfter),
      validBefore: BigInt(authorization.validBefore),
      nonce: authorization.nonce,
    },
  });

  return {
    x402Version: 2,
    scheme: 'exact',
    network: accept.network,
    payload: { signature, authorization },
  };
}

/**
 * Sign a validated `accept` and submit the `X-PAYMENT`. The caller MUST have already run
 * `assertAcceptMatchesIntent(accept, intent)` on THIS accept object — `signAndSubmit` does
 * not re-probe, so the signed message binds the exact validated terms (no TOCTOU).
 *
 * @throws PaymentFailed  code `PAYMENT_REJECTED` (seller said no) or `FACILITATOR_ERROR`
 *                        (transport failed AFTER signing, or a 200 without a usable receipt —
 *                        the authorization may still settle).
 * @throws SigningFailed  the signature was never produced; nothing left the process.
 */
export async function signAndSubmit(
  accept: AcceptOption,
  intent: ApprovedIntent,
  account: LocalAccount,
  chainId: number,
  authNonce?: Hex,
): Promise<PurchaseResult> {
  let payment: PaymentPayload;
  try {
    payment = await signAuthorization(account, accept, chainId, authNonce);
  } catch (e) {
    // No signature was produced, so nothing can be presented for settlement. See provenNotSent.
    throw new SigningFailed(`signing the authorization for ${intent.resource} failed: ${(e as Error).message}`);
  }

  // ── From here the signed authorization is about to leave, or has left, this process. Every
  //    failure below is post-sign: PAYMENT_REJECTED or FACILITATOR_ERROR, never a bare throw. ──
  let paid: Response;
  try {
    paid = await fetch(intent.resource, {
      method: 'GET',
      headers: { 'X-PAYMENT': encodeBase64Json(payment) },
    });
  } catch (e) {
    throw new PaymentFailed(
      'FACILITATOR_ERROR',
      `payment submit failed after signing ${intent.resource}: ${(e as Error).message}`,
    );
  }

  if (paid.status !== 200) {
    const body = await readChallenge(paid);
    throw new PaymentFailed(
      'PAYMENT_REJECTED',
      `payment rejected (HTTP ${paid.status}): ${body?.error ?? 'no reason given'}`,
    );
  }

  // A parse failure AFTER the seller's 200 is post-submit: the authorization was signed
  // and delivered, so the outcome is unknown, not a clean failure. Surface it as
  // FACILITATOR_ERROR (may still settle; reconcile) rather than letting a raw
  // SyntaxError escape (review fix).
  let parsed: unknown;
  try {
    parsed = await paid.json();
  } catch (e) {
    throw new PaymentFailed(
      'FACILITATOR_ERROR',
      `seller returned 200 but its body did not parse (${(e as Error).message}); ` +
        `the signed authorization was submitted and may still settle — reconcile`,
    );
  }
  const body = typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, unknown>) : null;

  // The receipt, from the first transport that carries a usable one: the v1 X-PAYMENT-RESPONSE
  // header (FR-X402-004 evidence chain), then the v2 PAYMENT-RESPONSE header, then the body.
  // A 200 with NONE of them -- a plain resource body, a null body, or a v2 settlement response
  // that is not a Snapfall receipt -- is not a failure: the seller holds the authorization and
  // may settle it, so it is FACILITATOR_ERROR (post-sign, reconcile), never a TypeError that the
  // service would record as FAILED and the daemon would read as "release".
  const candidates: unknown[] = [];
  for (const h of ['x-payment-response', 'payment-response']) {
    const raw = paid.headers.get(h);
    if (raw) candidates.push(decodeBase64Json<unknown>(raw.trim()));
  }
  if (body) candidates.push(body.receipt);
  const receipt = candidates.find(isReceipt);
  if (!receipt) {
    throw new PaymentFailed(
      'FACILITATOR_ERROR',
      `seller returned 200 for ${intent.resource} without a usable payment receipt ` +
        `(body ${body === null ? JSON.stringify(parsed) : 'has no receipt'}; ` +
        `x-payment-response ${paid.headers.has('x-payment-response') ? 'unusable' : 'absent'}; ` +
        `payment-response ${paid.headers.has('payment-response') ? 'unusable' : 'absent'}); ` +
        'the signed authorization was delivered and may still settle — reconcile',
    );
  }

  return {
    data: body?.data ?? null,
    receipt,
    authorizationSignature: payment.payload.signature,
    amountPaid: BigInt(accept.amount),
  };
}

/** A receipt the H3 record can be built from: every field it reads is a string. */
function isReceipt(r: unknown): r is PurchaseResult['receipt'] {
  if (typeof r !== 'object' || r === null) return false;
  const o = r as Record<string, unknown>;
  return ['amount', 'payer', 'payee', 'nonce', 'settlement'].every((k) => typeof o[k] === 'string');
}

/**
 * Execute one policy-approved purchase end to end.
 *
 * Order (no signature before the gates all pass):
 *   Gate 1 (decision) -> probe ONCE -> assert live accept matches approved terms + ceiling
 *   -> sign that exact accept -> submit X-PAYMENT -> 200 + receipt.
 *
 * @throws PolicyViolation  intent not approved, or the live challenge diverged from the
 *                          approved terms / exceeds the ceiling. No signature.
 * @throws PaymentFailed    a wire/seller/transport failure (see `signAndSubmit`).
 */
export async function purchase(
  intent: ApprovedIntent,
  account: LocalAccount,
  opts: { chainId: number; authNonce?: Hex },
): Promise<PurchaseResult> {
  // ── Gate 1: FR-PAY-005. No approved decision, no signature. ──
  if (intent.decision !== 'AUTO_APPROVE' && intent.decision !== 'HUMAN_APPROVED') {
    throw new PolicyViolation(
      'INTENT_NOT_APPROVED',
      `intent ${intent.intentId} is ${intent.decision}; the treasury signs only approved intents`,
    );
  }

  // ── Probe exactly once; validate then sign the SAME accept (no TOCTOU). ──
  const accept = await probeChallenge(intent.resource, opts.chainId);
  assertAcceptMatchesIntent(accept, intent);
  return signAndSubmit(accept, intent, account, opts.chainId, opts.authNonce);
}

/**
 * Load the treasury signer. `CIRCLE_TREASURY_WALLET_ID` present selects the Circle
 * developer-controlled wallet (circle-wallet.ts), the same rule the daemon's treasury lane uses;
 * otherwise `TREASURY_PRIVATE_KEY`, testnet keys only (see sidecar/.env.example).
 *
 * A configured Circle wallet that fails to load is an error, never a fall back to the raw key:
 * an operator who moved the treasury to Circle must not be paying from a shell-file key without
 * knowing it.
 */
export async function loadSigner(env: NodeJS.ProcessEnv = process.env): Promise<LocalAccount> {
  if (env[ENV_WALLET_ID]?.trim()) {
    return loadCircleAccount(circleConfigFromEnv(env));
  }
  const key = env.TREASURY_PRIVATE_KEY;
  if (!key) {
    throw new Error(
      'TREASURY_PRIVATE_KEY is not set. Copy sidecar/.env.example to .env and fill it ' +
        'with a TESTNET-ONLY key. Never a funded mainnet key.',
    );
  }
  return privateKeyToAccount(key as Hex);
}
