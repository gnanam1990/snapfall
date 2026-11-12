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
import { createServer, type Server } from 'node:http';
import { after, before, test } from 'node:test';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import type { Address, Hex } from 'viem';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES } from './x402.js';

const PORT = Number(process.env.PAID_API_PORT ?? 4031);
const BASE = `http://127.0.0.1:${PORT}`;
const RESOURCE = '/v1/company-profile';
const CHAIN_ID = 5042002;
const NETWORK = `eip155:${CHAIN_ID}`;
const PAY_TO = '0x000000000000000000000000000000000000dEaD';
/** The seller's default USDC placeholder (seller.ts), i.e. the EIP-712 verifyingContract. */
const USDC = '0x0000000000000000000000000000000000000001';

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

/** The parent env minus anything that would select a facilitator or move the network, so each
 *  seller here runs exactly the configuration its tests assume, whatever the shell exports. */
function sellerEnv(extra: Record<string, string>): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const k of [
    'CIRCLE_API_KEY', 'SELLER_PRIVATE_KEY', 'SNAPFALL_SELF_FACILITATOR', 'SNAPFALL_CIRCLE_FACILITATOR_SERVICE',
    'SNAPFALL_FACILITATOR_TEST_ENDPOINTS', 'CIRCLE_X402_VERIFY_URL', 'CIRCLE_X402_SETTLE_URL',
    'ARC_TESTNET_CHAIN_ID', 'ARC_USDC_ADDRESS', 'SELLER_ADDRESS', 'PAID_API_HOST',
  ]) delete env[k];
  return { ...env, ARC_CHAIN_ID: String(CHAIN_ID), ...extra };
}

async function waitAlive(base: string): Promise<void> {
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`${base}/health`)).ok) return;
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error(`seller at ${base} never came up`);
}

