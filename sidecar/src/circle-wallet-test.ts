/**
 * The Circle treasury signer, against a fake Circle API that does what the real one does:
 * decrypts the entity-secret ciphertext with its own RSA key, and signs the EIP-712 JSON it was
 * sent with the wallet's key. A wrong `data` encoding therefore produces a signature that does
 * not recover, which is exactly what the local recovery check exists to catch.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync, privateDecrypt, constants } from 'node:crypto';
import { privateKeyToAccount } from 'viem/accounts';
import { recoverTypedDataAddress, type Hex } from 'viem';

import { loadCircleAccount, type CircleWalletConfig } from './circle-wallet.js';
import { loadSigner } from './buyer.js';

// THROWAWAY values, never real credentials or a funded key.
const API_KEY = 'INERT:not-a-circle-key:inert-fake-api-secret';
const SECRET = 'ab'.repeat(32); // 32 bytes, deliberately patterned so no scanner reads it as a key
const WALLET_ID = '8a1f6c2e-3b4d-4e5f-9a0b-1c2d3e4f5a6b';
const WALLET_KEY: Hex = '0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d';
const OTHER_KEY: Hex = '0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a';

const USDC = '0x3600000000000000000000000000000000000000';

interface Fake {
  cfg: CircleWalletConfig;
  wallet: Record<string, string>;
  signWith: Hex;
  signRequests: Record<string, unknown>[];
  ciphertexts: string[];
  decrypt: (b64: string) => string;
}

function fakeCircle(): Fake {
  const { publicKey, privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const pem = publicKey.export({ type: 'spki', format: 'pem' }).toString();
  const walletAddr = privateKeyToAccount(WALLET_KEY).address;
  const f: Fake = {
    cfg: { apiKey: API_KEY, entitySecretHex: SECRET, walletId: WALLET_ID, baseUrl: 'https://circle.test' },
    wallet: {
      id: WALLET_ID, address: walletAddr, blockchain: 'ARC-TESTNET',
      state: 'LIVE', custodyType: 'DEVELOPER', accountType: 'EOA',
    },
    signWith: WALLET_KEY,
    signRequests: [],
    ciphertexts: [],
    decrypt: (b64) =>
      privateDecrypt(
        { key: privateKey, padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: 'sha256' },
        Buffer.from(b64, 'base64'),
      ).toString('hex'),
  };
  const json = (status: number, body: unknown) =>
    new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
  f.cfg.fetchImpl = async (url, init) => {
    const auth = (init?.headers as Record<string, string>)?.Authorization;
    if (auth !== `Bearer ${API_KEY}`) return json(401, { code: 401, message: 'Malformed authorization.' });
    const path = url.replace('https://circle.test', '');
    if (path === '/v1/w3s/config/entity/publicKey') return json(200, { data: { publicKey: pem } });
    if (path === `/v1/w3s/wallets/${WALLET_ID}`) return json(200, { data: { wallet: f.wallet } });
    if (path === '/v1/w3s/developer/sign/typedData' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as Record<string, string>;
      f.signRequests.push(body);
      f.ciphertexts.push(body.entitySecretCiphertext!);
      const td = JSON.parse(body.data!) as {
        types: Record<string, { name: string; type: string }[]>;
        domain: Record<string, unknown>;
        primaryType: string;
        message: Record<string, unknown>;
      };
      // Sign what was SENT, as Circle would, converting uints back from their JSON strings.
      const { EIP712Domain: _domainType, ...types } = td.types;
      const message = { ...td.message };
      for (const field of types[td.primaryType] ?? []) {
        if (field.type.startsWith('uint')) message[field.name] = BigInt(String(message[field.name]));
      }
      const signature = await privateKeyToAccount(f.signWith).signTypedData({
        domain: td.domain,
        types,
        primaryType: td.primaryType,
        message,
      } as never);
      return json(200, { data: { signature } });
    }
    return json(404, { code: 404, message: `unexpected ${path}` });
  };
  return f;
}

/** The EIP-3009 authorization buyer.ts signs, in the shape it signs it. */
function authorization(chainId: number) {
  return {
    domain: { name: 'USDC', version: '2', chainId, verifyingContract: USDC as Hex },
    types: {
      TransferWithAuthorization: [
        { name: 'from', type: 'address' },
        { name: 'to', type: 'address' },
        { name: 'value', type: 'uint256' },
        { name: 'validAfter', type: 'uint256' },
        { name: 'validBefore', type: 'uint256' },
        { name: 'nonce', type: 'bytes32' },
      ],
    },
    primaryType: 'TransferWithAuthorization' as const,
    message: {
      from: privateKeyToAccount(WALLET_KEY).address,
      to: '0x00000000000000000000000000000000000000b2' as Hex,
      value: 40_000n,
      validAfter: 0n,
      validBefore: 1_900_000_000n,
      nonce: `0x${'11'.repeat(32)}` as Hex,
    },
  };
}

