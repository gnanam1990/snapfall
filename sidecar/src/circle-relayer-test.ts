/**
 * The self-facilitator with a Circle developer-controlled wallet as its relayer, against a fake
 * Circle API that answers contract executions the way the real one does: an id at once, then a
 * transaction whose state moves on each read. No chain: the receipt status is injected, which is
 * what lets "Circle said COMPLETE but the call reverted" be asserted at all.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { generateKeyPairSync, privateDecrypt, constants } from 'node:crypto';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import { decodeFunctionData, getAddress, parseAbi, type Hex } from 'viem';

import { SelfFacilitator } from './self-facilitator.js';
import type { CircleWalletConfig } from './circle-wallet.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES, type PaymentPayload } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import type { PaymentRequirements } from './facilitator.js';

// THROWAWAY values, never real credentials.
const API_KEY = 'INERT:not-a-circle-key:inert-fake-api-secret';
const SECRET = 'ab'.repeat(32); // 32 bytes, deliberately patterned so no scanner reads it as a key
const WALLET_ID = '3c2b1a09-8f7e-4d6c-9b5a-4f3e2d1c0b9a';
const RELAYER = '0x00000000000000000000000000000000000000aa';
const CHAIN_ID = 5042002;
const USDC = '0x3600000000000000000000000000000000000000' as const;
const PAYEE = '0x000000000000000000000000000000000000fee1' as const;
const HASH = `0x${'c1'.repeat(32)}` as Hex;

const requirements: PaymentRequirements = {
  scheme: 'exact', network: `eip155:${CHAIN_ID}`, maxAmountRequired: '40000',
  resource: '/premium', payTo: PAYEE, asset: USDC,
};

const TWA = parseAbi([
  'function transferWithAuthorization(address from, address to, uint256 value, uint256 validAfter, uint256 validBefore, bytes32 nonce, uint8 v, bytes32 r, bytes32 s)',
]);

async function signedPayment(): Promise<PaymentPayload> {
  const account = privateKeyToAccount(generatePrivateKey());
  const authorization = {
    from: account.address, to: PAYEE, value: '40000', validAfter: '0',
    validBefore: String(Math.floor(Date.now() / 1000) + 3600), nonce: `0x${'ab'.repeat(32)}` as Hex,
  };
  const signature = await account.signTypedData({
    domain: { ...USDC_EIP712_DOMAIN, chainId: CHAIN_ID, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: account.address, to: PAYEE, value: 40_000n, validAfter: 0n,
      validBefore: BigInt(authorization.validBefore), nonce: authorization.nonce,
    },
  });
  return { x402Version: 2, scheme: 'exact', network: `eip155:${CHAIN_ID}`, payload: { signature, authorization } };
}

interface Fake {
  cfg: CircleWalletConfig;
  wallet: Record<string, string>;
  /** Transaction states served on successive reads; the last one repeats. */
  states: { state: string; txHash?: string; errorReason?: string }[];
  createStatus: number;
  executions: Record<string, string>[];
  walletReads: number;
  decrypt: (b64: string) => string;
}

function fakeCircle(): Fake {
  const { publicKey, privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const pem = publicKey.export({ type: 'spki', format: 'pem' }).toString();
  let reads = 0;
  const f: Fake = {
    cfg: { apiKey: API_KEY, entitySecretHex: SECRET, walletId: WALLET_ID, baseUrl: 'https://circle.test' },
    wallet: { id: WALLET_ID, address: RELAYER, blockchain: 'ARC-TESTNET', state: 'LIVE', custodyType: 'DEVELOPER', accountType: 'EOA' },
    states: [{ state: 'INITIATED' }, { state: 'SENT', txHash: HASH }, { state: 'COMPLETE', txHash: HASH }],
    createStatus: 201,
    executions: [],
    walletReads: 0,
    decrypt: (b64) =>
      privateDecrypt({ key: privateKey, padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: 'sha256' }, Buffer.from(b64, 'base64')).toString('hex'),
  };
  const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });
  f.cfg.fetchImpl = async (url, init) => {
    const path = url.replace('https://circle.test', '');
    if (path === '/v1/w3s/config/entity/publicKey') return json(200, { data: { publicKey: pem } });
    if (path === `/v1/w3s/wallets/${WALLET_ID}`) {
      f.walletReads++;
      return json(200, { data: { wallet: f.wallet } });
    }
    if (path === '/v1/w3s/developer/transactions/contractExecution' && init?.method === 'POST') {
      f.executions.push(JSON.parse(String(init.body)));
      if (f.createStatus >= 300) return json(f.createStatus, { code: 156001, message: 'refused' });
      reads = 0;
      return json(201, { data: { id: 'tx-1', state: 'INITIATED' } });
    }
    if (path === '/v1/w3s/transactions/tx-1') {
      const t = f.states[Math.min(reads++, f.states.length - 1)];
      return json(200, { data: { transaction: { id: 'tx-1', ...t } } });
    }
    return json(404, { code: 404, message: `unexpected ${path}` });
  };
  return f;
}

function relayed(f: Fake, receipt: 'success' | 'reverted' = 'success', timeoutMs = 2_000) {
  return new SelfFacilitator({
    chainId: CHAIN_ID, usdcAddress: USDC, circle: f.cfg,
    circleTiming: { pollMs: 1, timeoutMs },
    receiptStatus: async (h) => {
      assert.equal(h, HASH, 'the receipt is read for the hash Circle reported');
      return receipt;
    },
  });
}

