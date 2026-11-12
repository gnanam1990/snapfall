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

import {
  DEFAULT_PROBE_TIMEOUT_MS,
  MAX_TIMER_MS,
  PaymentFailed,
  PolicyViolation,
  POST_SIGN_CODES,
  SendDeadlinePassed,
  SigningFailed,
  isPostSign,
  parseProbeTimeoutMs,
  provenNotSent,
  receiptProblem,
  type ApprovedIntent,
  type PaymentCode,
  type PurchaseResult,
} from './buyer.js';
import type { PaymentPayload } from './x402.js';

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

// ── after the write-ahead: FAILED only for a proven non-settlement ────────────────────────────
//
// service.ts records FAILED (daemon: release) only when provenNotSent says so, and RECONCILING
// (daemon: hold) for everything else. The previous rule asked isPostSign instead, so any
// exception that was not one of its two codes -- a TypeError on a 200 without a receipt, a null
// body -- was recorded FAILED although X-PAYMENT had already been delivered.

test('only a signature that never left the process is a proven non-settlement', () => {
  assert.equal(provenNotSent(new SigningFailed('the signer refused')), true);
  // Signed, but the approval lapsed before X-PAYMENT was started, so it was discarded unsent.
  assert.equal(provenNotSent(new SendDeadlinePassed('the approval expired while signing')), true);
});

test('every other failure after the write-ahead is indeterminate, whatever its type', () => {
  const indeterminate: Array<[string, unknown]> = [
    ...[...PRE_SIGN, ...POST_SIGN].map((c): [string, unknown] => [`PaymentFailed ${c}`, new PaymentFailed(c, 'x')]),
    ['TypeError on a receipt-less 200', new TypeError("Cannot read properties of undefined (reading 'amount')")],
    ['SyntaxError from a body parse', new SyntaxError('Unexpected token')],
    ['plain Error', new Error('something else')],
    ['PolicyViolation', new PolicyViolation('PRICE_CHANGED', 'x')],
    ['a thrown string', 'boom'],
    ['undefined', undefined],
    ['null', null],
  ];
  for (const [name, e] of indeterminate) {
    assert.equal(
      provenNotSent(e),
      false,
      `${name} read as proven-unsent: service.ts would record FAILED and the daemon would release the ` +
        'hold behind an authorization the seller may already hold',
    );
  }
});

// ── what a 200's receipt must say before it can be DELIVERED ──────────────────────────────────
//
// DELIVERED is booked by the daemon as committed spend. Five strings were enough to get there, so a
// receipt saying PENDING, or naming another nonce or amount, recorded a payment as done. The
// end-to-end cases live in service-post-sign-test.ts; these pin each rule of receiptProblem.

const SIGNER = '0x1111111111111111111111111111111111111111';
const PAYEE = '0x000000000000000000000000000000000000dEaD';
const NONCE = `0x${'ab'.repeat(32)}`;
const signed: PaymentPayload = {
  x402Version: 2,
  scheme: 'exact',
  network: 'eip155:5042002',
  payload: {
    signature: `0x${'00'.repeat(65)}`,
    authorization: { from: SIGNER, to: PAYEE, value: '25000', validAfter: '0', validBefore: '9999999999', nonce: NONCE },
  },
};
const approved: ApprovedIntent = {
  intentId: 'pi_1', jobId: 'j', taskId: 't', agentId: 'a', resource: 'http://127.0.0.1/x',
  maxAmount: 25000n, decision: 'AUTO_APPROVE', policyVersion: 'p', merchant: PAYEE, amount: 25000n,
};
const good: PurchaseResult['receipt'] = {
  resource: '/x', amount: '25000', payer: SIGNER, payee: PAYEE, nonce: NONCE, settlement: 'NOT_BROADCAST',
};

test('a receipt for exactly the signed authorization, with a resolved settlement, is usable', () => {
  assert.equal(receiptProblem(good, signed, approved), null);
  assert.equal(receiptProblem({ ...good, settlement: `0x${'cd'.repeat(32)}` }, signed, approved), null);
  // Case-insensitive where the value is hex: the seller canonicalises the nonce, addresses are EIP-55.
  assert.equal(
    receiptProblem({ ...good, nonce: NONCE.toUpperCase().replace('0X', '0x'), payee: PAYEE.toLowerCase() }, signed, approved),
    null,
  );
});

