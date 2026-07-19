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
 */
export interface ApprovedIntent {
  intentId: string;
  jobId: string;
  taskId: string;
  agentId: string;
  /** Resource URL the agent asked for. */
  resource: string;
  /** Atomic USDC the policy engine reserved (FR-PAY-006). */
  maxAmount: bigint;
  decision: 'AUTO_APPROVE' | 'HUMAN_APPROVED' | 'HUMAN_APPROVAL_REQUIRED' | 'DENY';
  policyVersion: string;
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

export class PolicyViolation extends Error {}
export class PaymentFailed extends Error {}

/** Fresh 32-byte nonce. Replay protection lives with the seller; uniqueness lives here. */
function freshNonce(): Hex {
  return toHex(crypto.getRandomValues(new Uint8Array(32)));
}

/**
 * Sign an EIP-3009 `transferWithAuthorization` for one accept option.
 * This is the only place a key is used, and it is reached only past the guards in `purchase()`.
 */
async function signAuthorization(
  account: LocalAccount,
  accept: AcceptOption,
  chainId: number,
): Promise<PaymentPayload> {
  const now = Math.floor(Date.now() / 1000);
  const authorization = {
    from: account.address,
    to: accept.payTo as Address,
    value: accept.amount,
    validAfter: '0',
    validBefore: String(now + accept.maxTimeoutSeconds),
    nonce: freshNonce(),
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
 * Execute one policy-approved purchase.
 *
 * @throws PolicyViolation if the intent was never approved, or if the seller's price
 *         exceeds what the policy engine reserved. Both cases mean NO signature is produced.
 */
export async function purchase(
  intent: ApprovedIntent,
  account: LocalAccount,
  opts: { chainId: number },
): Promise<PurchaseResult> {
  // ── Gate 1: FR-PAY-005. No approved decision, no signature. ──
  if (intent.decision !== 'AUTO_APPROVE' && intent.decision !== 'HUMAN_APPROVED') {
    throw new PolicyViolation(
      `intent ${intent.intentId} is ${intent.decision}; the treasury signs only approved intents`,
    );
  }

  // ── Step 1: unpaid request. Expect a 402 with payment requirements. ──
  const probe = await fetch(intent.resource, { method: 'GET' });
  if (probe.status !== 402) {
    throw new PaymentFailed(`expected HTTP 402 from ${intent.resource}, got ${probe.status}`);
  }

  const challenge = await readChallenge(probe);
  if (!challenge || challenge.accepts.length === 0) {
    throw new PaymentFailed(`${intent.resource} returned 402 with no usable payment options`);
  }

  const accept = challenge.accepts.find((a) => a.network === `eip155:${opts.chainId}`);
  if (!accept) {
    throw new PaymentFailed(
      `seller offers [${challenge.accepts.map((a) => a.network).join(', ')}], ` +
        `we can only pay on eip155:${opts.chainId}`,
    );
  }

  // ── Gate 2: AT-05 substitution defence. The approval was bound to an amount;
  //    if the seller now quotes more, the approval is void and must be re-issued. ──
  const price = BigInt(accept.amount);
  if (price > intent.maxAmount) {
    throw new PolicyViolation(
      `seller wants ${formatUsdc(price)} USDC but policy reserved ${formatUsdc(intent.maxAmount)}; ` +
        're-evaluation required (FR-APR-004)',
    );
  }

  // ── Step 2: sign. Past both gates, this is the treasury acting on an approved intent. ──
  const payment = await signAuthorization(account, accept, opts.chainId);

  // ── Step 3: retry with the authorization attached. ──
  const paid = await fetch(intent.resource, {
    method: 'GET',
    headers: { 'X-PAYMENT': encodeBase64Json(payment) },
  });

  if (paid.status !== 200) {
    const body = await readChallenge(paid);
    throw new PaymentFailed(
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
    amountPaid: price,
  };
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
