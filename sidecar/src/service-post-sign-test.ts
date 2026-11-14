/**
 * The H3 sidecar's record of a payment whose outcome it cannot prove, exercised end to end.
 *
 * Boots the real sidecar (service.ts) as a child process against a fixture seller in THIS process,
 * so each test controls exactly what the seller answers and when, and counts how many times it
 * actually received X-PAYMENT. Loopback only, an ephemeral key, a throwaway store; nothing is
 * broadcast and no chain is contacted.
 *
 * Two properties, both read by the daemon to decide whether a budget hold may be released:
 *
 *   1. Once X-PAYMENT has been sent, a seller 200 without a usable receipt (plain body, null body,
 *      a v2 PAYMENT-RESPONSE settle header, an unresolved settlement, or a receipt that contradicts
 *      the signed authorization) is RECONCILING, never FAILED and never DELIVERED. The daemon
 *      releases on FAILED and books DELIVERED as spent; the seller holds a bearer authorization.
 *   2. A pay that has been accepted but not yet written ahead (the seller's 402 probe is still
 *      running) is PAYMENT_IN_PROGRESS on /v1/status, never PAYMENT_NOT_FOUND, which the daemon
 *      reads as "nothing was or will be signed". And the approval is re-checked after the probe,
 *      so a probe that outlives the approval signs nothing, and again after signing, so a signer
 *      that outlives the approval sends nothing.
 *
 *   npm run test:service-post-sign
 */
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFileSync, rmSync } from 'node:fs';
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import type { LocalAccount } from 'viem';
import { probeChallenge, provenNotSent, SendDeadlinePassed, signAndSubmit, type ApprovedIntent } from './buyer.js';
import { computeIntentHash, computePaymentId, signApproval, type ApprovalToken, type WireIntent } from './h3.js';

const FIXTURE_PORT = Number(process.env.POST_SIGN_FIXTURE_PORT ?? 4093);
const SIDECAR_PORT = Number(process.env.POST_SIGN_SIDECAR_PORT ?? 4094);
const FIXTURE = `http://127.0.0.1:${FIXTURE_PORT}`;
const SIDECAR = `http://127.0.0.1:${SIDECAR_PORT}`;
const CHAIN_ID = 5042002;
const NETWORK = `eip155:${CHAIN_ID}`;
const ASSET = '0x0000000000000000000000000000000000000001';
const PAY_TO = '0x000000000000000000000000000000000000dEaD';
const PRICE = '25000';
/** Probe bound handed to the sidecar: longer than any deliberate stall below except /hang. */
const PROBE_TIMEOUT_MS = 4000;

// Per-run test secrets, generated rather than written as literals.
const AUTH_TOKEN = `post-sign-token-${randomBytes(16).toString('hex')}`;
const H2_SECRET = `post-sign-h2-${randomBytes(16).toString('hex')}`;
const STORE_PATH = join(tmpdir(), `snapfall-post-sign-${randomBytes(4).toString('hex')}.json`);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// ── fixture seller ──────────────────────────────────────────────────────────────────────────

/** How many X-PAYMENT headers each path received: the "did a signature leave" ground truth. */
const paid = new Map<string, number>();
/** How many unpaid (402 probe) requests each path received: "the seller was not even asked". */
const probed = new Map<string, number>();
/** Paths whose 402 probe is held open until the test releases it, and tells the test it arrived. */
const stalls = new Map<string, { gate: Promise<void>; onArrive: () => void }>();

/** Reject if `p` has not settled within `ms`, so a barrier that never fires fails the test by name. */
function within<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`timed out after ${ms}ms waiting for ${what}`)), ms);
  });
  return Promise.race([p, timeout]).finally(() => clearTimeout(timer));
}

/**
 * Hold `path`'s 402 probe open. `arrived` resolves when the sidecar's probe actually reaches the
 * fixture -- a deterministic barrier: from then on the pay is provably accepted and awaiting the
 * probe, whatever the machine's load. A fixed sleep only guessed at that.
 */
