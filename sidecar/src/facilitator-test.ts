/**
 * The facilitator client, tested offline.
 *
 * What these CAN prove: request shaping, response parsing, and above all the safety property --
 * that no failure mode produces a settlement string which `capture-v1-fixture.ts` would accept as
 * evidence. That gate is the only thing between a fabricated string and a committed claim that a
 * payment happened, so it is tested harder than the happy path.
 *
 * What these CANNOT prove: that Circle's wire contract is what this code assumes. The field names
 * and status semantics are written from the documented x402 facilitator interface and have never
 * been exercised against the live service, because that needs V3's credentials. The first live
 * run is the real test, and it should be treated as one.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  CircleFacilitator,
  NOT_BROADCAST,
  PENDING_SETTLEMENT,
  settlementFor,
  type PaymentRequirements,
  type SettleOutcome,
} from './facilitator.js';
import { CIRCLE_TESTNET_FACILITATOR_ENDPOINTS } from './circle-facilitator-fixture.js';

const TX = `0x${'ab'.repeat(32)}`;

const REQ: PaymentRequirements = {
  scheme: 'exact',
  network: 'eip155:5042002',
  maxAmountRequired: '40000',
  resource: '/v1/company-profile',
  payTo: '0x000000000000000000000000000000000000dEaD',
  asset: '0x3600000000000000000000000000000000000000',
};

/** A fetch double. It returns SHAPES; it cannot mint a settlement the seller would trust. */
function fakeFetch(status: number, body: unknown, capture?: { url?: string; init?: RequestInit }) {
  return async (url: string, init: RequestInit): Promise<Response> => {
    if (capture) {
      capture.url = url;
      capture.init = init;
    }
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'content-type': 'application/json' },
    });
  };
}

const withKey = (fetchImpl: ReturnType<typeof fakeFetch>) =>
  new CircleFacilitator({ apiKey: 'test-key', fetchImpl });

// ── the safety property ─────────────────────────────────────────────────────

test('ONLY a real transaction hash is ever reported as settled', () => {
  // Every non-settled state must map to a marker capture-v1-fixture.ts refuses. If any of these
  // returned something 0x-64-shaped, a fixture could be written for a payment that never landed.
  const notSettled: SettleOutcome[] = [
    { state: 'rejected', reason: 'declined' },
    { state: 'unknown', reason: 'timeout' },
    { state: 'not-configured' },
  ];
  for (const outcome of notSettled) {
    const value = settlementFor(outcome);
    assert.ok(
      !/^0x[0-9a-fA-F]{64}$/.test(value),
      `${outcome.state} produced a transaction-hash-shaped settlement (${value}); the fixture gate would accept it`,
    );
  }
  assert.equal(settlementFor({ state: 'settled', txHash: TX }), TX);
});

test('an unknown outcome is PENDING, distinct from NOT_BROADCAST', () => {
  // They are both refused by the fixture gate, and they mean different things to an operator:
  // NOT_BROADCAST is "nothing was attempted", PENDING is "money may have moved, go and look".
  assert.equal(settlementFor({ state: 'unknown', reason: 'x' }), PENDING_SETTLEMENT);
  assert.equal(settlementFor({ state: 'not-configured' }), NOT_BROADCAST);
  assert.notEqual(PENDING_SETTLEMENT, NOT_BROADCAST);
});

test('success without a usable transaction hash is UNKNOWN, not settled', async () => {
  // The dangerous case: the facilitator says it worked but gives nothing to verify. A settlement
  // string that cannot be opened on an explorer is not evidence.
  for (const body of [
    { success: true },
    { success: true, transaction: '' },
    { success: true, transaction: 'not-a-hash' },
    { success: true, transaction: '0xdeadbeef' },
  ]) {
    const out = await withKey(fakeFetch(200, body)).settle({}, REQ);
    assert.equal(out.state, 'unknown', `body ${JSON.stringify(body)} was treated as ${out.state}`);
  }
});

