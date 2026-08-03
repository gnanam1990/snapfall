/**
 * The rule these tests defend: a paid resource goes out on a CONFIRMED settlement, or on an
 * explicitly acknowledged local demo, and never otherwise.
 *
 * The version of this rule that shipped had the opposite bug. `outcome.state !== 'settled' &&
 * facilitator.isConfigured()` meant that with no CIRCLE_API_KEY -- the default configuration --
 * the entire withholding branch was skipped and the seller handed the resource over for free.
 * The comment above it said "Goods are released on CONFIRMED payment only". No test contradicted
 * either, because the decision could not be reached without an HTTP server and a signed
 * authorization.
 *
 * So the mutation that matters most here is the original bug: making 'not-configured' deliver
 * unconditionally. `npm run test:release-policy` fails on it.
 */

import test from 'node:test';
import assert from 'node:assert/strict';
import { releaseDecision, type SettlementState } from './release-policy.js';

test('a confirmed settlement delivers, wherever the seller is bound', () => {
  assert.equal(releaseDecision('settled', true), 'deliver');
  assert.equal(releaseDecision('settled', false), 'deliver');
});

test('no facilitator delivers on loopback: the local demo concession, named and bounded', () => {
  // npm run demo:loop and the spine run's beats 3 and 5 depend on exactly this.
  assert.equal(releaseDecision('not-configured', true), 'deliver');
});

test('no facilitator REFUSES once the seller is reachable beyond loopback', () => {
  // The defect, stated as a test. This returned 'deliver' in every configuration before.
  assert.equal(releaseDecision('not-configured', false), 'refuse-unsettled');
});

test('a declined payment is refused, on loopback or not', () => {
  assert.equal(releaseDecision('rejected', true), 'refuse-rejected');
  assert.equal(releaseDecision('rejected', false), 'refuse-rejected');
});

test('an UNKNOWN settlement is refused even on loopback', () => {
  // The asymmetry is deliberate and is the reason this is not simply "loopback delivers".
  // 'not-configured' means nothing was attempted, so nothing can have moved. 'unknown' means the
  // money may be in flight, and delivering against that is giving the product away on a hope.
  assert.equal(releaseDecision('unknown', true), 'refuse-unknown');
  assert.equal(releaseDecision('unknown', false), 'refuse-unknown');
});

test('every state is decided, and only settled or the loopback demo ever delivers', () => {
  // Exhaustive over the union, so a state added later cannot default into delivering.
  const states: SettlementState[] = ['settled', 'not-configured', 'rejected', 'unknown'];
  const delivering = states
    .flatMap((s) => [true, false].map((ok) => ({ s, ok, d: releaseDecision(s, ok) })))
    .filter((r) => r.d === 'deliver');

  assert.deepEqual(
    delivering.map((r) => `${r.s}/${r.ok ? 'loopback' : 'network'}`).sort(),
    ['not-configured/loopback', 'settled/loopback', 'settled/network'],
  );
});
