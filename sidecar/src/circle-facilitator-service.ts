/**
 * Circle Facilitator Service — Circle's hosted x402 facilitator, the one that settles on Arc
 * MAINNET (eip155:5042) as well as testnet.
 *
 * facilitator.ts integrates Circle Gateway's x402 endpoints, which are testnet-only and access
 * gated. Facilitator Service is a different API (api.circle.com/v1/facilitator/x402, x402 v2,
 * EIP-3009 for `exact`) and the documented mainnet route, so on mainnet a seller either uses this
 * or relays itself (self-facilitator.ts). Selected by SNAPFALL_CIRCLE_FACILITATOR_SERVICE=1.
 *
 * Authentication is a Circle API key (CIRCLE_API_KEY) or, for the keyless trial, a seller proof:
 * an EIP-712 signature by the payTo key (SELLER_PRIVATE_KEY) over the request's purpose, method,
 * body hash, network and payTo. Settling with a key binds the payTo to the Circle account and
 * ends the trial; the trial covers `exact` only, which is all this seller sells.
 *
 * Same honesty rules as the Gateway client (see facilitator.ts):
 *   - only a 0x-64 transaction hash on `success: true` is `settled`;
 *   - `settlement_pending`, a timeout, a 5xx or a 409 is `unknown` (the authorization may land:
 *     reconcile through /status, never release);
 *   - a 4xx or a terminal errorReason is `rejected` (refused before broadcast).
 * The endpoints are constants with no environment override, so nothing that can set an env var
 * can redirect the API key or the paid resource to a host of its choosing.
 *
 * Docs: developers.circle.com/facilitator-service, API reference "Verify/Settle an x402 payment"
 * (checked 29 Oct 2026). Verified live: keyless /verify on Arc testnet and mainnet accepts this
 * client's envelope and body (it answers on the buyer's balance, not on the request's shape).
 */

import { keccak256, toBytes, toHex, type Hex } from 'viem';
import { privateKeyToAccount } from 'viem/accounts';

import type { Facilitator } from './facilitator-select.js';
import type { PaymentRequirements, SettleOutcome, VerifyOutcome } from './facilitator.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import type { PaymentPayload } from './x402.js';

export const CIRCLE_FACILITATOR_SERVICE_ENDPOINTS = {
  verify: 'https://api.circle.com/v1/facilitator/x402/verify',
  settle: 'https://api.circle.com/v1/facilitator/x402/settle',
  status: 'https://api.circle.com/v1/facilitator/x402/status',
} as const;

const TX_HASH_RE = /^0x[0-9a-fA-F]{64}$/;
const CAIP2_EVM_RE = /^eip155:(\d+)$/;
const PROOF_TTL_SECONDS = 300; // the service's maximum

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface CircleFacilitatorServiceConfig {
  apiKey?: string;
  /** Key controlling payTo, for the keyless trial's seller proof. Ignored when apiKey is set. */
  sellerPrivateKey?: string;
  /** Injected only by tests. */
  fetchImpl?: FetchLike;
  /** Injected only by tests, so the proof's issuedAt is deterministic. */
  now?: () => number;
  timeoutMs?: number;
}

const SELLER_PROOF_TYPES = {
  SellerRequest: [
    { name: 'purpose', type: 'string' },
    { name: 'method', type: 'string' },
    { name: 'bodyHash', type: 'bytes32' },
    { name: 'network', type: 'string' },
    { name: 'payTo', type: 'address' },
    { name: 'nonce', type: 'bytes32' },
    { name: 'issuedAt', type: 'uint64' },
    { name: 'expiresAt', type: 'uint64' },
  ],
} as const;

