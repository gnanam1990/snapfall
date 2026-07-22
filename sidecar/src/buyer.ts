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
import { toHex, type Address, type Hex, type LocalAccount } from 'viem';
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

/** Thrown on a wire/transport/seller failure. `FACILITATOR_ERROR` is post-sign (may still
 *  settle, must reconcile); all other codes are pre-sign (safe to release). */
export class PaymentFailed extends Error {
  readonly code: PaymentCode;
  constructor(code: PaymentCode, message: string) {
    super(message);
    this.name = 'PaymentFailed';
    this.code = code;
  }
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
  let probe: Response;
  try {
    probe = await fetch(resource, { method: 'GET' });
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
      name: accept.extra?.name ?? 'USD Coin',
      version: accept.extra?.version ?? '2',
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
 *                        (transport failed AFTER signing — the authorization may still settle).
 */
export async function signAndSubmit(
  accept: AcceptOption,
  intent: ApprovedIntent,
  account: LocalAccount,
  chainId: number,
  authNonce?: Hex,
): Promise<PurchaseResult> {
  const payment = await signAuthorization(account, accept, chainId, authNonce);

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

  const { data, receipt } = (await paid.json()) as PurchaseResult;

  // Prefer the header receipt when present (FR-X402-004 evidence chain).
  const headerReceipt = paid.headers.get('x-payment-response');
  const finalReceipt = headerReceipt
    ? decodeBase64Json<PurchaseResult['receipt']>(headerReceipt) ?? receipt
    : receipt;

  return {
    data,
    receipt: finalReceipt,
    authorizationSignature: payment.payload.signature,
    amountPaid: BigInt(accept.amount),
  };
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

/** Load the treasury signer. Testnet keys only — see sidecar/.env.example. */
export function loadSigner(): LocalAccount {
  const key = process.env.TREASURY_PRIVATE_KEY;
  if (!key) {
    throw new Error(
      'TREASURY_PRIVATE_KEY is not set. Copy sidecar/.env.example to .env and fill it ' +
        'with a TESTNET-ONLY key. Never a funded mainnet key.',
    );
  }
  return privateKeyToAccount(key as Hex);
}