function stall(path: string): { arrived: Promise<void>; release: () => void } {
  let release!: () => void;
  let onArrive!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  const arrived = new Promise<void>((r) => (onArrive = r));
  stalls.set(path, { gate, onArrive });
  return { arrived: within(arrived, 15_000, `the sidecar to probe ${path}`), release };
}

interface SignedAuth {
  from: string;
  to: string;
  value: string;
  nonce: string;
}

/** The Snapfall seller's receipt for exactly the authorization presented. */
function receiptFor(auth: SignedAuth, overrides: Record<string, string> = {}) {
  return { amount: auth.value, payer: auth.from, payee: auth.to, nonce: auth.nonce, settlement: 'NOT_BROADCAST', ...overrides };
}

/**
 * Probe paths whose challenge prices PRICE in a non-canonical spelling. Each one is the same
 * number to BigInt, so it passes every comparison against the approved intent; only the canonical
 * check stands between it and a signature whose receipt could never be accepted.
 */
const NON_CANONICAL_AMOUNTS: Record<string, unknown> = {
  '/amount-leading-zero': `0${PRICE}`,
  '/amount-space': ` ${PRICE}`,
  '/amount-plus': `+${PRICE}`,
  '/amount-hex': `0x${BigInt(PRICE).toString(16)}`,
  '/amount-number': Number(PRICE),
};

function challenge(asset = ASSET, amount: unknown = PRICE) {
  return {
    x402Version: 2,
    accepts: [
      {
        scheme: 'exact',
        network: NETWORK,
        amount,
        asset,
        payTo: PAY_TO,
        maxTimeoutSeconds: 300,
        description: 'fixture',
        extra: { name: 'USDC', version: '2' },
      },
    ],
  };
}

function json(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  const raw = JSON.stringify(body);
  res.writeHead(status, { 'content-type': 'application/json', ...headers });
  res.end(raw);
}

const b64 = (v: unknown) => Buffer.from(JSON.stringify(v)).toString('base64');

