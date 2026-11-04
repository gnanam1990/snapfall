/**
 * Circle Facilitator Service client: request shaping, the keyless seller proof, and the
 * settlement-honesty mapping. Offline — fetch is injected — so nothing here can mint a
 * settlement; the live wire contract is checked separately against Circle's /verify.
 *
 * The mutations that matter: treating `settlement_pending` (or a 409/5xx) as failure, which
 * invites a second charge; treating success without a hash as settled; and a seller proof
 * that does not recover to payTo over the exact body sent.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { keccak256, recoverTypedDataAddress, toBytes, type Hex } from 'viem';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';

import {
  CIRCLE_FACILITATOR_SERVICE_ENDPOINTS,
  CircleFacilitatorService,
  facilitatorServiceBody,
} from './circle-facilitator-service.js';
import type { PaymentRequirements } from './facilitator.js';
import { selectFacilitator } from './facilitator-select.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES, type PaymentPayload } from './x402.js';

const USDC = '0x3600000000000000000000000000000000000000' as const;
const sellerKey = generatePrivateKey();
const seller = privateKeyToAccount(sellerKey);
const TX = `0x${'ab'.repeat(32)}`;

const requirements: PaymentRequirements = {
  scheme: 'exact',
  network: 'eip155:5042',
  maxAmountRequired: '40000',
  resource: 'http://127.0.0.1:4021/premium',
  payTo: seller.address,
  asset: USDC,
  maxTimeoutSeconds: 300,
};

async function signedPayment(chainId = 5042): Promise<PaymentPayload> {
  const buyer = privateKeyToAccount(generatePrivateKey());
  const authorization = {
    from: buyer.address,
    to: seller.address,
    value: '40000',
    validAfter: '0',
    validBefore: String(Math.floor(Date.now() / 1000) + 600),
    nonce: `0x${'cd'.repeat(32)}`,
  };
  const signature = await buyer.signTypedData({
    domain: { ...USDC_EIP712_DOMAIN, chainId, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: buyer.address,
      to: seller.address,
      value: 40000n,
      validAfter: 0n,
      validBefore: BigInt(authorization.validBefore),
      nonce: authorization.nonce as Hex,
    },
  });
  return { x402Version: 2, scheme: 'exact', network: `eip155:${chainId}`, payload: { signature, authorization } };
}

type Captured = { url: string; headers: Record<string, string>; body: string };

function fakeFetch(status: number, json: unknown, captured: Captured[] = []) {
  return {
    captured,
    fetchImpl: async (url: string, init: RequestInit) => {
      captured.push({ url, headers: init.headers as Record<string, string>, body: String(init.body) });
      return new Response(JSON.stringify(json), { status, headers: { 'content-type': 'application/json' } });
    },
  };
}

test('the body is x402 v2 with accepted == paymentRequirements and the EIP-3009 extra', async () => {
  const payment = await signedPayment();
  const body = JSON.parse(facilitatorServiceBody(payment, requirements));
  assert.equal(body.x402Version, 2);
  assert.deepEqual(body.paymentPayload.accepted, body.paymentRequirements, 'the service rejects any difference');
  assert.equal(body.paymentRequirements.amount, '40000');
  assert.equal(body.paymentRequirements.network, 'eip155:5042');
  assert.equal(body.paymentRequirements.maxTimeoutSeconds, 60, 'capped: it is a wait window, not validity');
  assert.deepEqual(body.paymentRequirements.extra, { name: 'USDC', version: '2', assetTransferMethod: 'eip3009' });
  assert.equal(body.paymentPayload.resource.url, requirements.resource);
  assert.deepEqual(body.paymentPayload.payload, payment.payload);
});

test('keyless: the seller proof recovers to payTo over the exact body that was sent', async () => {
  const now = 1_800_000_000_000;
  const { fetchImpl, captured } = fakeFetch(200, { isValid: true, payer: '0x1' });
  const f = new CircleFacilitatorService({ apiKey: '', sellerPrivateKey: sellerKey, fetchImpl, now: () => now });
  const out = await f.verify(await signedPayment(), requirements);
  assert.equal(out.valid, true);

  const req = captured[0]!;
  assert.equal(req.url, CIRCLE_FACILITATOR_SERVICE_ENDPOINTS.verify);
  assert.equal(req.headers.authorization, undefined);
  const envelope = JSON.parse(Buffer.from(req.headers['facilitator-seller-proof']!, 'base64url').toString());
  assert.equal(envelope.version, 1);
  assert.equal(envelope.network, 'eip155:5042');
  assert.equal(envelope.payTo, seller.address);
  assert.equal(envelope.issuedAt, now / 1000);
  assert.equal(envelope.expiresAt - envelope.issuedAt, 300);

  const recovered = await recoverTypedDataAddress({
    domain: { name: 'Circle Facilitator Seller Request', version: '1', chainId: 5042 },
    types: {
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
    },
    primaryType: 'SellerRequest',
    message: {
      purpose: 'verify',
      method: 'POST',
      bodyHash: keccak256(toBytes(req.body)),
      network: envelope.network,
      payTo: envelope.payTo,
      nonce: envelope.nonce,
      issuedAt: BigInt(envelope.issuedAt),
      expiresAt: BigInt(envelope.expiresAt),
    },
    signature: envelope.signature,
  });
  assert.equal(recovered, seller.address);
});

test('a seller key that does not control payTo fails locally, before any request', async () => {
  const { fetchImpl, captured } = fakeFetch(200, { isValid: true });
  const f = new CircleFacilitatorService({ apiKey: '', sellerPrivateKey: generatePrivateKey(), fetchImpl });
  const out = await f.verify(await signedPayment(), requirements);
  assert.equal(out.valid, false);
  assert.match(out.reason ?? '', /not payTo/);
  assert.equal(captured.length, 0);
});

test('with an API key it sends a bearer token and no seller proof', async () => {
  const { fetchImpl, captured } = fakeFetch(200, { isValid: true });
  await new CircleFacilitatorService({ apiKey: 'k_test', sellerPrivateKey: sellerKey, fetchImpl }).verify(await signedPayment(), requirements);
  assert.equal(captured[0]!.headers.authorization, 'Bearer k_test');
  assert.equal(captured[0]!.headers['facilitator-seller-proof'], undefined);
});

test('verify maps isValid/invalidReason and HTTP errors to not-valid', async () => {
  const p = await signedPayment();
  const bad = await new CircleFacilitatorService({ apiKey: 'k', fetchImpl: fakeFetch(200, { isValid: false, invalidReason: 'insufficient_funds', payer: '0x9' }).fetchImpl }).verify(p, requirements);
  assert.deepEqual(bad, { valid: false, reason: 'insufficient_funds', payer: '0x9' });
  const unauth = await new CircleFacilitatorService({ apiKey: 'k', fetchImpl: fakeFetch(401, { message: 'unauthorized' }).fetchImpl }).verify(p, requirements);
  assert.equal(unauth.valid, false);
  assert.match(unauth.reason ?? '', /HTTP 401: unauthorized/);
});

test('settle: only success with a real hash is settled', async () => {
  const p = await signedPayment();
  const run = (status: number, json: unknown) =>
    new CircleFacilitatorService({ apiKey: 'k', fetchImpl: fakeFetch(status, json).fetchImpl }).settle(p, requirements);

  assert.deepEqual(await run(200, { success: true, transaction: TX, payer: '0x9', network: 'eip155:5042' }), { state: 'settled', txHash: TX, payer: '0x9' });
  assert.equal((await run(200, { success: true, transaction: '' })).state, 'unknown', 'success without a hash is not evidence');
  assert.equal((await run(200, { success: false, errorReason: 'settlement_pending', extensions: { 'settlement-status': { info: { paymentId: 'pay-1' } } } })).state, 'unknown');
  assert.deepEqual(await run(200, { success: false, errorReason: 'insufficient_funds' }), { state: 'rejected', reason: 'insufficient_funds' });
  assert.equal((await run(409, { message: 'conflict' })).state, 'unknown', 'may already have settled: reconcile');
  assert.equal((await run(503, {})).state, 'unknown');
  assert.equal((await run(400, { message: 'bad body' })).state, 'rejected');
});

test('settle: a request that never returns is unknown, never failed', async () => {
  const f = new CircleFacilitatorService({
    apiKey: 'k',
    fetchImpl: async () => {
      throw new Error('socket hang up');
    },
  });
  const out = await f.settle(await signedPayment(), requirements);
  assert.equal(out.state, 'unknown');
});

test('no credential is not-configured, and the endpoints cannot be redirected by env', async () => {
  const saved = { ...process.env };
  try {
    delete process.env.CIRCLE_API_KEY;
    delete process.env.SELLER_PRIVATE_KEY;
    process.env.SNAPFALL_FACILITATOR_TEST_ENDPOINTS = '1';
    process.env.CIRCLE_X402_SETTLE_URL = 'http://127.0.0.1:9/evil';
    const f = new CircleFacilitatorService();
    assert.equal(f.isConfigured(), false);
    assert.equal((await f.settle(await signedPayment(), requirements)).state, 'not-configured');
    assert.equal(f.endpoints().settle, 'https://api.circle.com/v1/facilitator/x402/settle');
  } finally {
    process.env = saved;
  }
});

test('selection: the service is opt-in, allowed on mainnet, and exclusive with self-relay', () => {
  assert.ok(selectFacilitator({ ARC_CHAIN_ID: '5042', SNAPFALL_CIRCLE_FACILITATOR_SERVICE: '1' }) instanceof CircleFacilitatorService);
  assert.ok(selectFacilitator({ SNAPFALL_CIRCLE_FACILITATOR_SERVICE: '1' }) instanceof CircleFacilitatorService);
  assert.throws(
    () => selectFacilitator({ SNAPFALL_CIRCLE_FACILITATOR_SERVICE: '1', SNAPFALL_SELF_FACILITATOR: '1' }),
    /choose one settlement path/,
  );
});
