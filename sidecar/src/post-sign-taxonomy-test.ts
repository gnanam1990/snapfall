/**
 * The post-sign taxonomy, pinned.
 *
 * buyer.ts documented it correctly and service.ts implemented half of it: FACILITATOR_ERROR was
 * special-cased to RECONCILING while PAYMENT_REJECTED fell through to FAILED. The daemon reads
 * FAILED as "nothing settled, release" (daemon/internal/h3/h3.go, h3.Unresolved), so a signed
 * bearer authorization the seller had already received would have its budget reservation released.
 *
 * buyer.ts even predicted it: "the distinction becomes load-bearing once a real facilitator is
 * wired". Nothing broadcasts today, so this was inert -- and would have gone live on exactly the
 * day everyone was busy watching the facilitator work.
 *
 * These assert the taxonomy as data rather than re-checking one branch, so adding a seventh
 * PaymentCode forces a decision here instead of defaulting it to "safe to release".
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { PaymentFailed, PolicyViolation, POST_SIGN_CODES, isPostSign, type PaymentCode } from './buyer.js';

// Every code buyer.ts defines, split the way its own doc comment splits them.
const PRE_SIGN: PaymentCode[] = [
  'RESOURCE_NOT_FOUND',
  'CHALLENGE_UNAVAILABLE',
  'NO_MATCHING_NETWORK',
  'UPSTREAM_UNREACHABLE',
];
const POST_SIGN: PaymentCode[] = ['PAYMENT_REJECTED', 'FACILITATOR_ERROR'];

test('a rejected payment is post-sign: the seller already holds the authorization', () => {
  // The specific regression. A non-200 to X-PAYMENT does not mean the seller never saw the
  // bearer instrument -- it means the seller saw it and answered no, and it can still be
  // presented for settlement.
  assert.equal(
    isPostSign(new PaymentFailed('PAYMENT_REJECTED', 'seller said no')),
    true,
    'PAYMENT_REJECTED classified as pre-sign: the daemon would release a reservation backing a ' +
      'signed authorization that may still settle',
  );
});

test('every post-sign code must be reconciled, never released', () => {
  for (const code of POST_SIGN) {
    assert.equal(isPostSign(new PaymentFailed(code, 'x')), true, `${code} must be post-sign`);
  }
});

test('every pre-sign code is safe to release', () => {
  for (const code of PRE_SIGN) {
    assert.equal(
      isPostSign(new PaymentFailed(code, 'x')),
      false,
      `${code} classified as post-sign: budget would be held against a payment that was never ` +
        'signed, and the job would starve on money nobody owes',
    );
  }
});

test('the split is exhaustive, so a new code cannot default to "safe to release"', () => {
  // Sorted union compared against the declared PaymentCode surface. If someone adds a seventh
  // code and does not classify it, isPostSign silently returns false for it -- the failure
  // direction that loses money. This makes that a red test instead.
  const classified = [...PRE_SIGN, ...POST_SIGN].sort();
  const declared: PaymentCode[] = [
    'RESOURCE_NOT_FOUND',
    'CHALLENGE_UNAVAILABLE',
    'NO_MATCHING_NETWORK',
    'PAYMENT_REJECTED',
    'UPSTREAM_UNREACHABLE',
    'FACILITATOR_ERROR',
  ];
  assert.deepEqual(
    classified,
    [...declared].sort(),
    'PaymentCode gained or lost a member without this taxonomy being updated. Classify it: does ' +
      'a signature leave the process before this error can be thrown?',
  );

  // And the exported set holds exactly the post-sign ones, no more.
  assert.deepEqual([...POST_SIGN_CODES].sort(), [...POST_SIGN].sort());
});

test('a policy violation is never post-sign, whatever its code', () => {
  // PolicyViolation is thrown BEFORE signing by construction ("NO signature is produced in any
  // of these cases"), so it must never route to RECONCILING and strand budget.
  assert.equal(isPostSign(new PolicyViolation('MERCHANT_CHANGED', 'payee swapped')), false);
  assert.equal(isPostSign(new Error('something else')), false);
  assert.equal(isPostSign(undefined), false);
});