async function fixture(req: IncomingMessage, res: ServerResponse) {
  const path = (req.url ?? '').split('?')[0] ?? '';
  const xp = req.headers['x-payment'];
  if (typeof xp !== 'string') {
    probed.set(path, (probed.get(path) ?? 0) + 1);
    if (path === '/hang') return; // never answers; the sidecar's probe bound must cut it off
    const held = stalls.get(path);
    if (held) {
      held.onArrive();
      await held.gate;
    }
    const c = challenge(path === '/unsignable' ? '0xnot-an-address' : ASSET, NON_CANONICAL_AMOUNTS[path] ?? PRICE);
    return json(res, 402, c, { 'payment-required': b64(c) });
  }

  paid.set(path, (paid.get(path) ?? 0) + 1);
  const auth = (JSON.parse(Buffer.from(xp, 'base64').toString('utf8')) as { payload: { authorization: SignedAuth } })
    .payload.authorization;
  switch (path) {
    case '/plain': // N3 (a): a resource body, no receipt anywhere
      return json(res, 200, { result: 'ok' });
    case '/null': // N3 (b): a null body
      return json(res, 200, null);
    case '/v2hdr': // N3 (c): a standard x402 v2 seller, settlement in PAYMENT-RESPONSE
      return json(res, 200, { result: 'ok' }, {
        'payment-response': b64({ success: true, transaction: `0x${'ab'.repeat(32)}`, network: NETWORK, payer: '0xbuyer' }),
      });
    case '/garbage': // a body that does not parse (already reconciled before this fix; kept pinned)
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end('{not json');
    case '/v1hdr': // control: receipt only in the v1 X-PAYMENT-RESPONSE header
      return json(res, 200, { data: { ok: true } }, { 'x-payment-response': b64(receiptFor(auth)) });
    case '/txhash': // control: a broadcast receipt carrying a real-shaped transaction hash
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { settlement: `0x${'cd'.repeat(32)}` }) });
    case '/pending': // a receipt whose settlement is unresolved: money may or may not move
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { settlement: 'PENDING' }) });
    case '/made-up-settlement': // a settlement word nobody defined is not a resolution either
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { settlement: 'settled' }) });
    case '/wrong-amount': // a receipt for a different amount than was signed
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { amount: '1' }) });
    case '/wrong-payer': // a receipt naming somebody else as the payer
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { payer: `0x${'77'.repeat(20)}` }) });
    case '/wrong-payee': // a receipt naming a payee other than the signed and approved one
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth, { payee: `0x${'66'.repeat(20)}` }) });
    case '/conflict-hdr': // a correct body, contradicted by a header naming another nonce
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) }, {
        'x-payment-response': b64(receiptFor(auth, { nonce: `0x${'99'.repeat(32)}` })),
      });
    case '/text-v1hdr': // a non-JSON resource body; the receipt rides only in the v1 header
      res.writeHead(200, { 'content-type': 'text/plain', 'x-payment-response': b64(receiptFor(auth)) });
      return res.end('plain text, not json');
    case '/empty-v1hdr': // an empty body; the receipt rides only in the v1 header
      res.writeHead(200, { 'x-payment-response': b64(receiptFor(auth)) });
      return res.end();
    case '/v2meta-body': // control: a body receipt next to ordinary v2 settle metadata (not a receipt)
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) }, {
        'payment-response': b64({ success: true, transaction: `0x${'ab'.repeat(32)}`, network: NETWORK, payer: auth.from }),
      });
    case '/malformed-body-receipt': // a good header receipt, contradicted by a malformed body receipt
      return json(res, 200, { data: { ok: true }, receipt: { ...receiptFor(auth), nonce: 123 } }, {
        'x-payment-response': b64(receiptFor(auth)),
      });
    case '/string-body-receipt': // a good header receipt next to a body receipt that is not an object
      return json(res, 200, { data: { ok: true }, receipt: 'paid' }, { 'x-payment-response': b64(receiptFor(auth)) });
    case '/malformed-hdr-receipt': // a good body receipt next to a header receipt with a non-string settlement
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) }, {
        'x-payment-response': b64({ ...receiptFor(auth), settlement: 7 }),
      });
    case '/malformed-v2-receipt': // a good body receipt next to a v2 header that claims a receipt but lacks fields
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) }, {
        'payment-response': b64({ success: true, settlement: 'NOT_BROADCAST' }),
      });
    case '/undecodable-hdr': // a good body receipt next to an X-PAYMENT-RESPONSE that is not base64 JSON
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) }, { 'x-payment-response': '%%not-a-receipt%%' });
    default: // control (/receipt, /stall): the Snapfall seller shape
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth) });
  }
}

let seller: Server | undefined;
let sidecar: ChildProcess | undefined;

before(async () => {
  seller = createServer((req, res) => {
    fixture(req, res).catch(() => res.destroy());
  });
  await new Promise<void>((r) => seller!.listen(FIXTURE_PORT, '127.0.0.1', r));

  const env: NodeJS.ProcessEnv = { ...process.env };
  delete env.CIRCLE_TREASURY_WALLET_ID; // always the throwaway raw key below, never a Circle wallet
  sidecar = spawn(process.execPath, ['--import', 'tsx', fileURLToPath(new URL('./service.ts', import.meta.url))], {
    env: {
      ...env,
      SIDECAR_PORT: String(SIDECAR_PORT),
      SIDECAR_AUTH_TOKEN: AUTH_TOKEN,
      H2_APPROVAL_SECRET: H2_SECRET,
      TREASURY_PRIVATE_KEY: generatePrivateKey(),
      SIDECAR_STORE_PATH: STORE_PATH,
      SIDECAR_PROBE_TIMEOUT_MS: String(PROBE_TIMEOUT_MS),
      ARC_CHAIN_ID: String(CHAIN_ID),
    },
    stdio: 'ignore',
  });
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`${SIDECAR}/health`)).ok) return;
    } catch {
      /* not up yet */
    }
    await sleep(150);
  }
  throw new Error('sidecar never came up');
});

