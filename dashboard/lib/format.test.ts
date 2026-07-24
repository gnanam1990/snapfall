import assert from 'node:assert/strict';
import test from 'node:test';
import { formatUsdcExact } from './format';

test('formatUsdcExact retains meaningful on-chain precision', () => {
  assert.equal(formatUsdcExact('20008000'), '20.008');
  assert.equal(formatUsdcExact('2000'), '0.002');
  assert.equal(formatUsdcExact('12500000'), '12.50');
  assert.equal(formatUsdcExact(0n), '0.00');
});

test('formatUsdcExact leaves malformed wire values visible', () => {
  assert.equal(formatUsdcExact('12.50'), '12.50');
  assert.equal(formatUsdcExact('not-an-amount'), 'not-an-amount');
});
