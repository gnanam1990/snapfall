/**
 * x402 wire format — shared by our paid demo API (seller) and the sidecar (buyer).
 *
 * Schema confirmed against circlefin/agent-stack-starter-kits `packages/circle-tools`
 * (Apache-2.0, read 19 Jul 2026), specifically `services.ts:readAccepts`:
 *
 *   - x402 **v2** carries the challenge in a `payment-required` response header,
 *     base64-encoded JSON `{ accepts: [...] }`.
 *   - x402 **v1** carries the same object in the 402 response *body*.
 *   - Gateway-batched options are identified by `extra.name === 'GatewayWalletBatched'`,
 *     NOT by the top-level `scheme` (which reads `exact` for both).
 *
 * We emit BOTH transports so either generation of buyer can read our seller.
 */

/** One payment option in a 402 challenge. */
export interface AcceptOption {
  /** Always `exact` for our demo API. Gateway-batched is distinguished by `extra.name`. */
  scheme: 'exact';
  /** CAIP-2 chain id, or an x402 short name. Arc testnet is `eip155:5042002`. */
  network: string;
  /** Amount in the asset's ATOMIC units. USDC is 6dp, so "40000" == 0.04 USDC. */
  amount: string;
  /** Token contract the payment is denominated in. */
  asset: string;
  /** Where the money goes — the seller's receiving address. */
  payTo: string;
  /** Seconds the signed authorization stays valid. */
  maxTimeoutSeconds: number;
  /** Human description of what is being sold. */
  description?: string;
  /** EIP-712 domain bits the buyer needs to sign `transferWithAuthorization`. */
  extra?: {
    name?: string;
    version?: string;
  };
}

export interface PaymentChallenge {
  x402Version: 1 | 2;
  accepts: AcceptOption[];
  /** Present on rejection so the buyer can tell *why* a retry failed. */
  error?: string;
}

/** EIP-3009 `TransferWithAuthorization` message — what the buyer actually signs. */
export interface Authorization {
  from: string;
  to: string;
  value: string;
  validAfter: string;
  validBefore: string;
  /** 32-byte hex. Replay protection — the seller must reject a reused nonce. */
  nonce: string;
}

/** Decoded contents of the buyer's `X-PAYMENT` header. */
export interface PaymentPayload {
  x402Version: 1 | 2;
  scheme: 'exact';
  network: string;
  payload: {
    signature: string;
    authorization: Authorization;
  };
}

// ── transport helpers ──────────────────────────────────────────────────────

export function encodeBase64Json(value: unknown): string {
  return Buffer.from(JSON.stringify(value), 'utf8').toString('base64');
}

export function decodeBase64Json<T>(encoded: string): T | null {
  try {
    return JSON.parse(Buffer.from(encoded, 'base64').toString('utf8')) as T;
  } catch {
    return null;
  }
}

/**
 * Read a 402 challenge from either transport, header (v2) first.
 * Mirrors `circle-tools`' `readAccepts` so our buyer stays wire-compatible with
 * Circle's, and returns null when the response carries no challenge at all.
 */
export async function readChallenge(res: Response): Promise<PaymentChallenge | null> {
  const header = res.headers.get('payment-required');
  if (header) {
    const decoded = decodeBase64Json<PaymentChallenge>(header.trim());
    if (decoded && Array.isArray(decoded.accepts)) return decoded;
  }
  try {
    const body = (await res.json()) as PaymentChallenge;
    if (body && Array.isArray(body.accepts)) return body;
  } catch {
    /* no JSON body */
  }
  return null;
}

/** EIP-712 types for EIP-3009 `transferWithAuthorization` (the "exact" scheme). */
export const TRANSFER_WITH_AUTHORIZATION_TYPES = {
  TransferWithAuthorization: [
    { name: 'from', type: 'address' },
    { name: 'to', type: 'address' },
    { name: 'value', type: 'uint256' },
    { name: 'validAfter', type: 'uint256' },
    { name: 'validBefore', type: 'uint256' },
    { name: 'nonce', type: 'bytes32' },
  ],
} as const;

/** Format an atomic USDC amount (6dp) for humans. */
export function formatUsdc(atomic: string | bigint): string {
  const v = BigInt(atomic);
  const whole = v / 1_000_000n;
  const frac = (v % 1_000_000n).toString().padStart(6, '0');
  return `${whole}.${frac}`;
}