test('an unresolved settlement is never usable, so it is RECONCILING, not DELIVERED', () => {
  for (const settlement of ['PENDING', 'pending', '', '  ', 'settled', 'ok', 'DRY_RUN', 'SIMULATED', 'MOCK', '0x1234']) {
    assert.match(receiptProblem({ ...good, settlement }, signed, approved) ?? '', /settlement/, `settlement ${JSON.stringify(settlement)} accepted`);
  }
});

test('every signed field must match the signed authorization and the approved intent', () => {
  const bad: Array<[string, Partial<PurchaseResult['receipt']>, RegExp]> = [
    ['another amount', { amount: '24999' }, /amount/],
    ['a non-canonical amount', { amount: '025000' }, /canonical/],
    ['a decimal amount', { amount: '0.025' }, /canonical/],
    ['another payer', { payer: `0x${'22'.repeat(20)}` }, /payer/],
    ['another payee', { payee: `0x${'33'.repeat(20)}` }, /payee/],
    ['another nonce', { nonce: `0x${'cd'.repeat(32)}` }, /nonce/],
  ];
  for (const [name, change, why] of bad) {
    assert.match(receiptProblem({ ...good, ...change }, signed, approved) ?? '', why, `a receipt with ${name} was accepted`);
  }
  // Matching the authorization is not enough if the authorization itself diverged from the approval.
  assert.match(receiptProblem(good, signed, { ...approved, amount: 1n }) ?? '', /approved amount/);
  assert.match(receiptProblem(good, signed, { ...approved, merchant: `0x${'44'.repeat(20)}` }) ?? '', /approved merchant/);
});

// ── SIDECAR_PROBE_TIMEOUT_MS: only a delay a Node timer can honour ────────────────────────────

test('a whole number of milliseconds within the timer range is used as given', () => {
  const warnings: string[] = [];
  for (const [raw, want] of [['1', 1], ['4000', 4000], [' 2500 ', 2500], [String(MAX_TIMER_MS), MAX_TIMER_MS]] as const) {
    assert.equal(parseProbeTimeoutMs(raw, (m) => warnings.push(m)), want);
  }
  assert.deepEqual(warnings, []);
  // And the ceiling really is usable: a signal at it has not fired straight away.
  const s = AbortSignal.timeout(MAX_TIMER_MS);
  assert.equal(s.aborted, false);
});

test('unset or empty falls back to the default without a warning', () => {
  const warnings: string[] = [];
  assert.equal(parseProbeTimeoutMs(undefined, (m) => warnings.push(m)), DEFAULT_PROBE_TIMEOUT_MS);
  assert.equal(parseProbeTimeoutMs('', (m) => warnings.push(m)), DEFAULT_PROBE_TIMEOUT_MS);
  assert.deepEqual(warnings, []);
});

test('a fractional, non-positive, oversized or garbled value falls back to the default with a warning', () => {
  // 0.5 makes AbortSignal.timeout throw ERR_OUT_OF_RANGE; 2^31 and 3e9 are clamped by Node to 1 ms.
  for (const raw of ['0.5', '1.5', '0', '-1', String(MAX_TIMER_MS + 1), '3000000000', '1e3', '0x10', 'abc', 'Infinity', 'NaN']) {
    const warnings: string[] = [];
    assert.equal(parseProbeTimeoutMs(raw, (m) => warnings.push(m)), DEFAULT_PROBE_TIMEOUT_MS, `${raw} was used as a timeout`);
    assert.equal(warnings.length, 1, `${raw} fell back silently`);
    assert.match(warnings[0] ?? '', /SIDECAR_PROBE_TIMEOUT_MS/);
  }
  // Confirm the premise against the running Node: both kinds really are unusable as given.
  assert.throws(() => AbortSignal.timeout(0.5), { code: 'ERR_OUT_OF_RANGE' });
});