before(async () => {
  // process.execPath, not `npx` under a shell: SIGKILL on a shell leaves the node grandchild
  // holding the port, and the next run cannot bind it.
  seller = spawn(process.execPath, ['--import', 'tsx', 'src/seller.ts'], {
    env: sellerEnv({ PAID_API_PORT: String(PORT) }),
    stdio: 'ignore',
  });
  await waitAlive(BASE);
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
  ['from is not an address', envelope({ ...okAuth, from: '0x1234' })],
  ['to has a bad EIP-55 checksum', envelope({ ...okAuth, to: '0x000000000000000000000000000000000000DEad' })],
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

// ── nonce canonicalisation (case-variant replay) ─────────────────────────────────────────────
//
// The replay and in-flight guard keyed on the raw nonce string while the signature and the chain
// read it as bytes32, so `0xAB..` and `0xab..` were one authorization under two keys: every
// re-casing of a spent nonce was delivered again, and k concurrent re-casings broadcast k times.

test('malformed nonce lengths and encodings are refused before the claim', async () => {
  const bad: Array<[string, string]> = [
    ['31 bytes', `0x${'ab'.repeat(31)}`],
    ['33 bytes', `0x${'ab'.repeat(33)}`],
    ['63 hex chars', `0x${'a'.repeat(63)}`],
    ['no 0x prefix', 'ab'.repeat(32)],
    ['upper-case 0X prefix', `0X${'ab'.repeat(32)}`],
    ['non-hex character', `0x${'ab'.repeat(31)}zz`],
    ['empty', ''],
    ['bare 0x', '0x'],
    ['trailing whitespace', `0x${'ab'.repeat(32)} `],
  ];
  for (const [name, nonce] of bad) {
    const res = await present(BASE, { ...okAuth, nonce }, `0x${'11'.repeat(65)}`);
    assert.equal(res.status, 402, `nonce ${name} answered ${res.status}, want 402`);
    assert.match(res.error ?? '', /nonce must be a 32-byte/, `nonce ${name} was not refused as malformed: ${res.error}`);
  }
  assert.ok(await alive(), 'the seller died on a malformed nonce');
});

test('a re-cased copy of a spent nonce is a replay, not a second delivery', async () => {
  const nonce = `0x${'ab'.repeat(30)}cdef` as Hex;
  const signed = await signAuthorization(nonce);

  const first = await present(BASE, signed.auth, signed.signature);
  assert.equal(first.status, 200, `the original authorization was not delivered: ${first.status} ${first.error}`);
  assert.equal(first.receiptNonce, nonce, 'the receipt must carry the canonical (lowercase) nonce');

  for (const variant of caseVariants(nonce)) {
    const again = await present(BASE, { ...signed.auth, nonce: variant }, signed.signature);
    assert.equal(again.status, 402, `case variant ${variant} was delivered again (HTTP ${again.status})`);
    assert.match(again.error ?? '', /replay/, `case variant ${variant} was not refused as a replay: ${again.error}`);
  }
});

test('an upper-cased nonce presented FIRST is canonicalised, so the lowercase one replays', async () => {
  const nonce = `0x${'9f'.repeat(31)}e1` as Hex;
  const signed = await signAuthorization(nonce);

  const upper = await present(BASE, { ...signed.auth, nonce: `0x${nonce.slice(2).toUpperCase()}` }, signed.signature);
  assert.equal(upper.status, 200, `an upper-case spelling of a valid nonce was refused: ${upper.status} ${upper.error}`);
  assert.equal(upper.receiptNonce, nonce, 'the receipt must carry the canonical nonce, not the spelling presented');

  const lower = await present(BASE, signed.auth, signed.signature);
  assert.equal(lower.status, 402);
  assert.match(lower.error ?? '', /replay/);
});

test('concurrent case variants of one authorization deliver once and broadcast once', async () => {
  // A facilitator-backed seller against a local stub that counts settle calls. The stub holds each
  // settle open long enough for every concurrent request to reach the claim while the first is in
  // flight, which is exactly the window the raw-string key left open.
  const facPort = PORT + 2;
  const sellerPort = PORT + 1;
  let settles = 0;
  let verifies = 0;
  const fac: Server = createServer((req, res) => {
    let raw = '';
    req.on('data', (c) => (raw += c));
    req.on('end', () => {
      const isSettle = (req.url ?? '').includes('settle');
      if (isSettle) settles++;
      else verifies++;
      const body = isSettle ? { success: true, transaction: `0x${'cd'.repeat(32)}` } : { isValid: true };
      setTimeout(() => {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify(body));
      }, isSettle ? 400 : 0);
    });
  });
  await new Promise<void>((r) => fac.listen(facPort, '127.0.0.1', r));
  const base = `http://127.0.0.1:${sellerPort}`;
  const settled = spawn(process.execPath, ['--import', 'tsx', 'src/seller.ts'], {
    env: sellerEnv({
      PAID_API_PORT: String(sellerPort),
      // Generated, never a literal: a test value, and the facilitator is a loopback stub.
      CIRCLE_API_KEY: `hostile-${Math.random().toString(16).slice(2, 10)}`,
      SNAPFALL_FACILITATOR_TEST_ENDPOINTS: '1',
      CIRCLE_X402_VERIFY_URL: `http://127.0.0.1:${facPort}/verify`,
      CIRCLE_X402_SETTLE_URL: `http://127.0.0.1:${facPort}/settle`,
    }),
    stdio: 'ignore',
  });
  try {
    await waitAlive(base);
    const nonce = `0x${'ab'.repeat(30)}cdef` as Hex;
    const signed = await signAuthorization(nonce);
    const variants = [nonce, ...caseVariants(nonce)];

    const results = await Promise.all(variants.map((n) => present(base, { ...signed.auth, nonce: n }, signed.signature)));

    // A redirected (non-Circle) facilitator never counts as settled, so the one request that won
    // the claim ends HELD (settlement unknown, goods withheld) and every other one was turned away
    // while it was in flight. Raw-keyed, all five won a claim and all five reached settle.
    assert.equal(settles, 1, `one authorization was broadcast ${settles} times: ${JSON.stringify(results)}`);
    assert.equal(verifies, 1, `one authorization was sent to facilitator verify ${verifies} times`);
    assert.ok(results.every((r) => r.status === 402), `goods released without a confirmed settlement: ${JSON.stringify(results)}`);
    const held = results.filter((r) => /held for you/.test(r.error ?? ''));
    const inFlight = results.filter((r) => /already being settled/.test(r.error ?? ''));
    assert.equal(held.length, 1, `want exactly one claim to reach settle: ${JSON.stringify(results)}`);
    assert.equal(inFlight.length, variants.length - 1, `want every other variant refused as in flight: ${JSON.stringify(results)}`);
    assert.match(held[0]?.error ?? '', new RegExp(`reference ${nonce}`), 'the hold must name the canonical nonce');

    // Afterwards, every spelling resolves to the SAME held record and none is resubmitted.
    for (const n of variants) {
      const again = await present(base, { ...signed.auth, nonce: n }, signed.signature);
      assert.equal(again.status, 402);
      assert.match(again.error ?? '', /held for you/, `variant ${n} was not answered from the hold: ${again.error}`);
    }
    assert.equal(settles, 1, `a held authorization was re-broadcast (${settles} settles)`);
  } finally {
    settled.kill('SIGKILL');
    await new Promise<void>((r) => fac.close(() => r()));
  }
});

/** Upper, mixed and alternating spellings of the same bytes32. */
function caseVariants(nonce: Hex): string[] {
  const hex = nonce.slice(2);
  return [
    `0x${hex.toUpperCase()}`,
    `0x${hex.slice(0, -4)}${hex.slice(-4).toUpperCase()}`,
    `0x${[...hex].map((c, i) => (i % 2 ? c.toUpperCase() : c)).join('')}`,
    `0x${[...hex].map((c, i) => (i % 2 ? c : c.toUpperCase())).join('')}`,
  ];
}

/** A REAL EIP-3009 signature from a throwaway key, over the seller's own domain and price. */
async function signAuthorization(nonce: Hex) {
  const account = privateKeyToAccount(generatePrivateKey());
  const auth = {
    from: account.address as string,
    to: PAY_TO,
    value: '40000',
    validAfter: '0',
    validBefore: String(Math.floor(Date.now() / 1000) + 300),
    nonce: nonce as string,
  };
  const signature = await account.signTypedData({
    domain: { name: USDC_EIP712_DOMAIN.name, version: USDC_EIP712_DOMAIN.version, chainId: CHAIN_ID, verifyingContract: USDC as Address },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: account.address,
      to: PAY_TO as Address,
      value: 40_000n,
      validAfter: 0n,
      validBefore: BigInt(auth.validBefore),
      nonce,
    },
  });
  return { auth, signature };
}

async function present(base: string, authorization: unknown, signature: string) {
  const header = Buffer.from(JSON.stringify(envelope(authorization, signature))).toString('base64');
  const res = await fetch(`${base}${RESOURCE}`, { headers: { 'X-PAYMENT': header } });
  const body = (await res.json()) as { error?: string; receipt?: { nonce?: string } };
  return { status: res.status, error: body.error, receiptNonce: body.receipt?.nonce };
}