after(async () => {
  sidecar?.kill('SIGKILL');
  seller?.closeAllConnections();
  await new Promise<void>((r) => seller?.close(() => r()) ?? r());
  rmSync(STORE_PATH, { force: true });
  rmSync(`${STORE_PATH}.tmp`, { force: true });
});

// ── H3 client helpers ───────────────────────────────────────────────────────────────────────

function makeIntent(path: string, ttlMs = 300_000, asset = ASSET): WireIntent {
  const now = Date.now();
  return {
    intentId: `pi_${randomBytes(4).toString('hex')}`,
    jobId: 'job_post_sign',
    taskId: 'task_post_sign',
    agentId: 'post-sign-test',
    resource: `${FIXTURE}${path}`,
    network: NETWORK,
    asset,
    merchant: PAY_TO,
    amount: PRICE,
    maxAmount: PRICE,
    purpose: `post-sign ${path}`,
    nonce: `0x${randomBytes(32).toString('hex')}`,
    decision: 'AUTO_APPROVE',
    policyVersion: 'pol_1',
    createdAt: new Date(now).toISOString(),
    expiresAt: new Date(now + ttlMs).toISOString(),
  };
}

function makeToken(intent: WireIntent): ApprovalToken {
  const base = {
    intentHash: computeIntentHash(intent),
    decision: intent.decision,
    approvedAmount: intent.amount,
    approver: 'policy-engine',
    policyVersion: intent.policyVersion,
    issuedAt: new Date().toISOString(),
    expiresAt: intent.expiresAt,
  };
  return { ...base, signature: signApproval(H2_SECRET, base) };
}

const paymentIdOf = (intent: WireIntent) => computePaymentId(computeIntentHash(intent), intent.nonce);

