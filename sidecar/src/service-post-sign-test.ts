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
 *      a v2 PAYMENT-RESPONSE settle header) is RECONCILING, never FAILED. The daemon releases on
 *      FAILED; the seller already holds a bearer authorization it can still settle.
 *   2. A pay that has been accepted but not yet written ahead (the seller's 402 probe is still
 *      running) is PAYMENT_IN_PROGRESS on /v1/status, never PAYMENT_NOT_FOUND, which the daemon
 *      reads as "nothing was or will be signed". And the approval is re-checked after the probe,
 *      so a probe that outlives the approval signs nothing.
 *
 *   npm run test:service-post-sign
 */
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { rmSync } from 'node:fs';
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { generatePrivateKey } from 'viem/accounts';
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
/** Paths whose 402 probe is held open until the test calls the release function. */
const stalls = new Map<string, Promise<void>>();

function receiptFor(nonce: string) {
  return { amount: PRICE, payer: '0xbuyer', payee: PAY_TO, nonce, settlement: 'NOT_BROADCAST' };
}

function challenge(asset = ASSET) {
  return {
    x402Version: 2,
    accepts: [
      {
        scheme: 'exact',
        network: NETWORK,
        amount: PRICE,
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
    if (path === '/hang') return; // never answers; the sidecar's probe bound must cut it off
    const stall = stalls.get(path);
    if (stall) await stall;
    const c = challenge(path === '/unsignable' ? '0xnot-an-address' : ASSET);
    return json(res, 402, c, { 'payment-required': b64(c) });
  }

  paid.set(path, (paid.get(path) ?? 0) + 1);
  const auth = (JSON.parse(Buffer.from(xp, 'base64').toString('utf8')) as { payload: { authorization: { nonce: string } } })
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
      return json(res, 200, { data: { ok: true } }, { 'x-payment-response': b64(receiptFor(auth.nonce)) });
    default: // control (/receipt, /stall): the Snapfall seller shape
      return json(res, 200, { data: { ok: true }, receipt: receiptFor(auth.nonce) });
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
  });
}

for (const [path, shape] of [
  ['/receipt', 'a Snapfall receipt in the body'],
  ['/v1hdr', 'a receipt in the v1 X-PAYMENT-RESPONSE header'],
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
  let release!: () => void;
  stalls.set('/stall', new Promise<void>((r) => (release = r)));
  const intent = makeIntent('/stall');
  const pid = paymentIdOf(intent);

  const pending = pay(intent);
  await sleep(300); // the sidecar has accepted it and is awaiting the probe

  for (let i = 0; i < 3; i++) {
    const st = await status(pid);
    assert.notEqual(st.json?.error?.code, 'PAYMENT_NOT_FOUND', 'status reported an in-flight pay as never received');
    assert.equal(st.status, 409, JSON.stringify(st.json));
    assert.equal(st.json?.error?.code, 'PAYMENT_IN_PROGRESS');
    assert.equal(st.json?.error?.retriable, true);
    await sleep(100);
  }
  assert.equal(paid.get('/stall') ?? 0, 0, 'nothing may be signed while the probe is outstanding');

  release();
  const r = await pending;
  assert.equal(r.status, 200, JSON.stringify(r.json));
  assert.equal(r.json?.state, 'DELIVERED');
  assert.equal((await status(pid)).json?.state, 'DELIVERED');
  stalls.delete('/stall');
});

test('a probe that outlives the approval signs nothing, and the id is then truly not found', async () => {
  let release!: () => void;
  stalls.set('/stall-expire', new Promise<void>((r) => (release = r)));
  const intent = makeIntent('/stall-expire', 1200); // approval lapses mid-probe
  const pid = paymentIdOf(intent);

  const pending = pay(intent);
  await sleep(300);
  assert.equal((await status(pid)).json?.error?.code, 'PAYMENT_IN_PROGRESS');
  await sleep(1300); // now past expiresAt, probe still held
  assert.equal((await status(pid)).json?.error?.code, 'PAYMENT_IN_PROGRESS', 'the pay is still in flight past expiry');

  release();
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
