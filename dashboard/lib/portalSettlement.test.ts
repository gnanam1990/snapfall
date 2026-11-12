import assert from 'node:assert/strict';
import test from 'node:test';

import {
  parseAcceptance,
  portalBadge,
  portalView,
  settlementFromAcceptResponse,
  type AcceptanceRead,
} from './portalSettlement';
import { GET as customerAcceptance } from '../app/api/customer/[jobId]/acceptance/route';
import { POST as customerAccept } from '../app/api/customer/[jobId]/accept/route';

const read = (over: Partial<AcceptanceRead>): AcceptanceRead => ({
  stage: 'delivery_ready',
  accepted: false,
  vaultJobId: null,
  settlement: null,
  ...over,
});

test('the daemon-stated settlement decides the view, whatever `accepted` says', () => {
  const accepted = { stage: 'accepted', accepted: true };
  assert.equal(portalView(read({ settlement: 'none' }), null), 'open');
  assert.equal(portalView(read({ ...accepted, settlement: 'settled' }), null), 'settled');
  assert.equal(portalView(read({ ...accepted, settlement: 'pending' }), null), 'pending');
  assert.equal(portalView(read({ ...accepted, settlement: 'retry' }), null), 'retry');
});

test('accepted is not settled: a claimed job with a reverted or pending settlement keeps a way forward', () => {
  // The wedge: `accepted: true` on a job whose settlement reverted used to read as "accepted and
  // settled", with the Accept button gone and no way to trigger the daemon's retry.
  const wedged = parseAcceptance({ jobId: 'j1', stage: 'accepted', accepted: true, settlement: 'retry' });
  assert.equal(portalView(wedged, null), 'retry');
  // The read is newer than this page's own Accept: a POST that said settled does not override it.
  assert.equal(portalView(wedged, 'settled'), 'retry');
  assert.equal(portalView(parseAcceptance({ stage: 'accepted', accepted: true, settlement: 'pending' }), 'settled'), 'pending');
});

test('an older daemon (no settlement field): settled only on an accepted-settled POST, never on `accepted` alone', () => {
  const old = parseAcceptance({ jobId: 'j1', stage: 'accepted', accepted: true });
  assert.equal(old.settlement, null);
  assert.equal(portalView(old, null), 'recorded', 'a reload after accepting claims nothing about settlement');
  assert.equal(portalView(old, settlementFromAcceptResponse({ state: 'accepted-settled' })), 'settled');
  assert.equal(portalView(old, settlementFromAcceptResponse({ state: 'accepted-pending-chain' })), 'pending');
  assert.equal(portalView(old, settlementFromAcceptResponse({ state: 'accepted-settlement-reverted' })), 'retry');
  assert.equal(portalView(old, settlementFromAcceptResponse({ state: 'something-new' })), 'recorded');
  assert.equal(portalView(parseAcceptance({ stage: 'delivery_ready', accepted: false }), null), 'open');
});

test('the POST accept response: its settlement field first, then its state', () => {
  assert.equal(settlementFromAcceptResponse({ state: 'accepted-settled', settlement: 'pending' }), 'pending');
  assert.equal(settlementFromAcceptResponse({ state: 'accepted-pending-chain' }), 'pending');
  assert.equal(settlementFromAcceptResponse({ state: 'accepted-settlement-reverted' }), 'retry');
  assert.equal(settlementFromAcceptResponse({ state: 'accepted-settled' }), 'settled');
  for (const body of [{}, null, 'x', { state: 42 }, { settlement: 'maybe' }]) {
    assert.equal(settlementFromAcceptResponse(body), null, JSON.stringify(body));
  }
});

test('an unknown or malformed settlement value is not a claim: it falls back like an older daemon', () => {
  for (const settlement of ['done', 'SETTLED', 1, true, null, '']) {
    const r = parseAcceptance({ stage: 'accepted', accepted: true, settlement });
    assert.equal(r.settlement, null, String(settlement));
    assert.equal(portalView(r, null), 'recorded');
  }
  const junk = parseAcceptance('not an object');
  assert.deepEqual(junk, { stage: null, accepted: false, vaultJobId: null, settlement: null });
  assert.equal(parseAcceptance({ accepted: 'true' }).accepted, false, 'only a real boolean true is accepted');
  assert.equal(parseAcceptance({ vaultJobId: '' }).vaultJobId, null);
});

test('only the settled view wears the accepted tint', () => {
  assert.deepEqual(portalBadge('settled', 'accepted'), { label: 'Accepted', tone: 'Accepted' });
  assert.deepEqual(portalBadge('pending', 'accepted'), { label: 'Settling', tone: 'InProgress' });
  assert.deepEqual(portalBadge('retry', 'accepted'), { label: 'Not settled', tone: 'AwaitingFunding' });
  assert.deepEqual(portalBadge('recorded', 'accepted'), { label: 'Accepted', tone: '' });
  assert.deepEqual(portalBadge('open', 'delivery_ready'), { label: 'delivery_ready', tone: 'Delivered' });
  assert.deepEqual(portalBadge('open', null), { label: 'checking…', tone: '' });
});

// ── The proxy carries the field unchanged ────────────────────────────────────────────────────

const DAEMON = 'http://127.0.0.1:4010/api/v1';

async function withDaemon<T>(answer: string, fn: () => Promise<T>): Promise<T> {
  const env = process.env as Record<string, string | undefined>;
  const savedUrl = env.SNAPFALL_OWNER_API_URL;
  const savedFetch = globalThis.fetch;
  env.SNAPFALL_OWNER_API_URL = DAEMON;
  globalThis.fetch = (async () =>
    new Response(answer, { status: 200, headers: { 'content-type': 'application/json' } })) as typeof fetch;
  try {
    return await fn();
  } finally {
    globalThis.fetch = savedFetch;
    if (savedUrl === undefined) delete env.SNAPFALL_OWNER_API_URL;
    else env.SNAPFALL_OWNER_API_URL = savedUrl;
  }
}

test('the customer proxy routes pass the daemon settlement field through byte for byte', async () => {
  const params = { params: Promise.resolve({ jobId: 'job-1' }) };
  const auth = { authorization: 'Bearer act_FAKE_customer' };

  const status = '{"jobId":"job-1","stage":"accepted","accepted":true,"settlement":"retry"}';
  const got = await withDaemon(status, () =>
    customerAcceptance(new Request('http://dashboard.local/api/customer/job-1/acceptance', { headers: auth }), params),
  );
  assert.equal(await got.text(), status);

  const accepted = '{"jobId":"job-1","state":"accepted-settlement-reverted","settlement":"retry"}';
  const posted = await withDaemon(accepted, () =>
    customerAccept(new Request('http://dashboard.local/api/customer/job-1/accept', { method: 'POST', headers: auth }), params),
  );
  const body = await posted.json();
  assert.equal(body.settlement, 'retry');
  assert.equal(settlementFromAcceptResponse(body), 'retry');
});
