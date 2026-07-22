/**
 * Snapfall H3 — shared crypto/canonical helpers for the payment sidecar API.
 *
 * These are the byte-exact definitions BOTH the sidecar (V4) and Gnanam's approval service
 * (H2) must agree on: the canonical intent JSON, its keccak256 hash, the deterministic
 * paymentId and authNonce, and the approval-token HMAC. Keeping them in one module means the
 * two sides cannot drift. Contract: docs/handshakes/H3-sidecar-api.md §3, §4.
 */

import { keccak256, toHex, type Hex } from 'viem';
import { createHmac, timingSafeEqual } from 'node:crypto';

/** ApprovedIntent as it travels on the wire — amounts are atomic-USDC decimal STRINGS. */
export interface WireIntent {
  intentId: string;
  jobId: string;
  taskId: string;
  agentId: string;
  resource: string;
  network: string;
  asset: string;
  merchant: string;
  amount: string;
  maxAmount: string;
  purpose: string;
  nonce: string;
  decision: 'AUTO_APPROVE' | 'HUMAN_APPROVED' | 'HUMAN_APPROVAL_REQUIRED' | 'DENY';
  policyVersion: string;
  createdAt: string;
  expiresAt: string;
}

/** The H2 approval-decision object (Gnanam's shape), consumed unchanged by `pay`. */
export interface ApprovalToken {
  intentHash: string;
  decision: string;
  approvedAmount: string;
  approver: string;
  policyVersion: string;
  issuedAt: string;
  expiresAt: string;
  signature: string;
}

/**
 * Canonical intent JSON (H3 §3.3): EXACTLY these 14 fields, keys lexicographically sorted,
 * no whitespace, string values verbatim. The object literal below is written in sorted key
 * order, and JSON.stringify preserves string-key insertion order, so the output is canonical.
 * `decision` and `createdAt` are deliberately excluded from the hash.
 */
export function canonicalIntent(i: WireIntent): string {
  return JSON.stringify({
    agentId: i.agentId,
    amount: i.amount,
    asset: i.asset,
    expiresAt: i.expiresAt,
    intentId: i.intentId,
    jobId: i.jobId,
    maxAmount: i.maxAmount,
    merchant: i.merchant,
    network: i.network,
    nonce: i.nonce,
    policyVersion: i.policyVersion,
    purpose: i.purpose,
    resource: i.resource,
    taskId: i.taskId,
  });
}

/** keccak256 over the canonical intent (H3 §3.3). */
export function computeIntentHash(i: WireIntent): Hex {
  return keccak256(toHex(canonicalIntent(i)));
}

/** Deterministic paymentId (H3 §4.1): "pay_" + first 16 hex chars of keccak256(hash|nonce). */
export function computePaymentId(intentHash: string, nonce: string): string {
  return 'pay_' + keccak256(toHex(`${intentHash}|${nonce}`)).slice(2, 18);
}

/** Deterministic EIP-3009 authorization nonce (H3 §4.4). Same intent -> same on-chain nonce,
 *  so the seller's replay guard catches a duplicate even across a sidecar restart. */
export function computeAuthNonce(intentHash: string): Hex {
  return keccak256(toHex(`${intentHash}|auth`));
}

/** Approval HMAC (H3 §3.4). Lowercase hex over intentHash|decision|approvedAmount|expiresAt. */
export function signApproval(
  secret: string,
  t: { intentHash: string; decision: string; approvedAmount: string; expiresAt: string },
): string {
  const msg = `${t.intentHash}|${t.decision}|${t.approvedAmount}|${t.expiresAt}`;
  return createHmac('sha256', secret).update(msg).digest('hex');
}

/** Constant-time verify of an approval token's HMAC signature. */
export function verifyApproval(secret: string, token: ApprovalToken): boolean {
  const expected = signApproval(secret, token);
  return constantTimeEquals(expected, (token.signature ?? '').toLowerCase());
}

/** Length-checked constant-time string compare (utf8 bytes). */
export function constantTimeEquals(a: string, b: string): boolean {
  const ba = Buffer.from(a, 'utf8');
  const bb = Buffer.from(b, 'utf8');
  if (ba.length !== bb.length) return false;
  return timingSafeEqual(ba, bb);
}
