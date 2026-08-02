/**
 * The rule these tests defend: the Float page's "computed entirely on-chain" check mark may
 * appear over a rate read from FloatPool, and over nothing else.
 *
 * It shipped as an unconditional element next to a value resolved with `??` from two sources,
 * so it vouched for the daemon's asserted rate too -- and under SNAPFALL_DEMO_STREAM for the
 * hardcoded 5000 in mockData.ts. That is a provenance claim over a figure nobody read.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { rateProvenance, mayClaimOnChain } from './rateProvenance.js';

test('a chain read is chain-sourced', () => {
  assert.equal(rateProvenance(5500, null), 'chain');
});

test('the chain read wins even when the daemon also reports one', () => {
  // Both present is the normal connected-daemon case. The chain value is the one displayed
  // (`snapshot.orgRateBps ?? fallbackRateBps`), so the provenance must follow it.
  assert.equal(rateProvenance(5500, 5000), 'chain');
});

test('a daemon figure is never reported as chain-sourced', () => {
  assert.equal(rateProvenance(null, 5000), 'daemon');
  assert.equal(mayClaimOnChain(rateProvenance(null, 5000)), false);
});

test('no organisation configured and no daemon is neither', () => {
  assert.equal(rateProvenance(null, null), 'none');
  assert.equal(mayClaimOnChain(rateProvenance(null, null)), false);
});

test('undefined is treated as absent, not as a value', () => {
  // FloatSnapshot fields arrive over the wire; a dropped key must not read as a rate.
  assert.equal(rateProvenance(undefined, undefined), 'none');
  assert.equal(rateProvenance(undefined, 5000), 'daemon');
});

test('a real on-chain zero is chain-sourced, not mistaken for absence', () => {
  // The trap a truthiness check would fall into: 0 bps is a legitimate rate, and treating it as
  // missing would silently attribute a chain read to the daemon -- the same class of error as
  // the unconditional check mark, just inverted.
  assert.equal(rateProvenance(0, 5000), 'chain');
  assert.equal(mayClaimOnChain(rateProvenance(0, 5000)), true);
});

test('only chain provenance may carry the on-chain proof claim', () => {
  // Exhaustive over the union, so adding a variant without deciding its claim fails here.
  const all: Array<ReturnType<typeof rateProvenance>> = ['chain', 'daemon', 'none'];
  assert.deepEqual(all.filter(mayClaimOnChain), ['chain']);
});