test('signs an EIP-3009 authorization through Circle, fresh ciphertext every time, recoverable to the wallet', async () => {
  const f = fakeCircle();
  const account = await loadCircleAccount(f.cfg);
  assert.equal(account.address, privateKeyToAccount(WALLET_KEY).address);

  const td = authorization(5042002);
  const sig1 = await account.signTypedData(td);
  const sig2 = await account.signTypedData(td);
  assert.equal(await recoverTypedDataAddress({ ...td, signature: sig1 }), account.address);
  assert.equal(sig1, sig2, 'same message, same deterministic signature');

  assert.equal(f.signRequests.length, 2);
  const body = f.signRequests[0]!;
  assert.equal(body.walletId, WALLET_ID);
  const sent = JSON.parse(String(body.data));
  assert.deepEqual(sent.types.EIP712Domain.map((t: { name: string }) => t.name), ['name', 'version', 'chainId', 'verifyingContract']);
  assert.equal(sent.message.value, '40000', 'bigints travel as decimal strings');

  assert.notEqual(f.ciphertexts[0], f.ciphertexts[1], 'a reused ciphertext is rejected by Circle');
  for (const ct of f.ciphertexts) assert.equal(f.decrypt(ct), SECRET);
});

test('a signature that does not recover to the wallet is refused before it can be used', async () => {
  const f = fakeCircle();
  const account = await loadCircleAccount(f.cfg);
  f.signWith = OTHER_KEY;
  await assert.rejects(account.signTypedData(authorization(5042002)), /recovers to .* not the wallet/);
});

test('refuses to sign for a chain the wallet is not on, without asking Circle', async () => {
  const f = fakeCircle();
  const account = await loadCircleAccount(f.cfg);
  await assert.rejects(account.signTypedData(authorization(1)), /refusing to sign for chain 1/);
  assert.equal(f.signRequests.length, 0);
});

test('signs typed data only', async () => {
  const account = await loadCircleAccount(fakeCircle().cfg);
  await assert.rejects(account.signMessage({ message: 'hi' }), /typed data only/);
});

test('refuses a wallet that cannot be the treasury signer', async () => {
  for (const [field, value] of [
    ['accountType', 'SCA'],
    ['state', 'FROZEN'],
    ['custodyType', 'ENDUSER'],
    ['blockchain', 'BASE-SEPOLIA'],
    ['address', '0x123'],
  ] as const) {
    const f = fakeCircle();
    f.wallet[field] = value;
    await assert.rejects(loadCircleAccount(f.cfg), Error, `${field}=${value} was accepted`);
  }
});

test('a configured but broken Circle wallet never falls back to the raw key, and never echoes a secret', async () => {
  const env = {
    CIRCLE_TREASURY_WALLET_ID: WALLET_ID,
    CIRCLE_API_KEY: API_KEY,
    CIRCLE_ENTITY_SECRET: 'not-hex',
    TREASURY_PRIVATE_KEY: WALLET_KEY,
  };
  await assert.rejects(loadSigner(env), (e: Error) => {
    assert.match(e.message, /CIRCLE_ENTITY_SECRET/);
    for (const s of ['inert-fake-api-secret', WALLET_KEY.slice(2, 20)]) assert.ok(!e.message.includes(s), 'secret echoed');
    return true;
  });

  // Rejected by Circle: the error carries Circle's code, not the key.
  const f = fakeCircle();
  f.cfg.apiKey = 'WRONG:KEY:inert-fake-api-secret';
  await assert.rejects(loadCircleAccount(f.cfg), (e: Error) => {
    assert.match(e.message, /HTTP 401/);
    assert.ok(!e.message.includes('inert-fake-api-secret'), 'api key echoed');
    return true;
  });

  // And with no Circle wallet configured, the raw key path is unchanged.
  const raw = await loadSigner({ TREASURY_PRIVATE_KEY: WALLET_KEY });
  assert.equal(raw.address, privateKeyToAccount(WALLET_KEY).address);
});
