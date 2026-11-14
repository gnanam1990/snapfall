/**
 * PaymentStore durability tests (review fixes): atomic write, fail-closed load, and
 * restart survival — the properties the double-pay guard actually rests on.
 *
 *   npm run store:test
 */

import { writeFileSync, readFileSync, rmSync, existsSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { PaymentStore, type PaymentRecord } from './store.js';

let failures = 0;
function check(label: string, ok: boolean, detail = '') {
  if (!ok) failures++;
  console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${label}${detail ? ` — ${detail}` : ''}`);
}

function tmpPath(): string {
  const dir = join(tmpdir(), `snapfall-store-test-${randomBytes(4).toString('hex')}`);
  mkdirSync(dir, { recursive: true });
  return join(dir, 'payments.json');
}

function rec(nonce: string, state: PaymentRecord['state'] = 'DELIVERED'): PaymentRecord {
  return {
    paymentId: 'pay_' + nonce,
    idempotencyNonce: nonce,
    intentHash: '0x' + nonce,
    state,
    amountReserved: '40000',
    amountPaid: state === 'DELIVERED' ? '40000' : null,
    receipt: null,
    data: null,
    authorizationSignature: null,
    reason: null,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

function main() {
  console.log('\nPaymentStore durability tests\n');

  // 1. Restart survival: a new store over the same path reads back prior records.
  const p1 = tmpPath();
  const s1 = new PaymentStore(p1);
  s1.upsert(rec('aa'));
  s1.upsert(rec('bb'));
  const s1b = new PaymentStore(p1); // "restart"
  check('record survives restart by nonce', s1b.getByNonce('aa')?.paymentId === 'pay_aa');
  check('record survives restart by id', s1b.getById('pay_bb')?.idempotencyNonce === 'bb');
  rmSync(p1, { force: true });

  // 2. A genuinely absent file is a fresh start (not an error).
  const p2 = join(tmpdir(), `snapfall-store-absent-${randomBytes(4).toString('hex')}.json`);
  let freshOk = true;
  try {
    const s2 = new PaymentStore(p2);
    freshOk = s2.getByNonce('anything') === undefined;
  } catch {
    freshOk = false;
  }
  check('absent file starts empty (fresh start)', freshOk);

  // 3. Fail closed: an existing-but-corrupt file THROWS instead of silently starting
  //    empty (which would drop the records the double-pay guard relies on).
  const p3 = tmpPath();
  writeFileSync(p3, '{ this is not valid json ]');
  let threw = false;
  try {
    new PaymentStore(p3);
  } catch (e) {
    threw = /corrupt/i.test((e as Error).message);
  }
  check('corrupt store fails closed (throws, not empty)', threw);
  rmSync(p3, { force: true });

  // 4. Atomic write: no .tmp residue after a successful upsert, and the file parses.
  const p4 = tmpPath();
  const s4 = new PaymentStore(p4);
  s4.upsert(rec('cc'));
  check('no .tmp residue after write', !existsSync(`${p4}.tmp`));
  check('written file reloads cleanly', new PaymentStore(p4).getByNonce('cc') !== undefined);
  rmSync(p4, { force: true });

  // 5. Not-found tombstones (the late-pay fence) are durable, idempotent, and kept apart from
  //    payment records, in a file that stays a flat array.
  const p5 = tmpPath();
  const s5 = new PaymentStore(p5);
  s5.upsert(rec('dd'));
  s5.tombstone('pay_0123456789abcdef', '2026-11-14T00:00:00.000Z');
  s5.tombstone('pay_0123456789abcdef', '2026-11-14T09:99:99.000Z'); // idempotent: first report wins
  const s5b = new PaymentStore(p5); // "restart"
  check('tombstone survives restart', s5b.isTombstoned('pay_0123456789abcdef'));
  check('an unreported id is not tombstoned', !s5b.isTombstoned('pay_ffffffffffffffff'));
  check('records survive alongside tombstones', s5b.getByNonce('dd')?.paymentId === 'pay_dd');
  check('a tombstone is never returned as a payment record', s5b.getById('pay_0123456789abcdef') === undefined);
  const onDisk = JSON.parse(readFileSync(p5, 'utf8')) as Array<Record<string, unknown>>;
  check('store file stays a flat array', Array.isArray(onDisk));
  check(
    'tombstone written once, first report kept',
    onDisk.filter((e) => e.kind === 'PAYMENT_NOT_FOUND_TOMBSTONE').length === 1 &&
      onDisk.some((e) => e.reportedAt === '2026-11-14T00:00:00.000Z'),
  );
  rmSync(p5, { force: true });

  // 6. A tombstone that cannot name its id fails closed, like any other corruption.
  const p6 = tmpPath();
  writeFileSync(p6, JSON.stringify([{ kind: 'PAYMENT_NOT_FOUND_TOMBSTONE', reportedAt: '2026-11-14T00:00:00.000Z' }]));
  let threw6 = false;
  try {
    new PaymentStore(p6);
  } catch (e) {
    threw6 = /tombstone/i.test((e as Error).message);
  }
  check('tombstone without paymentId fails closed', threw6);
  rmSync(p6, { force: true });

  console.log(
    failures === 0
      ? '\nPaymentStore green: atomic write, fail-closed load, restart survival, durable not-found tombstones.\n'
      : `\n${failures} check(s) FAILED.\n`,
  );
  process.exit(failures === 0 ? 0 : 1);
}

main();
