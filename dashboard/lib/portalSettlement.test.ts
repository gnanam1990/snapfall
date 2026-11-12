import assert from 'node:assert/strict';
import test from 'node:test';

import {
  isSettlementPaused,
  parseAcceptance,
  portalBadge,
  portalPlan,
  portalView,
  settlementFromAcceptResponse,
  PORTAL_COPY,
  type AcceptanceRead,
  type PortalPlanInput,
  type PortalView,
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
  for (const settlement of ['done', 'SETTLED', 'UNKNOWN', 1, true, null, '']) {
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
  assert.deepEqual(portalBadge('unknown', 'accepted'), { label: 'Accepted', tone: '' });
  assert.deepEqual(portalBadge('open', 'delivery_ready'), { label: 'delivery_ready', tone: 'Delivered' });
  assert.deepEqual(portalBadge('open', null), { label: 'checking…', tone: '' });
});

// ── unknown: the daemon could not read the settlement (HTTP 200, not a 500) ─────────────────

test('unknown on an accepted job is its own view: no settled claim, no Accept, a re-read first', () => {
  const r = parseAcceptance({ stage: 'accepted', accepted: true, settlement: 'unknown' });
  assert.equal(r.settlement, 'unknown');
  assert.equal(portalView(r, null), 'unknown');
  // This page's own earlier POST that said pending or retry is older than this read; it does not decide.
  assert.equal(portalView(r, 'retry'), 'unknown');
  assert.equal(portalView(r, 'pending'), 'unknown');
  // A recorded settlement is final, so a read that later cannot tell does not undo it.
  assert.equal(portalView(r, 'settled'), 'settled');
  // A job that is not accepted is open, whatever the settlement read said.
  assert.equal(portalView(parseAcceptance({ stage: 'delivery_ready', accepted: false, settlement: 'unknown' }), null), 'open');
  // The POST accept may report it too.
  assert.equal(settlementFromAcceptResponse({ state: 'accepted-settled', settlement: 'unknown' }), 'unknown');

  const p = plan({ view: 'unknown' });
  assert.equal(p.copy, 'unknown');
  assert.equal(p.accept, null, 'nothing says an Accept would help');
  assert.deepEqual(p.checkAgain, { primary: true });
  assert.doesNotMatch(PORTAL_COPY.unknown, /is settled|accepted and settled|settled on chain/i);
  assert.match(PORTAL_COPY.unknown, /couldn’t read/);
  assert.deepEqual(p.badge, { label: 'Accepted', tone: '' }, 'never the settled tint');
});

// ── what the page says and offers ───────────────────────────────────────────────────────────

const plan = (over: Partial<PortalPlanInput>) =>
  portalPlan({ view: 'open', stage: 'accepted', statusUnavailable: false, selfCustody: false, chainStatus: null, paused: false, ...over });

const VIEWS: PortalView[] = ['open', 'settled', 'pending', 'retry', 'unknown', 'recorded'];

test('a failed status read says something true for every customer, and offers a re-read', () => {
  for (const view of VIEWS) {
    for (const selfCustody of [false, true]) {
      const p = plan({ view, selfCustody, statusUnavailable: true, chainStatus: 'Accepted' });
      assert.equal(p.copy, 'unavailable');
      assert.equal(p.accept, null);
      assert.equal(p.wallet, false);
      assert.deepEqual(p.checkAgain, { primary: true });
      assert.deepEqual(p.badge, { label: 'status unavailable', tone: '' });
    }
  }
  // The old copy said "Nothing has been accepted or settled", false for an accepted customer.
  assert.doesNotMatch(PORTAL_COPY.unavailable, /accepted|settled/i);
  assert.equal(
    PORTAL_COPY.unavailable,
    'We couldn’t read this job’s status right now. Nothing changes while this page is unavailable; try again in a moment.',
  );
});

test('only the settled view says settled; every other view leaves an action', () => {
  for (const view of VIEWS) {
    for (const selfCustody of [false, true]) {
      for (const chainStatus of [null, 'Delivered', 'Funded', 'Accepted']) {
        for (const paused of [false, true]) {
          const p = plan({ view, selfCustody, chainStatus, paused });
          const said = p.copy ? PORTAL_COPY[p.copy] : '';
          const where = JSON.stringify({ view, selfCustody, chainStatus, paused });
          if (p.copy === 'settled') assert.equal(view, 'settled', where);
          if (/accepted and settled/.test(said)) assert.equal(view, 'settled', where);
          if (p.copy?.startsWith('settledOnChain')) {
            assert.equal(chainStatus, 'Accepted', `${where}: an on-chain settlement claim the chain did not confirm`);
            assert.equal(selfCustody, true, where);
          }
          if (view === 'settled' || view === 'recorded') continue;
          assert.ok(p.wallet || p.accept || p.checkAgain, `${where}: the customer is left with nothing to do`);
        }
      }
    }
  }
});