test('a transport failure after submitting is UNKNOWN, never a failure', async () => {
  // settle() has left the process; the authorization may land regardless. Calling this a failure
  // is what lets a caller release budget behind a payment that settles later.
  const boom = async () => {
    throw new Error('socket hang up');
  };
  const out = await new CircleFacilitator({ apiKey: 'k', fetchImpl: boom }).settle({}, REQ);
  assert.equal(out.state, 'unknown');
});

test('a 5xx is unknown but a 4xx is a clean rejection', async () => {
  // A 4xx is the facilitator declining before broadcast, so nothing moved. A 5xx may be a failure
  // AFTER it already submitted, which cannot be distinguished from outside.
  const server = await withKey(fakeFetch(503, { message: 'upstream down' })).settle({}, REQ);
  assert.equal(server.state, 'unknown');

  const client = await withKey(fakeFetch(400, { errorReason: 'bad authorization' })).settle({}, REQ);
  assert.equal(client.state, 'rejected');
  assert.match((client as { reason: string }).reason, /bad authorization/);
});

test('an explicit success:false is a rejection even on HTTP 200', async () => {
  const out = await withKey(fakeFetch(200, { success: false, errorReason: 'insufficient balance' })).settle({}, REQ);
  assert.equal(out.state, 'rejected');
  assert.match((out as { reason: string }).reason, /insufficient balance/);
});

// ── configuration ───────────────────────────────────────────────────────────

test('with no API key nothing is attempted', async () => {
  let called = false;
  const f = new CircleFacilitator({
    apiKey: '',
    fetchImpl: async () => {
      called = true;
      return new Response('{}');
    },
  });
  assert.equal(f.isConfigured(), false);
  assert.deepEqual(await f.settle({}, REQ), { state: 'not-configured' });
  assert.equal(called, false, 'a request was made without a key; an attempted-and-failed broadcast is a worse claim than none');
});

test('the default endpoints are the ones AT-18 pins', () => {
  // If these drift, the V1 fixture stops validating and the submission loses its Circle claim.
  const f = new CircleFacilitator({ apiKey: 'k' });
  assert.deepEqual(f.endpoints(), {
    verify: CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.verify,
    settle: CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.settle,
  });
});

// ── request shaping ─────────────────────────────────────────────────────────

test('the settle request carries the bearer key and the x402 envelope', async () => {
  const cap: { url?: string; init?: RequestInit } = {};
  await withKey(fakeFetch(200, { success: true, transaction: TX }, cap)).settle({ scheme: 'exact' }, REQ);

  assert.equal(cap.url, CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.settle);
  const headers = cap.init?.headers as Record<string, string>;
  assert.equal(headers.authorization, 'Bearer test-key');
  assert.equal(headers['content-type'], 'application/json');

  const body = JSON.parse(String(cap.init?.body)) as Record<string, unknown>;
  assert.equal(body.x402Version, 1, 'the fixture contract pins x402Version 1');
  assert.deepEqual(body.paymentRequirements, REQ);
  assert.deepEqual(body.paymentPayload, { scheme: 'exact' });
});

test('a verified settle returns the hash and the payer', async () => {
  const out = await withKey(fakeFetch(200, { success: true, transaction: TX, payer: '0xabc' })).settle({}, REQ);
  assert.deepEqual(out, { state: 'settled', txHash: TX, payer: '0xabc' });
});

test('verify reports the facilitator reason rather than a generic failure', async () => {
  const ok = await withKey(fakeFetch(200, { isValid: true, payer: '0xabc' })).verify({}, REQ);
  assert.equal(ok.valid, true);
  assert.equal(ok.payer, '0xabc');

  const bad = await withKey(fakeFetch(200, { isValid: false, invalidReason: 'expired authorization' })).verify({}, REQ);
  assert.equal(bad.valid, false);
  assert.match(bad.reason ?? '', /expired authorization/);
});

test('verify failing unreachable does not read as valid', async () => {
  const boom = async () => {
    throw new Error('dns');
  };
  const out = await new CircleFacilitator({ apiKey: 'k', fetchImpl: boom }).verify({}, REQ);
  assert.equal(out.valid, false, 'an unreachable verify must never let a settle proceed');
});