async function api(method: string, path: string, body?: unknown): Promise<{ status: number; json: any }> {
  const res = await fetch(`${SIDECAR}${path}`, {
    method,
    headers: { 'content-type': 'application/json', authorization: `Bearer ${AUTH_TOKEN}` },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, json: await res.json().catch(() => null) };
}

const pay = (intent: WireIntent) => api('POST', '/v1/pay', { intent, approvalToken: makeToken(intent) });
const status = (paymentId: string) => api('GET', `/v1/status/${paymentId}`);

// ── 1. post-sign outcomes ───────────────────────────────────────────────────────────────────

for (const [path, shape] of [
  ['/plain', 'a 200 resource body with no receipt'],
  ['/null', 'a 200 null body'],
  ['/v2hdr', 'a 200 whose settlement is only in a v2 PAYMENT-RESPONSE header'],
  ['/garbage', 'a 200 body that does not parse'],
  ['/pending', 'a 200 receipt whose settlement is PENDING'],
  ['/made-up-settlement', 'a 200 receipt whose settlement is an undefined word'],
  ['/wrong-amount', 'a 200 receipt for a different amount than was signed'],
  ['/wrong-payer', 'a 200 receipt naming a different payer'],
  ['/wrong-payee', 'a 200 receipt naming a different payee'],
  ['/conflict-hdr', 'a 200 whose receipt header contradicts the body on the nonce'],
  ['/malformed-body-receipt', 'a 200 whose body receipt is malformed next to a good header receipt'],
  ['/string-body-receipt', 'a 200 whose body receipt is a string next to a good header receipt'],
  ['/malformed-hdr-receipt', 'a 200 whose header receipt is malformed next to a good body receipt'],
  ['/malformed-v2-receipt', 'a 200 whose v2 header claims a receipt but is incomplete'],
  ['/undecodable-hdr', 'a 200 whose X-PAYMENT-RESPONSE does not decode, next to a good body receipt'],
] as const) {
  test(`${shape} after X-PAYMENT is RECONCILING, never FAILED`, async () => {
    const intent = makeIntent(path);
    const r = await pay(intent);

    assert.equal(paid.get(path), 1, 'the fixture must have received the signed authorization exactly once');
    assert.equal(r.json?.error?.code, 'FACILITATOR_ERROR', `pay answered ${r.status} ${JSON.stringify(r.json)}`);
    assert.equal(r.status, 502);

    const st = await status(paymentIdOf(intent));
    assert.equal(st.status, 200);
    assert.equal(
      st.json?.state,
      'RECONCILING',
      `recorded ${st.json?.state}: the daemon releases the hold on FAILED while the seller holds a deliverable authorization`,
    );
    assert.equal(st.json?.terminal, false);
    assert.equal(st.json?.receipt, null, 'an unusable receipt must not be recorded as the payment receipt');
  });
}

for (const [path, shape] of [
  ['/receipt', 'a Snapfall receipt in the body'],
  ['/v1hdr', 'a receipt in the v1 X-PAYMENT-RESPONSE header'],
  ['/txhash', 'a receipt settled with a transaction hash'],
  ['/text-v1hdr', 'a header receipt with a non-JSON body'],
  ['/empty-v1hdr', 'a header receipt with an empty body'],
  ['/v2meta-body', 'a body receipt next to ordinary v2 settle metadata'],
] as const) {
  test(`control: ${shape} is still DELIVERED`, async () => {
    const intent = makeIntent(path);
    const r = await pay(intent);
    assert.equal(r.status, 200, JSON.stringify(r.json));
    assert.equal(r.json?.state, 'DELIVERED');
    assert.equal(r.json?.amountPaid, PRICE);
    const st = await status(paymentIdOf(intent));
    assert.equal(st.json?.state, 'DELIVERED');
  });
}

test('a signature that was never produced is the one post-write-ahead FAILED', async () => {
  // The challenge names an asset that is not an address. It matches the (equally bogus) approved
  // asset, so the pre-sign checks pass and the record is written ahead -- then the EIP-712 signer
  // refuses it. Nothing left the process, so FAILED (release) is the truth here.
  const intent = makeIntent('/unsignable', 300_000, '0xnot-an-address');
  const r = await pay(intent);
  assert.equal(paid.get('/unsignable') ?? 0, 0, 'an authorization reached the seller');
  assert.equal(r.status, 500, JSON.stringify(r.json));
  const st = await status(paymentIdOf(intent));
  assert.equal(st.json?.state, 'FAILED');
});

// ── 2. in-flight visibility and the expiry re-check ─────────────────────────────────────────

test('a pay waiting on the seller probe is PAYMENT_IN_PROGRESS on status, never PAYMENT_NOT_FOUND', async () => {
  const held = stall('/stall');
  const intent = makeIntent('/stall');
  const pid = paymentIdOf(intent);

  const pending = pay(intent);
  await held.arrived; // the sidecar has accepted it and its probe is now parked in the fixture

  for (let i = 0; i < 3; i++) {
    const st = await status(pid);
    assert.notEqual(st.json?.error?.code, 'PAYMENT_NOT_FOUND', 'status reported an in-flight pay as never received');
    assert.equal(st.status, 409, JSON.stringify(st.json));
    assert.equal(st.json?.error?.code, 'PAYMENT_IN_PROGRESS');
    assert.equal(st.json?.error?.retriable, true);
  }
  assert.equal(paid.get('/stall') ?? 0, 0, 'nothing may be signed while the probe is outstanding');

  held.release();
  const r = await pending;
  assert.equal(r.status, 200, JSON.stringify(r.json));
  assert.equal(r.json?.state, 'DELIVERED');
  assert.equal((await status(pid)).json?.state, 'DELIVERED');
  stalls.delete('/stall');
});

test('a probe that outlives the approval signs nothing, and the id is then truly not found', async () => {
  const held = stall('/stall-expire');
  const intent = makeIntent('/stall-expire', 2000); // approval lapses mid-probe
  const pid = paymentIdOf(intent);

  const pending = pay(intent);
  await held.arrived; // synchronisation: the probe is provably outstanding
  assert.equal((await status(pid)).json?.error?.code, 'PAYMENT_IN_PROGRESS');
  // The expiry wait, separate from the barrier: a clock condition, not a guess about the sidecar.
  await sleep(Math.max(0, Date.parse(intent.expiresAt) - Date.now()) + 100);
  assert.ok(Date.now() > Date.parse(intent.expiresAt), 'the approval has not lapsed yet');
  assert.equal((await status(pid)).json?.error?.code, 'PAYMENT_IN_PROGRESS', 'the pay is still in flight past expiry');

  held.release();
  const r = await pending;
  assert.equal(r.status, 410, JSON.stringify(r.json));
  assert.equal(r.json?.error?.code, 'APPROVAL_EXPIRED');
  assert.equal(r.json?.error?.paymentId, null);
  assert.equal(paid.get('/stall-expire') ?? 0, 0, 'an authorization was signed and sent after the approval expired');

  const st = await status(pid);
  assert.equal(st.status, 404);
  assert.equal(st.json?.error?.code, 'PAYMENT_NOT_FOUND', 'with no record and nothing in flight, not-found is now the truth');
  stalls.delete('/stall-expire');
});

// ── 3. the approval is re-checked between signing and sending ───────────────────────────────
//
// The service checks expiry before signing, but signing can await a remote signer (Circle). These
// drive buyer.signAndSubmit directly, in process, with a signer that only returns once the send
// deadline has passed -- the one window the sidecar child cannot be made to stall in -- against the
// same fixture seller, whose X-PAYMENT counter is the ground truth for "nothing was sent".

/** A real key whose signTypedData returns only after `until` (epoch ms): a slow remote signer. */
function slowSigner(until: number): LocalAccount {
  const inner = privateKeyToAccount(generatePrivateKey());
  return {
    ...inner,
    async signTypedData(args) {
      await sleep(Math.max(0, until - Date.now()) + 20);
      return inner.signTypedData(args);
    },
  } as LocalAccount;
}

function buyerIntentFor(path: string): ApprovedIntent {
  const w = makeIntent(path);
  return {
    intentId: w.intentId, jobId: w.jobId, taskId: w.taskId, agentId: w.agentId, resource: w.resource,
    maxAmount: BigInt(w.maxAmount), decision: 'AUTO_APPROVE', policyVersion: w.policyVersion,
    merchant: w.merchant, amount: BigInt(w.amount), asset: w.asset, network: w.network,
  };
}

test('an approval that lapses while the signer runs: signed, never sent, provably unsent', async () => {
  const intent = buyerIntentFor('/slow-sign');
  const accept = await probeChallenge(intent.resource, CHAIN_ID);
  const sendBy = Date.now() + 150; // live when signing starts, lapsed when it returns

  const err = await signAndSubmit(accept, intent, slowSigner(sendBy), CHAIN_ID, undefined, { sendBy }).then(
    () => assert.fail('an authorization signed after the approval lapsed was submitted'),
    (e: unknown) => e,
  );
  assert.ok(err instanceof SendDeadlinePassed, `want SendDeadlinePassed, got ${String(err)}`);
  assert.equal(provenNotSent(err), true, 'an unsent authorization must be releasable (FAILED), not held');
  assert.equal(paid.get('/slow-sign') ?? 0, 0, 'the X-PAYMENT left the process after the approval expired');
  assert.doesNotMatch((err as Error).message, /0x[0-9a-f]{130}/i, 'the discarded signature leaked into the error');
});

test('an unparseable send deadline fails closed', async () => {
  const intent = buyerIntentFor('/nan-deadline');
  const accept = await probeChallenge(intent.resource, CHAIN_ID);
  const account = privateKeyToAccount(generatePrivateKey());
  await assert.rejects(signAndSubmit(accept, intent, account, CHAIN_ID, undefined, { sendBy: NaN }), SendDeadlinePassed);
  assert.equal(paid.get('/nan-deadline') ?? 0, 0);
});

test('control: a deadline still in the future is sent and delivered', async () => {
  const intent = buyerIntentFor('/receipt-direct');
  const accept = await probeChallenge(intent.resource, CHAIN_ID);
  const account = privateKeyToAccount(generatePrivateKey());
  const r = await signAndSubmit(accept, intent, account, CHAIN_ID, undefined, { sendBy: Date.now() + 60_000 });
  assert.equal(paid.get('/receipt-direct'), 1);
  assert.equal(r.receipt.payer, account.address);
});

test('a seller that never answers the probe is cut off pre-sign by the probe bound', async () => {
  const intent = makeIntent('/hang');
  const started = Date.now();
  const r = await pay(intent);
  const took = Date.now() - started;
  assert.equal(r.json?.error?.code, 'UPSTREAM_UNREACHABLE', JSON.stringify(r.json));
  assert.ok(took < PROBE_TIMEOUT_MS + 2000, `the probe ran ${took}ms, past its ${PROBE_TIMEOUT_MS}ms bound`);
  assert.equal(paid.get('/hang') ?? 0, 0);
  assert.equal((await status(paymentIdOf(intent))).json?.error?.code, 'PAYMENT_NOT_FOUND');
});

// ── a non-canonical challenge amount is refused before anything is signed ─────────────────────

test('a challenge priced "0" + the approved amount is refused pre-sign, never signed into RECONCILING', async () => {
  // "025000" is 25000 to every BigInt comparison against the approved intent, so it used to be
  // signed as written. The receipt is then held to the canonical form and to the signed string,
  // so no receipt for it could ever be accepted: a signed bearer authorization stuck RECONCILING.
  const path = '/amount-leading-zero';
  const intent = makeIntent(path);
  const r = await pay(intent);
  assert.equal(r.json?.error?.code, 'CHALLENGE_UNAVAILABLE', `pay answered ${r.status} ${JSON.stringify(r.json)}`);
  assert.equal(r.status, 502);
  assert.equal(paid.get(path) ?? 0, 0, 'an authorization reached the seller');
  assert.equal(
    (await status(paymentIdOf(intent))).json?.error?.code,
    'PAYMENT_NOT_FOUND',
    'a refused challenge must leave no record: nothing was signed, so the daemon may release',
  );

  const q = await api('POST', '/v1/quote', { resource: `${FIXTURE}${path}`, chainId: CHAIN_ID });
  assert.equal(q.json?.error?.code, 'CHALLENGE_UNAVAILABLE', `quote answered ${q.status} ${JSON.stringify(q.json)}`);
});

test('every non-canonical spelling of the price is refused by the probe, before any key is used', async () => {
  for (const [path, amount] of Object.entries(NON_CANONICAL_AMOUNTS)) {
    await assert.rejects(
      probeChallenge(`${FIXTURE}${path}`, CHAIN_ID),
      (e: Error & { code?: string }) => e.code === 'CHALLENGE_UNAVAILABLE',
      `amount ${JSON.stringify(amount)} was accepted`,
    );
    assert.equal(paid.get(path) ?? 0, 0);
  }
  // Control: the canonical price is not refused by this check.
  assert.equal((await probeChallenge(`${FIXTURE}/receipt-probe`, CHAIN_ID)).amount, PRICE);
});

// ── 4. a PAYMENT_NOT_FOUND is a promise: a pay that arrives after it never signs ──────────────
//
// The daemon releases its hold on PAYMENT_NOT_FOUND. A pay still in transit at that moment (sent,
// not yet read by the sidecar) is neither recorded nor in flight, so status cannot see it. The
// sidecar therefore tombstones every id it reports not found, durably, and refuses a later pay for
// that id before anything is signed or the seller is even probed.

interface StoredEntry {
  kind?: string;
  paymentId?: string;
}
const storedEntries = (): StoredEntry[] => JSON.parse(readFileSync(STORE_PATH, 'utf8')) as StoredEntry[];
const tombstoned = (pid: string) =>
  storedEntries().some((e) => e.kind === 'PAYMENT_NOT_FOUND_TOMBSTONE' && e.paymentId === pid);

test('a pay that arrives after status answered PAYMENT_NOT_FOUND is refused pre-sign; the seller receives nothing', async () => {
  const path = '/late-pay';
  const intent = makeIntent(path);
  const pid = paymentIdOf(intent);

  const before = await status(pid);
  assert.equal(before.status, 404);
  assert.equal(before.json?.error?.code, 'PAYMENT_NOT_FOUND');
  assert.ok(tombstoned(pid), 'the not-found answer went out without a durable fence behind it');

  for (let i = 0; i < 2; i++) {
    const r = await pay(intent);
    assert.equal(r.status, 410, JSON.stringify(r.json));
    assert.equal(r.json?.error?.code, 'APPROVAL_EXPIRED', 'the refusal must be a code the daemon classifies as pre-sign');
    assert.equal(r.json?.error?.paymentId, null, 'no durable record exists, so the envelope carries no paymentId');
  }
  assert.equal(paid.get(path) ?? 0, 0, 'an authorization was sent for an id already reported not found');
  assert.equal(probed.get(path) ?? 0, 0, 'the seller was contacted for a fenced id');

  const after = await status(pid);
  assert.equal(after.json?.error?.code, 'PAYMENT_NOT_FOUND', 'not-found must stay the truth: nothing was written or signed');
});

test('status and pay racing on one id: a not-found answer and a signature never coexist', async () => {
  // Fired together, either the pay registers first (status says PAYMENT_IN_PROGRESS or reads the
  // record) or the tombstone does (the pay is refused). The one forbidden outcome is a 404 on an id
  // that was then signed.
  for (let i = 0; i < 12; i++) {
    const path = `/race-${i}`;
    const intent = makeIntent(path);
    const pid = paymentIdOf(intent);
    let st: Awaited<ReturnType<typeof status>>;
    let r: Awaited<ReturnType<typeof pay>>;
    if (i % 2 === 0) [st, r] = await Promise.all([status(pid), pay(intent)]);
    else [r, st] = await Promise.all([pay(intent), status(pid)]);
    const notFound = st.json?.error?.code === 'PAYMENT_NOT_FOUND';
    const signed = (paid.get(path) ?? 0) > 0;
    assert.ok(!(notFound && signed), `race ${i}: status said not found and the pay still signed (${JSON.stringify(r.json)})`);
    if (notFound) assert.equal(r.json?.error?.code, 'APPROVAL_EXPIRED', JSON.stringify(r.json));
    else assert.equal(r.json?.state, 'DELIVERED', JSON.stringify(r.json));
  }
});

test('a pay in flight is PAYMENT_IN_PROGRESS on status and is NOT fenced by it', async () => {
  const held = stall('/stall-fence');
  const intent = makeIntent('/stall-fence');
  const pid = paymentIdOf(intent);
  const pending = pay(intent);
  await held.arrived;
  const st = await status(pid);
  assert.equal(st.status, 409, JSON.stringify(st.json));
  assert.equal(st.json?.error?.code, 'PAYMENT_IN_PROGRESS');
  assert.equal(tombstoned(pid), false, 'an in-flight id was tombstoned');
  held.release();
  const r = await pending;
  assert.equal(r.status, 200, JSON.stringify(r.json));
  assert.equal(r.json?.state, 'DELIVERED');
  stalls.delete('/stall-fence');
});

test('a status for an id no pay could produce is not found and leaves no tombstone', async () => {
  const before = storedEntries().length;
  const st = await status('not-a-payment-id');
  assert.equal(st.json?.error?.code, 'PAYMENT_NOT_FOUND');
  assert.equal(storedEntries().length, before, 'a malformed id grew the durable store');
});