test('custodial: retry offers the Accept, pending offers both, a paused job offers only the re-read', () => {
  assert.deepEqual(plan({ view: 'retry' }).accept, { label: 'Try settling again', busy: 'Settling…', primary: true });
  assert.deepEqual(plan({ view: 'retry' }).checkAgain, { primary: false });
  assert.equal(plan({ view: 'pending' }).accept?.primary, false);
  assert.deepEqual(plan({ view: 'pending' }).checkAgain, { primary: true });
  assert.equal(plan({ view: 'pending' }).copy, 'pendingRetrySafe');
  assert.deepEqual(plan({ view: 'open', stage: 'delivery_ready' }).accept, { label: 'Accept & settle', busy: 'Settling…', primary: true });
  assert.deepEqual(plan({ view: 'open', stage: 'delivery_ready' }).badge, { label: 'delivery_ready', tone: 'Delivered' });

  for (const view of ['open', 'pending', 'retry', 'unknown'] as const) {
    const p = plan({ view, paused: true });
    assert.equal(p.copy, 'paused', view);
    assert.equal(p.accept, null, `${view}: "Try settling again" would only meet the pause again`);
    assert.deepEqual(p.checkAgain, { primary: true });
    assert.deepEqual(p.badge, { label: 'Paused', tone: '' });
  }
  assert.equal(PORTAL_COPY.paused, 'Settlement is paused by the operator. Check again later.');
});

test('a 423 (or a FROZEN code) from POST accept is the pause, and nothing else is', () => {
  assert.equal(isSettlementPaused(423, { error: { code: 'FROZEN', message: 'the kill switch is engaged' } }), true);
  assert.equal(isSettlementPaused(423, null), true);
  assert.equal(isSettlementPaused(400, { error: { code: 'FROZEN' } }), true);
  for (const [status, body] of [
    [409, { error: { code: 'NOT_READY' } }],
    [500, { error: { code: 'INTERNAL' } }],
    [503, {}],
    [502, 'x'],
  ] as const) {
    assert.equal(isSettlementPaused(status, body), false, String(status));
  }
});

test('self-custody reads the chain: Accepted is settled on chain with a re-read, Delivered is the wallet', () => {
  // The daemon says retry both when the chain is still Delivered and when the chain already shows
  // Accepted and no record caught up. The old page showed a passkey sign-in in both cases.
  for (const view of ['pending', 'retry', 'unknown'] as const) {
    const onChain = plan({ view, selfCustody: true, chainStatus: 'Accepted' });
    assert.equal(onChain.copy, 'settledOnChain', view);
    assert.equal(onChain.wallet, false, `${view}: nothing left to sign`);
    assert.equal(onChain.accept, null);
    assert.deepEqual(onChain.checkAgain, { primary: true });
    assert.equal(plan({ view, selfCustody: true, chainStatus: 'Accepted', paused: true }).copy, 'settledOnChainPaused');

    const delivered = plan({ view, selfCustody: true, chainStatus: 'Delivered' });
    assert.equal(delivered.wallet, true, `${view}: the wallet's accept is what moves it`);
    assert.equal(delivered.accept, null, 'never the custodial Accept in self-custody');
    assert.deepEqual(delivered.checkAgain, { primary: false });
    // The kill switch stops the daemon, not the chain: the wallet still settles.
    assert.equal(plan({ view, selfCustody: true, chainStatus: 'Delivered', paused: true }).wallet, true);

    const unread = plan({ view, selfCustody: true, chainStatus: null });
    assert.equal(unread.wallet, true);
    assert.equal(unread.accept, null);
    assert.deepEqual(unread.checkAgain, { primary: true });
    assert.notEqual(unread.copy, 'retry', 'no escrow claim the chain was not read for');
  }
  assert.equal(plan({ view: 'retry', selfCustody: true, chainStatus: 'Delivered' }).copy, 'walletNotSettled');
  assert.equal(plan({ view: 'unknown', selfCustody: true, chainStatus: 'Delivered' }).copy, 'unknown');
  assert.equal(PORTAL_COPY.settledOnChain, 'Your payment is settled on chain; the record will update shortly.');

  // Open: the wallet, unless the chain shows it settled and the daemon never heard the Accept;
  // then recording it (the POST accept, which submits nothing on a settled job) is the way forward.
  assert.deepEqual(plan({ view: 'open', selfCustody: true, chainStatus: 'Delivered' }), {
    copy: null,
    badge: { label: 'accepted', tone: 'Delivered' },
    wallet: true,
    accept: null,
    checkAgain: null,
  });
  const unrecorded = plan({ view: 'open', selfCustody: true, chainStatus: 'Accepted' });
  assert.equal(unrecorded.copy, 'settledOnChainUnrecorded');
  assert.deepEqual(unrecorded.accept, { label: 'Record my acceptance', busy: 'Recording…', primary: true });
  assert.equal(unrecorded.wallet, false);
  assert.equal(plan({ view: 'open', selfCustody: true, chainStatus: 'Accepted', paused: true }).copy, 'settledOnChainPaused');
  // The chain's word is not taken outside self-custody: the daemon settles there.
  assert.equal(plan({ view: 'retry', chainStatus: 'Accepted' }).copy, 'retry');
});

test('older daemons keep working: recorded says nothing about settlement and offers no Accept', () => {
  const p = plan({ view: 'recorded' });
  assert.equal(p.copy, 'recorded');
  assert.equal(p.accept, null);
  assert.doesNotMatch(PORTAL_COPY.recorded, /accepted and settled/);
  assert.equal(plan({ view: 'settled' }).copy, 'settled');
  assert.deepEqual(plan({ view: 'settled' }).badge, { label: 'Accepted', tone: 'Accepted' });
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
