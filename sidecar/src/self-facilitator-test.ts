/**
 * SelfFacilitator, in isolation. No chain: the broadcast is injected, so the settle logic and the
 * honesty rule (a real receipt hash is the ONLY thing that settles) are exercised without a
 * network and without any way to mint a fake hash. The verify tests sign real EIP-3009
 * authorizations, including one over the WRONG (mainnet "USD Coin") domain — the bug this whole
 * change was about — and prove it is rejected against the real USDC domain.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import type { Hex } from 'viem';

import { SelfFacilitator, type Broadcast } from './self-facilitator.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES, type PaymentPayload } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import type { PaymentRequirements } from './facilitator.js';

const CHAIN_ID = 5042002;
const USDC = '0x3600000000000000000000000000000000000000' as const;
const PAYEE = '0x000000000000000000000000000000000000fee1' as const;

const requirements: PaymentRequirements = {
  scheme: 'exact', network: `eip155:${CHAIN_ID}`, maxAmountRequired: '40000',
  resource: '/premium', payTo: PAYEE, asset: USDC,
};

async function signedPayment(opts: { name?: string; to?: string; value?: string } = {}): Promise<{ payment: PaymentPayload; from: string }> {
  const account = privateKeyToAccount(generatePrivateKey());
  const authorization = {
    from: account.address,
    to: opts.to ?? PAYEE,
    value: opts.value ?? '40000',
    validAfter: '0',
    validBefore: String(Math.floor(Date.now() / 1000) + 3600),
    nonce: `0x${'ab'.repeat(32)}` as Hex,
  };
  const signature = await account.signTypedData({
    domain: { name: opts.name ?? USDC_EIP712_DOMAIN.name, version: USDC_EIP712_DOMAIN.version, chainId: CHAIN_ID, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: authorization.from as Hex,
      to: authorization.to as Hex,
      value: BigInt(authorization.value),
      validAfter: 0n,
      validBefore: BigInt(authorization.validBefore),
      nonce: authorization.nonce,
    },
  });
  return {
    payment: { x402Version: 2, scheme: 'exact', network: `eip155:${CHAIN_ID}`, payload: { signature, authorization } },
    from: account.address,
  };
}

const ok: Broadcast = async () => ({ txHash: `0x${'11'.repeat(32)}` as Hex, status: 'success' });
function fac(broadcast: Broadcast) {
  return new SelfFacilitator({ chainId: CHAIN_ID, usdcAddress: USDC, broadcast });
}

test('verify recovers the payer against the real USDC domain', async () => {
  const { payment, from } = await signedPayment();
  const out = await fac(ok).verify(payment, requirements);
  assert.equal(out.valid, true);
  assert.equal(out.payer?.toLowerCase(), from.toLowerCase());
});

test('a signature over the WRONG domain (mainnet "USD Coin") is rejected — the latent bug', async () => {
  const { payment } = await signedPayment({ name: 'USD Coin' });
  const out = await fac(ok).verify(payment, requirements);
  assert.equal(out.valid, false, '"USD Coin" recovers to a different address against the USDC domain');
});

test('verify rejects a payment to the wrong payee or below the required amount', async () => {
  const wrongPayee = await signedPayment({ to: '0x000000000000000000000000000000000000dead' });
  assert.equal((await fac(ok).verify(wrongPayee.payment, requirements)).valid, false);
  const tooLittle = await signedPayment({ value: '1' });
  assert.equal((await fac(ok).verify(tooLittle.payment, requirements)).valid, false);
});

test('settle returns settled ONLY with the real receipt hash', async () => {
  const { payment, from } = await signedPayment();
  const hash = `0x${'ce'.repeat(32)}` as Hex;
  const out = await new SelfFacilitator({ chainId: CHAIN_ID, usdcAddress: USDC, broadcast: async () => ({ txHash: hash, status: 'success' }) })
    .settle(payment, requirements);
  assert.deepEqual(out, { state: 'settled', txHash: hash, payer: from });
});

test('a reverted broadcast is rejected — nothing moved', async () => {
  const { payment } = await signedPayment();
  const out = await new SelfFacilitator({ chainId: CHAIN_ID, usdcAddress: USDC, broadcast: async () => ({ txHash: `0x${'22'.repeat(32)}` as Hex, status: 'reverted' }) })
    .settle(payment, requirements);
  assert.equal(out.state, 'rejected');
});

test('a broadcast that throws is unknown, never settled — the auth may still land', async () => {
  const { payment } = await signedPayment();
  const out = await new SelfFacilitator({ chainId: CHAIN_ID, usdcAddress: USDC, broadcast: async () => { throw new Error('rpc dropped the submit'); } })
    .settle(payment, requirements);
  assert.equal(out.state, 'unknown');
});

test('with no broadcaster configured, settle is not-configured — never a fabricated hash', async () => {
  const saved = process.env.BROADCASTER_PRIVATE_KEY;
  delete process.env.BROADCASTER_PRIVATE_KEY;
  try {
    const { payment } = await signedPayment();
    const out = await new SelfFacilitator({ chainId: CHAIN_ID, usdcAddress: USDC }).settle(payment, requirements);
    assert.equal(out.state, 'not-configured');
  } finally {
    if (saved !== undefined) process.env.BROADCASTER_PRIVATE_KEY = saved;
  }
});

test('endpoints are self-markers, never Circle URLs — a self settle cannot pose as Circle evidence', () => {
  const eps = fac(ok).endpoints();
  assert.match(eps.settle, /^self:transferWithAuthorization@/);
  assert.doesNotMatch(eps.settle, /circle\.com/);
  assert.doesNotMatch(eps.verify, /circle\.com/);
});
