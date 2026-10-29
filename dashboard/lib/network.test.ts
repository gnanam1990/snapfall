import assert from 'node:assert/strict';
import test from 'node:test';
import { chainNetwork, clientSafeError, contractAddress, deploymentFor, networkLabel, networkName, requireAddress } from './network';
import { isSafeExplorerUrl } from './format';

const ALCHEMY_MAINNET = 'https://arc-mainnet.g.alchemy.com/v2/test-key';
const ALCHEMY_TESTNET = 'https://arc-testnet.g.alchemy.com/v2/test-key';

test('unset SNAPFALL_NETWORK is testnet, read exactly as the routes did before', () => {
  assert.equal(networkName(undefined), 'arc-testnet');
  assert.equal(networkName('  '), 'arc-testnet');
  const net = chainNetwork({}, 'arc-testnet');
  assert.equal(net.chainId, 5042002);
  assert.equal(net.rpcUrl, deploymentFor('arc-testnet').network.rpcUrl);
  assert.equal(net.hasPrivateRpc, false);
  assert.equal(requireAddress('floatPool', {}, 'arc-testnet'), '0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5');
});

test('a typo in SNAPFALL_NETWORK fails instead of quietly meaning testnet', () => {
  assert.throws(() => networkName('mainnet'), /not arc-testnet or arc-mainnet/);
  assert.throws(() => networkName('arc-Mainnet'), /not arc-testnet or arc-mainnet/);
});

test('mainnet reads ARC_MAINNET_RPC and chain 5042', () => {
  const net = chainNetwork({ ARC_MAINNET_RPC: ALCHEMY_MAINNET }, 'arc-mainnet');
  assert.equal(net.chainId, 5042);
  assert.equal(net.rpcUrl, ALCHEMY_MAINNET);
  assert.equal(net.hasPrivateRpc, true);
  assert.equal(net.explorerUrl, 'https://explorer.arc.io');
  assert.equal(networkLabel('arc-mainnet'), 'Arc mainnet');
});

test('mainnet never reads ARC_TESTNET_RPC, and testnet never reads ARC_MAINNET_RPC', () => {
  // The mutation this pins: one shared RPC variable, so a testnet URL lands under chain 5042.
  const mainnet = chainNetwork({ ARC_TESTNET_RPC: ALCHEMY_TESTNET }, 'arc-mainnet');
  assert.equal(mainnet.rpcUrl, 'https://rpc.mainnet.arc.io');
  assert.equal(mainnet.hasPrivateRpc, false);
  const testnet = chainNetwork({ ARC_MAINNET_RPC: ALCHEMY_MAINNET }, 'arc-testnet');
  assert.equal(testnet.rpcUrl, deploymentFor('arc-testnet').network.rpcUrl);
});

test('mainnet has no contract until one is configured, and never falls back to a testnet address', () => {
  assert.equal(contractAddress('floatPool', {}, 'arc-mainnet'), undefined);
  assert.throws(() => requireAddress('floatPool', {}, 'arc-mainnet'), /set SNAPFALL_FLOAT_POOL_ADDRESS/);
  const pool = '0x1234567890123456789012345678901234567890';
  assert.equal(requireAddress('floatPool', { SNAPFALL_FLOAT_POOL_ADDRESS: pool }, 'arc-mainnet'), pool);
  // USDC is the same precompile on both networks, committed in both records.
  assert.equal(requireAddress('usdc', {}, 'arc-mainnet'), '0x3600000000000000000000000000000000000000');
});

test('SNAPFALL_DEPLOYMENT_BLOCK overrides the record start block', () => {
  assert.equal(chainNetwork({ SNAPFALL_DEPLOYMENT_BLOCK: '123' }, 'arc-mainnet').startBlock, 123);
  assert.equal(chainNetwork({}, 'arc-testnet').startBlock, deploymentFor('arc-testnet').network.startBlock);
});

test('both networks\' explorer links survive the href allowlist, and look-alikes do not', () => {
  for (const name of ['arc-testnet', 'arc-mainnet'] as const) {
    assert.ok(isSafeExplorerUrl(`${deploymentFor(name).network.explorerUrl}/tx/0xabc`), name);
  }
  assert.ok(isSafeExplorerUrl('https://explorer.testnet.arc.io/tx/0xabc'));
  assert.ok(!isSafeExplorerUrl('https://explorer.arc.io.evil.example/tx/0xabc'));
  assert.ok(!isSafeExplorerUrl('http://explorer.arc.io/tx/0xabc'));
});

test('chain errors shown to clients never carry the RPC URL or a provider key', () => {
  const env = { ARC_MAINNET_RPC: 'https://arc-mainnet.g.alchemy.com/v2/sEcReTkEy123456' };
  const leaky = new Error('request to https://arc-mainnet.g.alchemy.com/v2/sEcReTkEy123456 failed, reason: ECONNRESET');
  const safe = clientSafeError(leaky, 'x', env, 'arc-mainnet');
  assert.doesNotMatch(safe, /sEcReTkEy123456/);
  assert.match(safe, /\[rpc\] failed, reason: ECONNRESET/);

  // A URL we did not configure, and query-string keys, are caught by shape.
  assert.doesNotMatch(clientSafeError(new Error('https://other.example/v3/abcdefgh12345 down'), 'x', {}, 'arc-mainnet'), /abcdefgh12345/);
  assert.doesNotMatch(clientSafeError(new Error('GET https://rpc.example/?apikey=topsecret1 403'), 'x', {}, 'arc-mainnet'), /topsecret1/);

  // The useful part of an operator-facing message survives.
  assert.equal(clientSafeError(new Error('floatPool has no address on arc-mainnet: set SNAPFALL_FLOAT_POOL_ADDRESS'), 'x', env, 'arc-mainnet'),
    'floatPool has no address on arc-mainnet: set SNAPFALL_FLOAT_POOL_ADDRESS');
  assert.equal(clientSafeError('not an error', 'fallback text', env, 'arc-mainnet'), 'fallback text');
});
