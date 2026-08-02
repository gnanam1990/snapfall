import assert from 'node:assert/strict';
import test from 'node:test';
import { formatUsdc, formatUsdcExact, formatBps } from './format';

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

// The centre-piece-screen bug: the daemon omits aggregate fields it cannot source, so the
// dashboard formats `undefined`. Before this, formatUsdc(undefined) fell through to
// String(undefined) === "undefined" and rendered the literal word on a money card.
const DASH = '—';

test('an absent amount renders a dash, never the literal "undefined" or "null"', () => {
  for (const fmt of [formatUsdc, formatUsdcExact]) {
    assert.equal(fmt(undefined), DASH, 'undefined must dash');
    assert.equal(fmt(null), DASH, 'null must dash');
  }
});

test('no input to formatUsdc/formatUsdcExact can produce "undefined" or "null"', () => {
  // Includes the shapes a real aggregate or a bad frame can carry.
  const inputs: Array<string | bigint | null | undefined> = [
    undefined, null, '', '0', '12500000', 12500000n, 0n, '12.50', 'not-an-amount', '0xdead',
  ];
  for (const fmt of [formatUsdc, formatUsdcExact]) {
    for (const i of inputs) {
      const out = fmt(i);
      assert.notEqual(out, 'undefined', `${String(i)} produced "undefined"`);
      assert.notEqual(out, 'null', `${String(i)} produced "null"`);
    }
  }
});

test('formatUsdc formats and preserves the malformed-visible contract', () => {
  assert.equal(formatUsdc('12500000'), '12.50');
  assert.equal(formatUsdc(''), '0.00');
  assert.equal(formatUsdc('not-an-amount'), 'not-an-amount');
});

test('formatBps dashes an absent or non-finite rate, never "undefined"', () => {
  assert.equal(formatBps(6000), '60%');
  assert.equal(formatBps(5500), '55%');
  assert.equal(formatBps(undefined), DASH);
  assert.equal(formatBps(null), DASH);
  assert.equal(formatBps(NaN), DASH);
});