test('settles through the Circle relayer: the exact transferWithAuthorization, a fresh key and ciphertext each time', async () => {
  const f = fakeCircle();
  const fac = relayed(f);
  assert.equal(fac.isConfigured(), true);
  assert.equal(fac.relayerKind(), 'circle');
  const payment = await signedPayment();

  const out = await fac.settle(payment, requirements);
  assert.deepEqual(out, { state: 'settled', txHash: HASH, payer: payment.payload.authorization.from });

  await fac.settle(await signedPayment(), requirements);
  assert.equal(f.executions.length, 2);
  assert.equal(f.walletReads, 1, 'the wallet is loaded once, not per payment');
  const [a, b] = f.executions as [Record<string, string>, Record<string, string>];
  assert.equal(a.walletId, WALLET_ID);
  assert.equal(a.contractAddress, USDC);
  assert.notEqual(a.idempotencyKey, b.idempotencyKey);
  assert.notEqual(a.entitySecretCiphertext, b.entitySecretCiphertext);
  assert.equal(f.decrypt(a.entitySecretCiphertext!), SECRET);

  const call = decodeFunctionData({ abi: TWA, data: a.callData as Hex });
  const auth = payment.payload.authorization;
  assert.equal(call.functionName, 'transferWithAuthorization');
  assert.deepEqual(call.args.slice(0, 6), [getAddress(auth.from), getAddress(PAYEE), 40_000n, 0n, BigInt(auth.validBefore), auth.nonce]);
});

test("Circle's COMPLETE is not the verdict: a reverted receipt is rejected, never settled", async () => {
  const f = fakeCircle();
  const out = await relayed(f, 'reverted').settle(await signedPayment(), requirements);
  assert.equal(out.state, 'rejected');
  assert.match((out as { reason: string }).reason, new RegExp(`reverted \\(tx ${HASH}\\)`));
  assert.equal(f.executions.length, 1, 'it was submitted; the receipt rejected it');
});

test('Circle reporting it never broadcast is rejected (nothing moved); FAILED with a hash defers to the receipt', async () => {
  for (const t of [{ state: 'DENIED' }, { state: 'CANCELLED' }, { state: 'FAILED', errorReason: 'INSUFFICIENT_NATIVE_TOKEN' }]) {
    const f = fakeCircle();
    f.states = [t];
    const out = await relayed(f).settle(await signedPayment(), requirements);
    assert.equal(out.state, 'rejected', t.state);
    assert.match((out as { reason: string }).reason, new RegExp(`not broadcast: Circle transaction tx-1 (was ${t.state}|failed before broadcast)`));
  }
  const f = fakeCircle();
  f.states = [{ state: 'FAILED', txHash: HASH }];
  assert.equal((await relayed(f, 'reverted').settle(await signedPayment(), requirements)).state, 'rejected');
});

test('a submission that may have landed is unknown: a 5xx on create, or still pending at the deadline', async () => {
  const f5 = fakeCircle();
  f5.createStatus = 503;
  assert.equal((await relayed(f5).settle(await signedPayment(), requirements)).state, 'unknown');

  const f4 = fakeCircle();
  f4.createStatus = 400;
  assert.equal((await relayed(f4).settle(await signedPayment(), requirements)).state, 'rejected', 'a 4xx is Circle refusing');

  const stuck = fakeCircle();
  stuck.states = [{ state: 'SENT', txHash: HASH }];
  const out = await relayed(stuck, 'success', 20).settle(await signedPayment(), requirements);
  assert.equal(out.state, 'unknown');
  assert.match((out as { reason: string }).reason, /may still land/);
});

test('a wallet that cannot relay on this chain is refused before anything is submitted, and retried next time', async () => {
  for (const [field, value] of [['accountType', 'SCA'], ['blockchain', 'ARC'], ['state', 'FROZEN']] as const) {
    const f = fakeCircle();
    f.wallet[field] = value;
    const fac = relayed(f);
    const out = await fac.settle(await signedPayment(), requirements);
    assert.equal(out.state, 'rejected', `${field}=${value}`);
    assert.equal(f.executions.length, 0, `${field}=${value} submitted anyway`);

    f.wallet = { ...f.wallet, accountType: 'EOA', blockchain: 'ARC-TESTNET', state: 'LIVE' };
    assert.equal((await fac.settle(await signedPayment(), requirements)).state, 'settled', 'a failed load is not cached');
  }
});

test('from the environment: the Circle relayer wins over a raw key, and a broken one fails at startup without echoing a secret', () => {
  const saved = { ...process.env };
  try {
    Object.assign(process.env, {
      CIRCLE_BROADCASTER_WALLET_ID: WALLET_ID, CIRCLE_API_KEY: API_KEY, CIRCLE_ENTITY_SECRET: SECRET,
      BROADCASTER_PRIVATE_KEY: generatePrivateKey(),
    });
    assert.equal(new SelfFacilitator({ chainId: CHAIN_ID }).relayerKind(), 'circle');

    process.env.CIRCLE_ENTITY_SECRET = 'not-hex';
    assert.throws(() => new SelfFacilitator({ chainId: CHAIN_ID }), (e: Error) => {
      assert.match(e.message, /CIRCLE_ENTITY_SECRET/);
      assert.ok(!e.message.includes('inert-fake-api-secret'), 'api key echoed');
      return true;
    });

    delete process.env.CIRCLE_BROADCASTER_WALLET_ID;
    assert.equal(new SelfFacilitator({ chainId: CHAIN_ID }).relayerKind(), 'key');
  } finally {
    for (const k of Object.keys(process.env)) if (!(k in saved)) delete process.env[k];
    Object.assign(process.env, saved);
  }
});
