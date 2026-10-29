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

import { resolveArcNetwork } from './arc-network.js';
import { selectFacilitator } from './facilitator-select.js';
import { SelfFacilitator } from './self-facilitator.js';

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