/** The x402 v2 request body Facilitator Service expects, built from this seller's own types. */
export function facilitatorServiceBody(payment: PaymentPayload, requirements: PaymentRequirements): string {
  const accepted = {
    scheme: requirements.scheme,
    network: requirements.network,
    amount: requirements.maxAmountRequired,
    asset: requirements.asset,
    payTo: requirements.payTo,
    // The service's meaning is how long it may wait for a terminal state before answering
    // pending, not the authorization's validity, so it is capped rather than passed through.
    maxTimeoutSeconds: Math.min(requirements.maxTimeoutSeconds ?? 30, 60),
    extra: { name: USDC_EIP712_DOMAIN.name, version: USDC_EIP712_DOMAIN.version, assetTransferMethod: 'eip3009' },
  };
  return JSON.stringify({
    x402Version: 2,
    paymentPayload: {
      x402Version: 2,
      resource: { url: requirements.resource },
      accepted,
      payload: { signature: payment.payload.signature, authorization: payment.payload.authorization },
    },
    paymentRequirements: accepted,
  });
}

function chainIdOf(network: string): number {
  const m = CAIP2_EVM_RE.exec(network);
  if (!m) throw new Error(`not an EVM CAIP-2 network: ${network}`);
  return Number(m[1]);
}

export class CircleFacilitatorService implements Facilitator {
  private readonly apiKey: string;
  private readonly sellerPrivateKey: string;
  private readonly doFetch: FetchLike;
  private readonly now: () => number;
  private readonly timeoutMs: number;

  constructor(cfg: CircleFacilitatorServiceConfig = {}) {
    this.apiKey = (cfg.apiKey ?? process.env.CIRCLE_API_KEY ?? '').trim();
    this.sellerPrivateKey = (cfg.sellerPrivateKey ?? process.env.SELLER_PRIVATE_KEY ?? '').trim();
    this.doFetch = cfg.fetchImpl ?? ((input, init) => fetch(input, init));
    this.now = cfg.now ?? (() => Date.now());
    this.timeoutMs = cfg.timeoutMs ?? Number(process.env.CIRCLE_X402_TIMEOUT_MS ?? 20_000);
  }

  /** Configured with either credential. Without one, the seller reports NOT_BROADCAST honestly. */
  isConfigured(): boolean {
    return this.apiKey !== '' || this.sellerPrivateKey !== '';
  }

  endpoints(): { verify: string; settle: string } {
    return { verify: CIRCLE_FACILITATOR_SERVICE_ENDPOINTS.verify, settle: CIRCLE_FACILITATOR_SERVICE_ENDPOINTS.settle };
  }

  /** The Facilitator-Seller-Proof header for one request. Exported shape is the documented envelope. */
  async sellerProof(purpose: 'verify' | 'settle', body: string, network: string, payTo: string): Promise<string> {
    const account = privateKeyToAccount(this.sellerPrivateKey as Hex);
    if (account.address.toLowerCase() !== payTo.toLowerCase()) {
      // The service would answer 401; saying why here saves a confusing round trip.
      throw new Error(`SELLER_PRIVATE_KEY controls ${account.address}, not payTo ${payTo}`);
    }
    const issuedAt = BigInt(Math.floor(this.now() / 1000));
    const message = {
      purpose,
      method: 'POST',
      bodyHash: keccak256(toBytes(body)),
      network,
      payTo: payTo as Hex,
      nonce: toHex(crypto.getRandomValues(new Uint8Array(32))),
      issuedAt,
      expiresAt: issuedAt + BigInt(PROOF_TTL_SECONDS),
    };
    const signature = await account.signTypedData({
      domain: { name: 'Circle Facilitator Seller Request', version: '1', chainId: chainIdOf(network) },
      types: SELLER_PROOF_TYPES,
      primaryType: 'SellerRequest',
      message,
    });
    const envelope = {
      version: 1,
      signature,
      network,
      payTo,
      nonce: message.nonce,
      issuedAt: Number(message.issuedAt),
      expiresAt: Number(message.expiresAt),
    };
    return Buffer.from(JSON.stringify(envelope)).toString('base64url');
  }

