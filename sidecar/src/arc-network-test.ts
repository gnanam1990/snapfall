/**
 * The network choice, and the two ways it used to go wrong silently.
 *
 * Before arc-network.ts every script read ARC_TESTNET_RPC whatever chain id it was given, so
 * ARC_CHAIN_ID=5042 with a mainnet URL in ARC_TESTNET_RPC "worked" and ARC_CHAIN_ID=5042 alone sent
 * mainnet-signed authorizations to the testnet endpoint. The mutations that matter: reading
 * ARC_TESTNET_RPC on mainnet, and defaulting an unknown chain id to testnet. Both fail here.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import type { Hex } from 'viem';

import { resolveArcNetwork } from './arc-network.js';
import { selectFacilitator } from './facilitator-select.js';
import { SelfFacilitator } from './self-facilitator.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES, type PaymentPayload } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import type { PaymentRequirements } from './facilitator.js';

const ALCHEMY_MAINNET = 'https://arc-mainnet.g.alchemy.com/v2/test-key';
const ALCHEMY_TESTNET = 'https://arc-testnet.g.alchemy.com/v2/test-key';

test('no chain id is Arc testnet on the public endpoint, as before', () => {
  const n = resolveArcNetwork({});
  assert.equal(n.name, 'arc-testnet');
  assert.equal(n.chainId, 5042002);
  assert.equal(n.caip2, 'eip155:5042002');
  assert.equal(n.rpcUrl, 'https://rpc.testnet.arc.network');
  assert.equal(n.testnet, true);
});

test('ARC_CHAIN_ID=5042 is mainnet, on ARC_MAINNET_RPC', () => {
  const n = resolveArcNetwork({ ARC_CHAIN_ID: '5042', ARC_MAINNET_RPC: ALCHEMY_MAINNET });
  assert.equal(n.name, 'arc-mainnet');
  assert.equal(n.caip2, 'eip155:5042');
  assert.equal(n.rpcUrl, ALCHEMY_MAINNET);
  assert.equal(n.rpcUrlEnv, 'ARC_MAINNET_RPC');
  assert.equal(n.testnet, false);
});

test('mainnet never reads ARC_TESTNET_RPC, even when it is the only RPC set', () => {
  const n = resolveArcNetwork({ ARC_CHAIN_ID: '5042', ARC_TESTNET_RPC: ALCHEMY_TESTNET });
  assert.equal(n.rpcUrl, 'https://rpc.mainnet.arc.io');
});

test('testnet never reads ARC_MAINNET_RPC', () => {
  const n = resolveArcNetwork({ ARC_CHAIN_ID: '5042002', ARC_MAINNET_RPC: ALCHEMY_MAINNET, ARC_TESTNET_RPC: ' ' });
  assert.equal(n.rpcUrl, 'https://rpc.testnet.arc.network');
});

test('an unknown or malformed chain id refuses to start rather than falling back to testnet', () => {
  assert.throws(() => resolveArcNetwork({ ARC_CHAIN_ID: '1' }), /not an Arc network/);
  assert.throws(() => resolveArcNetwork({ ARC_CHAIN_ID: '5042abc' }), /not a chain id/);
  assert.throws(() => resolveArcNetwork({ ARC_CHAIN_ID: '-5042' }), /not a chain id/);
});

test('a stale ARC_TESTNET_CHAIN_ID cannot outvote ARC_CHAIN_ID', () => {
  assert.throws(() => resolveArcNetwork({ ARC_CHAIN_ID: '5042', ARC_TESTNET_CHAIN_ID: '5042002' }), /disagree/);
  assert.equal(resolveArcNetwork({ ARC_TESTNET_CHAIN_ID: '5042002' }).chainId, 5042002);
  assert.equal(resolveArcNetwork({ ARC_CHAIN_ID: '5042', ARC_TESTNET_CHAIN_ID: '5042' }).chainId, 5042);
});

test('the testnet-only Circle Gateway facilitator refuses mainnet; self-relay is allowed', () => {
  assert.throws(() => selectFacilitator({ ARC_CHAIN_ID: '5042' }), /testnet-only/);
  assert.ok(selectFacilitator({ ARC_CHAIN_ID: '5042', SNAPFALL_SELF_FACILITATOR: '1' }) instanceof SelfFacilitator);
  assert.ok(!(selectFacilitator({}) instanceof SelfFacilitator));
});

// The relayer itself. Its RPC and chain are private, so these read them the way a regression
// would show up: the URL it would broadcast to, and the domain it verifies against.
const rpcOf = (f: SelfFacilitator): string => (f as unknown as { rpcUrl: string }).rpcUrl;

function withEnv<T>(vars: Record<string, string | undefined>, fn: () => T): T {
  const saved = Object.fromEntries(Object.keys(vars).map((k) => [k, process.env[k]]));
  for (const [k, v] of Object.entries(vars)) v === undefined ? delete process.env[k] : (process.env[k] = v);
  try {
    return fn();
  } finally {
    for (const [k, v] of Object.entries(saved)) v === undefined ? delete process.env[k] : (process.env[k] = v);
  }
}

test('the self-relayer on mainnet broadcasts to the mainnet RPC, never ARC_TESTNET_RPC', () => {
  withEnv({ ARC_CHAIN_ID: '5042', ARC_TESTNET_RPC: ALCHEMY_TESTNET, ARC_MAINNET_RPC: undefined, ARC_TESTNET_CHAIN_ID: undefined }, () => {
    assert.equal(rpcOf(new SelfFacilitator()), 'https://rpc.mainnet.arc.io');
  });
  withEnv({ ARC_CHAIN_ID: '5042', ARC_MAINNET_RPC: ALCHEMY_MAINNET, ARC_TESTNET_CHAIN_ID: undefined }, () => {
    assert.equal(rpcOf(new SelfFacilitator()), ALCHEMY_MAINNET);
  });
  // An injected mainnet chain id takes its RPC from mainnet too, whatever the process env says.
  withEnv({ ARC_CHAIN_ID: '5042002', ARC_TESTNET_RPC: ALCHEMY_TESTNET, ARC_MAINNET_RPC: undefined }, () => {
    assert.equal(rpcOf(new SelfFacilitator({ chainId: 5042 })), 'https://rpc.mainnet.arc.io');
  });
  assert.throws(() => new SelfFacilitator({ chainId: 1 }), /not an Arc network/);
});

test('the self-relayer verifies against the chain it is on: a mainnet authorization fails on testnet', async () => {
  const USDC = '0x3600000000000000000000000000000000000000' as const;
  const PAYEE = '0x000000000000000000000000000000000000fee1' as const;
  const account = privateKeyToAccount(generatePrivateKey());
  const authorization = {
    from: account.address, to: PAYEE, value: '40000', validAfter: '0',
    validBefore: String(Math.floor(Date.now() / 1000) + 3600), nonce: `0x${'ab'.repeat(32)}` as Hex,
  };
  const signature = await account.signTypedData({
    domain: { ...USDC_EIP712_DOMAIN, chainId: 5042, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: { from: account.address, to: PAYEE, value: 40000n, validAfter: 0n, validBefore: BigInt(authorization.validBefore), nonce: authorization.nonce },
  });
  const payment: PaymentPayload = { x402Version: 2, scheme: 'exact', network: 'eip155:5042', payload: { signature, authorization } };
  const requirements: PaymentRequirements = { scheme: 'exact', network: 'eip155:5042', maxAmountRequired: '40000', resource: '/r', payTo: PAYEE, asset: USDC };

  assert.equal((await new SelfFacilitator({ chainId: 5042, usdcAddress: USDC }).verify(payment, requirements)).valid, true);
  assert.equal((await new SelfFacilitator({ chainId: 5042002, usdcAddress: USDC }).verify(payment, requirements)).valid, false);
});
