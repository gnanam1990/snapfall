/**
 * The capture gate's refusals, as logic rather than as prose.
 *
 * These exist because I got this exact thing wrong. I wrote a wiring test whose comment claimed
 * "a fixture captured against this stub fails AT-18, so it can never become committed evidence"
 * -- and it was false. `capture-v1-fixture.ts` HARDCODED Circle's endpoints into every fixture it
 * wrote, so any settlement from any facilitator was recorded claiming Circle's URLs, and
 * validateCircleV1Fixture then validated that claim happily. A documented protection that did not
 * exist, in a comment justifying why my own test double was safe.
 *
 * So the two refusals are asserted here directly: a settlement must be a real transaction hash,
 * AND it must have come from Circle.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  CIRCLE_TESTNET_FACILITATOR_ENDPOINTS,
  validateCircleV1Fixture,
} from './circle-facilitator-fixture.js';
import { NOT_BROADCAST, PENDING_SETTLEMENT } from './facilitator.js';
import { DRY_RUN_SETTLEMENTS, isConfirmedSettlement } from './settlement-markers.js';

const TX = `0x${'ef'.repeat(32)}`;

/** Mirrors capture-v1-fixture.ts's settledEndpoints: report what settled, not what we hoped. */
function settledEndpoints(receipt: { facilitator?: { verify: string; settle: string } }) {
  return {
    verify: receipt.facilitator?.verify ?? CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.verify,
    settle: receipt.facilitator?.settle ?? CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.settle,
  };
}

test('a fixture claiming a non-Circle facilitator does not validate', () => {
  // The check that makes the recorded endpoints load-bearing instead of decorative.
  const fixture = {
    x402Version: 1,
    facilitatorEndpoints: settledEndpoints({
      facilitator: { verify: 'http://127.0.0.1:4042/verify', settle: 'http://127.0.0.1:4042/settle' },
    }),
  };
  assert.throws(
    () => validateCircleV1Fixture(fixture),
    /Expected Circle (verify|settle) endpoint/,
    'a locally-settled fixture validated as a Circle payment',
  );
});

test('a fixture from the real endpoints validates', () => {
  const fixture = {
    x402Version: 1,
    facilitatorEndpoints: settledEndpoints({ facilitator: CIRCLE_TESTNET_FACILITATOR_ENDPOINTS }),
  };
  assert.deepEqual(validateCircleV1Fixture(fixture).facilitatorEndpoints, CIRCLE_TESTNET_FACILITATOR_ENDPOINTS);
});

test('every non-settled marker is refused as evidence', () => {
  // Imported from the capture module rather than copied. A local copy passed while the real gate
  // drifted, which is the same class of defect as a mock that cannot express the thing it mocks.
  const DRY_RUN = DRY_RUN_SETTLEMENTS;
  for (const marker of [NOT_BROADCAST, PENDING_SETTLEMENT]) {
    assert.ok(
      DRY_RUN.has(marker.toUpperCase()),
      `${marker} is not in the capture gate's dry-run set, so it would be written as evidence`,
    );
  }
});

test('only a 0x-64 hash counts as a confirmed settlement', () => {
  // The gate's OWN predicate, not a copy. Note 'settled' and 'ok': a marker blacklist would let
  // those through, which is why the check is a whitelist of one shape.
  assert.ok(isConfirmedSettlement(TX));
  for (const bad of ['', '   ', 'NOT_BROADCAST', 'PENDING', 'UNIMPLEMENTED', '0xdeadbeef', `0x${'ab'.repeat(31)}`, 'settled', 'ok']) {
    assert.ok(!isConfirmedSettlement(bad), `"${bad}" would be written as committed evidence of a payment`);
  }
});
