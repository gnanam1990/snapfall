/**
 * H3 cross-language golden vectors.
 *
 * The daemon (Go) and this sidecar (TypeScript) each compute `intentHash` independently, over a
 * canonical JSON form they must agree on byte for byte. Everything downstream hangs off that
 * agreement: `paymentId` is derived from the hash, `authNonce` is derived from the hash, and the
 * `approvalToken` HMAC is signed over the hash. A single escaping difference between the two
 * implementations therefore does not degrade gracefully, it makes every payment fail with
 * INTENT_HASH_MISMATCH, and it would surface for the first time when the daemon is first wired to
 * the sidecar.
 *
 * The Go side asserts the canonical string byte-for-byte
 * (approval.TestCanonicalWire_ShapeAndGoldenVector) but until now only PRINTED the hash, so
 * nothing pinned the two languages together. These vectors are that pin: the same constants are
 * asserted here and in `daemon/internal/approval/hash_test.go`, so a divergence fails a test in
 * whichever language changed.
 *
 * GV-2 exists because GV-1 contains no character that JSON has to escape. Escaping is precisely
 * where two independent canonicalizers drift, so the second vector carries `&`, `<`, `>` and an
 * escaped double quote. That combination is not arbitrary: Go's encoding/json escapes &, < and >
 * as &, <, > by DEFAULT, and the Go side only matches JS because jsonString calls
 * SetEscapeHTML(false). GV-2 is what proves that call is still there.
 */
import { strict as assert } from 'node:assert';
import test from 'node:test';
import { canonicalIntent, computeIntentHash, computePaymentId, computeAuthNonce, type WireIntent } from './h3.js';

/** GV-1: the vector `daemon/internal/approval/hash_test.go:vectorIntent()` builds. */
const GV1: WireIntent = {
  agentId: 'market-researcher',
  amount: '40000',
  asset: '0x2222222222222222222222222222222222222222',
  expiresAt: '2026-07-22T10:05:00Z',
  intentId: 'pi_9f3c1a2b',
  jobId: 'job_104',
  maxAmount: '40000',
  merchant: '0x1111111111111111111111111111111111111111',
  network: 'eip155:5042002',
  nonce: '0x' + 'ab'.repeat(32),
  policyVersion: 'pol_7',
  purpose: 'Competitor profile for job_104',
  resource: 'http://127.0.0.1:4021/v1/company-profile',
  taskId: 'task_research_01',
  // On the wire but deliberately NOT hashed. `decision` is authenticated separately, by the
  // approvalToken's HMAC over intentHash|decision|approvedAmount|expiresAt, so binding it here
  // too would be redundant; `createdAt` does not affect what is being bought, and `nonce`
  // already supplies uniqueness. The exclusion is pinned by a test below rather than trusted.
  decision: 'AUTO_APPROVE',
  createdAt: '2026-07-22T10:00:00Z',
};

/** GV-2: the same shape, carrying characters a canonicalizer might escape differently. */
const GV2: WireIntent = {
  ...GV1,
  intentId: 'pi_esc_01',
  purpose: 'Benchmark A&B <compare> "quoted" / 50% off',
  resource: 'http://127.0.0.1:4021/v1/benchmark?a=1&b=2',
};

const GV1_CANONICAL =
  '{"agentId":"market-researcher","amount":"40000","asset":"0x2222222222222222222222222222222222222222",' +
  '"expiresAt":"2026-07-22T10:05:00Z","intentId":"pi_9f3c1a2b","jobId":"job_104","maxAmount":"40000",' +
  '"merchant":"0x1111111111111111111111111111111111111111","network":"eip155:5042002",' +
  '"nonce":"0x' + 'ab'.repeat(32) + '","policyVersion":"pol_7",' +
  '"purpose":"Competitor profile for job_104","resource":"http://127.0.0.1:4021/v1/company-profile",' +
  '"taskId":"task_research_01"}';

test('GV-1 canonical form is byte-identical to the Go side', () => {
  // The exact string daemon/internal/approval/hash_test.go asserts as `want`. If this fails, the
  // two canonicalizers disagree about key order, separators, or escaping.
  assert.equal(canonicalIntent(GV1), GV1_CANONICAL);
});

test('GV-1 derived values are pinned across languages', () => {
  const hash = computeIntentHash(GV1);
  assert.equal(hash, GOLDEN.gv1.intentHash);
  // paymentId and authNonce are DERIVED from the hash, so pinning them catches a divergence in
  // the derivation even when the hash itself agrees.
  assert.equal(computePaymentId(hash, GV1.nonce), GOLDEN.gv1.paymentId);
  assert.equal(computeAuthNonce(hash), GOLDEN.gv1.authNonce);
});

test('decision and createdAt are on the wire but outside the hash, deliberately', () => {
  const moved: WireIntent = { ...GV1, decision: 'HUMAN_APPROVED', createdAt: '2030-01-01T00:00:00Z' };
  // Same 14 hashed fields, so the same hash. If this ever fails, either the canonical field list
  // grew or one of these two was added to it, and the Go side must move in the same commit.
  assert.equal(canonicalIntent(moved), canonicalIntent(GV1));
  assert.equal(computeIntentHash(moved), GOLDEN.gv1.intentHash);
});

test('GV-2 pins escaping, which is where canonicalizers drift', () => {
  const hash = computeIntentHash(GV2);
  assert.equal(hash, GOLDEN.gv2.intentHash);
  // JSON escapes the double quote and leaves & < > / alone. A canonicalizer that HTML-escapes
  // (Go's encoding/json does this by default for &, <, > unless SetEscapeHTML(false)) would
  // produce a different string and therefore a different hash. That is the trap this catches.
  const c = canonicalIntent(GV2);
  assert.ok(c.includes('A&B <compare>'), 'ampersand and angle brackets must stay literal');
  assert.ok(c.includes('\\"quoted\\"'), 'double quotes must be backslash-escaped');
  assert.ok(c.includes('?a=1&b=2'), 'query string must survive unescaped');
});

/**
 * The pinned constants. Computed by the shipped sidecar implementation and asserted in
 * BOTH languages, so neither side can drift silently.
 */
export const GOLDEN = {
  gv1: {
    canonical: GV1_CANONICAL,
    intentHash: '0x1230b4badedc3411ed0cb4f53d6be2ad5f7fccda30e9569bbd66c785e97e9e04',
    paymentId: 'pay_dbe42421492fc2e0',
    authNonce: '0x3baaf2b15df9861bb66384e486ad5c61c7b999cd15c94527af6487dc767eb71b',
  },
  gv2: {
    intentHash: '0x6a306e8ebe6e8f3b0ceadf22f9b7d7de490f19a7513e925c617232b6dd0893ed',
  },
} as const;

export { GV1, GV2 };
