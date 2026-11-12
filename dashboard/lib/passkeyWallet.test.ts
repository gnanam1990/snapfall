import { test } from 'node:test';
import assert from 'node:assert/strict';
import { clientKeyMatchesNetwork, passkeyConfig, userOpFees } from './passkeyWallet';

// The bundler's medium level, as Circle answers it: decimal wei strings.
const answer = {
  low: { maxFeePerGas: '30000000000', maxPriorityFeePerGas: '1000000000' },
  medium: { maxFeePerGas: '48500000000', maxPriorityFeePerGas: '1500000000' },
  high: { maxFeePerGas: '60000000000', maxPriorityFeePerGas: '2000000000' },
};

test('user operation fees come from the bundler medium level', () => {
  assert.deepEqual(userOpFees(answer), { maxFeePerGas: 48_500_000_000n, maxPriorityFeePerGas: 1_500_000_000n });
});

test('a missing or malformed gas price is refused, never guessed', () => {
  for (const bad of [
    null,
    {},
    { medium: { maxFeePerGas: '', maxPriorityFeePerGas: '1' } },
    { medium: { maxFeePerGas: '1.5', maxPriorityFeePerGas: '1' } },
    { medium: { maxFeePerGas: '0x10', maxPriorityFeePerGas: '1' } },
    { medium: { maxFeePerGas: '-1', maxPriorityFeePerGas: '1' } },
  ]) {
    assert.throws(() => userOpFees(bad as never), /usable gas price/);
  }
  assert.throws(() => userOpFees({ medium: { maxFeePerGas: '0', maxPriorityFeePerGas: '0' } }), /inconsistent/);
  assert.throws(() => userOpFees({ medium: { maxFeePerGas: '10', maxPriorityFeePerGas: '11' } }), /inconsistent/);
});

test('a gas price that is not a decimal string is refused, even if it would stringify to one', () => {
  // 2^53 + 1 cannot be held exactly by a JSON number; accepting it would sign a rounded fee.
  const answers = [
    { medium: { maxFeePerGas: 9007199254740993, maxPriorityFeePerGas: '1000000000' } },
    { medium: { maxFeePerGas: '48500000000', maxPriorityFeePerGas: 1500000000 } },
    { medium: { maxFeePerGas: ['48500000000'], maxPriorityFeePerGas: '1500000000' } },
  ];
  for (const bad of answers) {
    assert.throws(() => userOpFees(bad as never), /usable gas price/);
  }
});

test('a client key is only used on its own network', () => {
  assert.equal(clientKeyMatchesNetwork('TEST_CLIENT_KEY:abc:def', 'arc-testnet'), true);
  assert.equal(clientKeyMatchesNetwork('TEST_CLIENT_KEY:abc:def', 'arc-mainnet'), false);
  assert.equal(clientKeyMatchesNetwork('LIVE_CLIENT_KEY:abc:def', 'arc-mainnet'), true);
  assert.equal(clientKeyMatchesNetwork('LIVE_CLIENT_KEY:abc:def', 'arc-testnet'), false);
  assert.equal(clientKeyMatchesNetwork('abc', 'arc-mainnet'), false);
  assert.equal(clientKeyMatchesNetwork('abc', 'arc-testnet'), false);
});

test('a mainnet build with only a testnet key offers no passkey wallet', () => {
  assert.equal(passkeyConfig('arc-mainnet', 'TEST_CLIENT_KEY:abc:def'), null);
  assert.equal(passkeyConfig('arc-testnet', 'LIVE_CLIENT_KEY:abc:def'), null);
  assert.equal(passkeyConfig('arc-mainnet', ''), null);
  assert.equal(passkeyConfig('arc-mainnet', 'LIVE_CLIENT_KEY:abc:def')?.clientKey, 'LIVE_CLIENT_KEY:abc:def');
  assert.equal(passkeyConfig('arc-testnet', 'TEST_CLIENT_KEY:abc:def')?.clientKey, 'TEST_CLIENT_KEY:abc:def');
});