  private async headers(purpose: 'verify' | 'settle', body: string, requirements: PaymentRequirements): Promise<Record<string, string>> {
    const h: Record<string, string> = { 'content-type': 'application/json', accept: 'application/json' };
    if (this.apiKey) h.authorization = `Bearer ${this.apiKey}`;
    else h['facilitator-seller-proof'] = await this.sellerProof(purpose, body, requirements.network, requirements.payTo);
    return h;
  }

  private async post(url: string, purpose: 'verify' | 'settle', payment: PaymentPayload, requirements: PaymentRequirements) {
    const body = facilitatorServiceBody(payment, requirements);
    const headers = await this.headers(purpose, body, requirements);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const res = await this.doFetch(url, { method: 'POST', headers, body, signal: controller.signal });
      const parsed = (await res.json().catch(() => ({}))) as Record<string, unknown>;
      return { res, parsed };
    } finally {
      clearTimeout(timer);
    }
  }

  /** Never broadcasts and never consumes trial usage, so every failure is a plain "not valid". */
  async verify(payment: PaymentPayload, requirements: PaymentRequirements): Promise<VerifyOutcome> {
    if (!this.isConfigured()) return { valid: false, reason: 'facilitator not configured' };
    try {
      const { res, parsed } = await this.post(CIRCLE_FACILITATOR_SERVICE_ENDPOINTS.verify, 'verify', payment, requirements);
      if (!res.ok) return { valid: false, reason: `verify HTTP ${res.status}: ${reasonOf(parsed)}` };
      const valid = parsed.isValid === true;
      return {
        valid,
        reason: valid ? undefined : str(parsed.invalidReason) || 'facilitator said the payment is not valid',
        payer: str(parsed.payer) || undefined,
      };
    } catch (e) {
      return { valid: false, reason: `verify failed: ${(e as Error).message}` };
    }
  }

  async settle(payment: PaymentPayload, requirements: PaymentRequirements): Promise<SettleOutcome> {
    if (!this.isConfigured()) return { state: 'not-configured' };

    let res: Response;
    let parsed: Record<string, unknown>;
    try {
      ({ res, parsed } = await this.post(CIRCLE_FACILITATOR_SERVICE_ENDPOINTS.settle, 'settle', payment, requirements));
    } catch (e) {
      // The request may have left this process and been acted on. Unknown, never failed.
      return { state: 'unknown', reason: `settle did not return: ${(e as Error).message}` };
    }

    if (!res.ok) {
      const reason = `settle HTTP ${res.status}: ${reasonOf(parsed)}`;
      // 409 conflicts with an existing payment (it may already have settled); 5xx may come after
      // submission. Both need reconciling, not a refusal that would invite a second charge.
      if (res.status >= 500 || res.status === 409) return { state: 'unknown', reason };
      return { state: 'rejected', reason };
    }

    const txHash = str(parsed.transaction);
    if (parsed.success === true) {
      if (TX_HASH_RE.test(txHash)) return { state: 'settled', txHash, payer: str(parsed.payer) || undefined };
      return { state: 'unknown', reason: `success without a usable transaction hash (got ${JSON.stringify(txHash)})` };
    }
    const errorReason = str(parsed.errorReason);
    if (errorReason === 'settlement_pending' || errorReason === '') {
      // Documented: pending is not failure. The payment exists and /status reconciles it.
      return { state: 'unknown', reason: `settlement pending${paymentIdOf(parsed) ? ` (paymentId ${paymentIdOf(parsed)})` : ''}` };
    }
    return { state: 'rejected', reason: errorReason };
  }
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

function reasonOf(parsed: Record<string, unknown>): string {
  return str(parsed.errorReason) || str(parsed.invalidReason) || str(parsed.message) || str(parsed.error) || 'no reason given';
}

function paymentIdOf(parsed: Record<string, unknown>): string {
  const ext = parsed.extensions as Record<string, unknown> | undefined;
  const status = ext?.['settlement-status'] as Record<string, unknown> | undefined;
  const info = (status?.info ?? status) as Record<string, unknown> | undefined;
  return str(info?.paymentId);
}
