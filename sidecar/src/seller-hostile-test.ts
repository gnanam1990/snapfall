/**
 * The seller must answer a malformed payment, not die of it.
 *
 * Every case below used to kill the process. `createServer(async handler)` with no try/catch makes
 * any throw inside the handler an unhandled rejection, and Node exits: the client sees ECONNRESET,
 * the seller is gone, and the demo is over. Reproduced at ten for ten, one fresh process per case.
 *
 * The two that matter most are not malformed JSON at all. `value` sent as a JSON number instead of
 * a string is an honest client mistake, and a signature that does not recover makes viem THROW
 * ("Invalid yParityOrV value") rather than return false -- a well-formed payment with a bad
 * signature, which is the single most likely thing a real client gets wrong.
 *
 * Runs against the real seller over HTTP rather than calling verifyPayment directly, because the
 * defect was in the handler wiring and a unit test of the verifier would have passed throughout.
 */
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { after, before, test } from 'node:test';

const PORT = Number(process.env.PAID_API_PORT ?? 4031);
const BASE = `http://127.0.0.1:${PORT}`;
const RESOURCE = '/v1/company-profile';
const NETWORK = process.env.SNAPFALL_NETWORK ?? 'eip155:5042002';
const PAY_TO = '0x000000000000000000000000000000000000dEaD';

let seller: ChildProcess | undefined;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function alive(): Promise<boolean> {
  try {
    const r = await fetch(`${BASE}/health`);
    return r.ok;
  } catch {
    return false;
  }
}

before(async () => {
  seller = spawn('npx', ['tsx', 'src/seller.ts'], {
    env: { ...process.env, PAID_API_PORT: String(PORT), PAY_TO_ADDRESS: PAY_TO },
    shell: true,
    stdio: 'ignore',
  });
  for (let i = 0; i < 80; i++) {
    if (await alive()) return;
    await sleep(250);
  }
  throw new Error('seller never came up');
});

after(() => {
  seller?.kill('SIGKILL');
});

const okAuth = {
  value: '40000',
  from: `0x${'11'.repeat(20)}`,
  to: PAY_TO,
  validAfter: '0',
  validBefore: '99999999999',
  nonce: `0x${'22'.repeat(32)}`,
};

const envelope = (authorization: unknown, signature = `0x${'11'.repeat(65)}`) => ({
  x402Version: 2,
  scheme: 'exact',
  network: NETWORK,
  payload: { signature, authorization },
});

/** Every one of these killed the process before the shape guard and the handler try/catch. */
const HOSTILE: Array<[name: string, body: unknown]> = [
  ['value as a JSON number', envelope({ ...okAuth, value: 40000 })],
  ['value missing', envelope({ ...okAuth, value: undefined })],
  ['value not numeric', envelope({ ...okAuth, value: 'abc' })],
  ['value with a decimal point', envelope({ ...okAuth, value: '0.04' })],
  ['validAfter not numeric', envelope({ ...okAuth, validAfter: 'soon' })],
  ['validBefore missing', envelope({ ...okAuth, validBefore: undefined })],
  ['to missing', envelope({ ...okAuth, to: undefined })],
  ['authorization is a string', envelope('nope')],
  ['authorization is null', envelope(null)],
  ['signature does not recover', envelope(okAuth)],
  ['signature too short', envelope(okAuth, '0xdead')],
  ['nonce is a number', envelope({ ...okAuth, nonce: 42 })],
];

for (const [name, body] of HOSTILE) {
  test(`survives: ${name}`, async () => {
    const header = Buffer.from(JSON.stringify(body)).toString('base64');

    const res = await fetch(`${BASE}${RESOURCE}`, { headers: { 'X-PAYMENT': header } });

    // 402 with a reason is the correct answer to a payment that cannot be verified. Not 500:
    // the client sent something wrong, the server did not fail.
    assert.equal(res.status, 402, `${name} answered ${res.status}, want 402 with a reason`);
    const payload = (await res.json()) as { error?: string };
    assert.ok(payload.error, `${name} produced a 402 with no reason for the client to act on`);

    // The point of the whole file.
    assert.ok(await alive(), `the seller DIED on: ${name}`);
  });
}

test('the seller still answers a well-formed challenge afterwards', async () => {
  // Proves the hardening rejects bad input without wedging the happy path: after twelve hostile
  // requests the seller must still issue a normal 402 challenge to an unpaid request.
  const res = await fetch(`${BASE}${RESOURCE}`);
  assert.equal(res.status, 402);
  const challenge = (await res.json()) as { accepts?: Array<{ payTo?: string; amount?: string }> };
  // if-throw rather than assert.ok: assert does not narrow the type across statements.
  // if-throw rather than assert.ok, which does not narrow across statements; and the element is
  // checked separately because noUncheckedIndexedAccess makes accepts[0] optional too.
  const first = challenge.accepts?.[0];
  if (!first) throw new Error('no accepts array in the challenge');
  assert.equal(first.amount, '40000', 'the 0.04 price changed');
});
