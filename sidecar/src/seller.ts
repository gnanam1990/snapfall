/**
 * Snapfall paid demo API — the x402 SELLER (PRD §14.3 C, FR-X402-001).
 *
 * Serves the three resources in the demo seed (PRD §15.2):
 *   /v1/company-profile     0.04 USDC — auto-approved under policy (AT-02)
 *   /v1/premium-dataset     4.00 USDC — over threshold, escalates, gets rejected (AT-03/AT-04)
 *   /v1/benchmark-summary   0.06 USDC — the cheaper alternative the agent adapts to
 *
 * Unpaid request  -> 402 + challenge on BOTH transports (header v2 + body v1).
 * Paid request    -> verifies the EIP-3009 authorization, then 200 + data + receipt.
 *
 * SETTLEMENT CAVEAT: this server verifies the buyer's signature and authorization
 * fields, but does NOT broadcast `transferWithAuthorization` on-chain — that is the
 * facilitator's job and needs a funded Arc testnet wallet we do not have yet. So the
 * loop below is cryptographically real end-to-end and financially a dry run. Wiring
 * settlement is the next step; see sidecar/README.md.
 */

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { verifyTypedData, type Address, type Hex } from 'viem';
import {
  encodeBase64Json,
  decodeBase64Json,
  formatUsdc,
  TRANSFER_WITH_AUTHORIZATION_TYPES,
  type AcceptOption,
  type PaymentChallenge,
  type PaymentPayload,
} from './x402.js';

const PORT = Number(process.env.PAID_API_PORT ?? 4021);

/** Arc testnet — docs.arc.io/arc/references/connect-to-arc (verified 19 Jul 2026). */
const CHAIN_ID = Number(process.env.ARC_CHAIN_ID ?? 5042002);
const NETWORK = `eip155:${CHAIN_ID}`;

/** USDC on Arc testnet. Placeholder until C confirms the token address. */
const USDC_ADDRESS = (process.env.ARC_USDC_ADDRESS ??
  '0x0000000000000000000000000000000000000001') as Address;

/** Where the seller wants to be paid. Overridden by env in the real demo. */
const PAY_TO = (process.env.SELLER_ADDRESS ??
  '0x000000000000000000000000000000000000dEaD') as Address;

const MAX_TIMEOUT_SECONDS = 300;

interface Resource {
  /** Atomic USDC (6dp). */
  price: bigint;
  description: string;
  /** Returned once payment verifies. Deliberately small, synthetic, non-sensitive. */
  data: unknown;
}

const RESOURCES: Record<string, Resource> = {
  '/v1/company-profile': {
    price: 40_000n, // 0.04 USDC
    description: 'Competitor company profile',
    data: {
      company: 'Cursor',
      category: 'AI coding assistant',
      founded: 2022,
      pricing: { free: true, proMonthlyUsd: 20 },
      positioning: 'IDE-native autocomplete and chat',
    },
  },
  '/v1/premium-dataset': {
    price: 4_000_000n, // 4.00 USDC — the one the founder rejects on camera
    description: 'Premium market dataset (full competitive landscape)',
    data: { rows: 12_400, note: 'full dataset payload elided in demo' },
  },
  '/v1/benchmark-summary': {
    price: 60_000n, // 0.06 USDC — the cheaper alternative
    description: 'Coding-assistant benchmark summary',
    data: {
      benchmark: 'SWE-bench Verified',
      results: [
        { product: 'Cursor', score: 0.62 },
        { product: 'Copilot', score: 0.55 },
        { product: 'Cody', score: 0.48 },
      ],
    },
  },
};

/**
 * Nonces already spent, per resource. Replay protection is the seller's job:
 * an authorization is a bearer instrument until it is consumed.
 * In-memory is fine for a demo; production needs shared storage.
 */
const spentNonces = new Set<string>();

function challengeFor(path: string, resource: Resource): PaymentChallenge {
  const accept: AcceptOption = {
    scheme: 'exact',
    network: NETWORK,
    amount: resource.price.toString(),
    asset: USDC_ADDRESS,
    payTo: PAY_TO,
    maxTimeoutSeconds: MAX_TIMEOUT_SECONDS,
    description: `${resource.description} (${formatUsdc(resource.price)} USDC)`,
    extra: { name: 'USD Coin', version: '2' },
  };
  return { x402Version: 2, accepts: [accept] };
}

function send(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  const json = JSON.stringify(body, null, 2);
  res.writeHead(status, {
    'content-type': 'application/json',
    'content-length': Buffer.byteLength(json),
    ...headers,
  });
  res.end(json);
}

/** 402 with the challenge on both transports. */
function sendChallenge(res: ServerResponse, challenge: PaymentChallenge, error?: string) {
  const body = error ? { ...challenge, error } : challenge;
  send(res, 402, body, { 'payment-required': encodeBase64Json(body) });
}

type Verdict = { ok: true } | { ok: false; reason: string };

async function verifyPayment(payment: PaymentPayload, resource: Resource): Promise<Verdict> {
  const { signature, authorization: a } = payment.payload;

  if (payment.scheme !== 'exact') return { ok: false, reason: `unsupported scheme ${payment.scheme}` };
  if (payment.network !== NETWORK) return { ok: false, reason: `wrong network ${payment.network}, expected ${NETWORK}` };

  // Amount must be EXACT — over-payment is as wrong as under-payment, because the
  // policy engine reserved a specific number against the job budget (FR-PAY-006).
  if (BigInt(a.value) !== resource.price) {
    return { ok: false, reason: `expected ${resource.price} atomic USDC, got ${a.value}` };
  }
  if (a.to.toLowerCase() !== PAY_TO.toLowerCase()) {
    return { ok: false, reason: `wrong payee ${a.to}` };
  }

  const now = BigInt(Math.floor(Date.now() / 1000));
  if (now < BigInt(a.validAfter)) return { ok: false, reason: 'authorization not yet valid' };
  if (now >= BigInt(a.validBefore)) return { ok: false, reason: 'authorization expired' };

  if (spentNonces.has(a.nonce)) return { ok: false, reason: 'nonce already spent (replay)' };

  // The actual cryptography: does this signature bind THIS authorization to `from`?
  const valid = await verifyTypedData({
    address: a.from as Address,
    domain: {
      name: 'USD Coin',
      version: '2',
      chainId: CHAIN_ID,
      verifyingContract: USDC_ADDRESS,
    },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: a.from as Address,
      to: a.to as Address,
      value: BigInt(a.value),
      validAfter: BigInt(a.validAfter),
      validBefore: BigInt(a.validBefore),
      nonce: a.nonce as Hex,
    },
    signature: signature as Hex,
  });
  if (!valid) return { ok: false, reason: 'signature does not recover to authorization.from' };

  return { ok: true };
}

const server = createServer(async (req: IncomingMessage, res: ServerResponse) => {
  const path = (req.url ?? '').split('?')[0] ?? '';

  if (path === '/health') return send(res, 200, { ok: true, network: NETWORK, payTo: PAY_TO });

  const resource = RESOURCES[path];
  if (!resource) return send(res, 404, { error: 'no such resource', available: Object.keys(RESOURCES) });

  const challenge = challengeFor(path, resource);
  const header = req.headers['x-payment'];

  // FR-X402-001: no payment presented -> advertise the price.
  if (typeof header !== 'string' || header.length === 0) {
    console.log(`[seller] 402 ${path} — ${formatUsdc(resource.price)} USDC`);
    return sendChallenge(res, challenge);
  }

  const payment = decodeBase64Json<PaymentPayload>(header);
  if (!payment?.payload?.authorization) {
    console.log(`[seller] 402 ${path} — malformed X-PAYMENT`);
    return sendChallenge(res, challenge, 'malformed X-PAYMENT header');
  }

  const verdict = await verifyPayment(payment, resource);
  if (!verdict.ok) {
    console.log(`[seller] 402 ${path} — rejected: ${verdict.reason}`);
    return sendChallenge(res, challenge, verdict.reason);
  }

  // Consume the nonce BEFORE handing over the goods, so a concurrent replay
  // of the same authorization cannot also be served.
  spentNonces.add(payment.payload.authorization.nonce);
  console.log(
    `[seller] 200 ${path} — paid ${formatUsdc(resource.price)} USDC by ${payment.payload.authorization.from}`,
  );

  // FR-X402-004: the receipt the buyer ties to its task + accounting category.
  const receipt = {
    resource: path,
    amount: payment.payload.authorization.value,
    asset: USDC_ADDRESS,
    network: NETWORK,
    payer: payment.payload.authorization.from,
    payee: PAY_TO,
    nonce: payment.payload.authorization.nonce,
    settlement: 'NOT_BROADCAST' as const, // see file header
  };

  return send(res, 200, { data: resource.data, receipt }, {
    'x-payment-response': encodeBase64Json(receipt),
  });
});

server.listen(PORT, () => {
  console.log(`[seller] Snapfall paid demo API on http://127.0.0.1:${PORT}`);
  console.log(`[seller] network ${NETWORK} · payTo ${PAY_TO}`);
  for (const [path, r] of Object.entries(RESOURCES)) {
    console.log(`[seller]   ${path} — ${formatUsdc(r.price)} USDC`);
  }
});
