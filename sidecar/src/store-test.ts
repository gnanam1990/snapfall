/**
 * PaymentStore durability tests (review fixes): atomic write, fail-closed load, and
 * restart survival — the properties the double-pay guard actually rests on.
 *
 *   npm run store:test
 */

import * as fs from 'node:fs';
import { writeFileSync, readFileSync, rmSync, existsSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { PaymentStore, type PaymentRecord, type StoreFs } from './store.js';

/** node:fs with every call logged, and an optional failure injected into one fsync target. */
function recordingFs(log: string[], failFsyncOf?: () => string | null): StoreFs {
  const paths = new Map<number, string>();
  return {
    existsSync: (p) => fs.existsSync(p),
    readFileSync: (p, e) => fs.readFileSync(p, e),
    mkdirSync: (p, o) => fs.mkdirSync(p, o),
    openSync: (p, f) => {
      const fd = fs.openSync(p, f);
      paths.set(fd, p);
      log.push(`open ${p} ${f}`);
      return fd;
    },
    writeSync: (fd, b, o, l) => {
      log.push(`write ${paths.get(fd)}`);
      return fs.writeSync(fd, b, o, l);
    },
    fsyncSync: (fd) => {
      const p = paths.get(fd)!;
      if (failFsyncOf?.() === p) {
        log.push(`fsync ${p} FAILED`);
        throw Object.assign(new Error(`EIO: fsync ${p}`), { code: 'EIO' });
      }
      log.push(`fsync ${p}`);
      fs.fsyncSync(fd);
    },
    closeSync: (fd) => {
      log.push(`close ${paths.get(fd)}`);
      paths.delete(fd);
      fs.closeSync(fd);
    },
    renameSync: (a, b) => {
      log.push(`rename ${a} -> ${b}`);
      fs.renameSync(a, b);
    },
  };
}

let failures = 0;
function check(label: string, ok: boolean, detail = '', failOnlyDetail = false) {
  if (!ok) failures++;
  const shown = failOnlyDetail && ok ? '' : detail;
  console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${label}${shown ? ` — ${shown}` : ''}`);
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

  // 7. A tombstone whose write failed is not "already recorded". The first status answers 500; a
  //    repeat must write again (and fail again while the disk refuses), never return quietly and
  //    let the second status answer 404 with nothing on disk to survive a restart.
  const p7 = tmpPath();
  const s7 = new PaymentStore(p7);
  s7.upsert(rec('ee'));
  mkdirSync(`${p7}.tmp`); // the atomic write's temp path is a directory: every write now throws
  const throws = (fn: () => void) => {
    try {
      fn();
      return false;
    } catch {
      return true;
    }
  };
  check('a tombstone whose write fails throws', throws(() => s7.tombstone('pay_00000000000000a7')));
  check('the id is still refused in this process', s7.isTombstoned('pay_00000000000000a7'));
  check(
    'a repeat while the disk still refuses throws again, so the status stays a 500',
    throws(() => s7.tombstone('pay_00000000000000a7')),
  );
  check('nothing reached the disk yet', !new PaymentStore(p7).isTombstoned('pay_00000000000000a7'));
  rmSync(`${p7}.tmp`, { recursive: true, force: true });
  check('a repeat once the disk recovers succeeds', !throws(() => s7.tombstone('pay_00000000000000a7')));
  check('and the tombstone survives a restart', new PaymentStore(p7).isTombstoned('pay_00000000000000a7'));
  rmSync(p7, { force: true });

  // 8. Durable, not just atomic: the temp file's data is synced before the rename, and the directory
  //    after it, so a power loss after the 404 went out cannot lose a tombstone.
  const p8 = tmpPath();
  const d8 = dirname(p8);
  const log8: string[] = [];
  const s8 = new PaymentStore(p8, recordingFs(log8));
  s8.tombstone('pay_00000000000000a8');
  const at = (entry: string) => log8.indexOf(entry);
  check(
    'the temp file is written, fsynced, then closed before the rename',
    at(`write ${p8}.tmp`) >= 0 &&
      at(`write ${p8}.tmp`) < at(`fsync ${p8}.tmp`) &&
      at(`fsync ${p8}.tmp`) < at(`close ${p8}.tmp`) &&
      at(`close ${p8}.tmp`) < at(`rename ${p8}.tmp -> ${p8}`),
    log8.join(' | '),
    true,
  );
  if (process.platform !== 'win32') {
    check(
      'the directory is fsynced after the rename',
      at(`rename ${p8}.tmp -> ${p8}`) >= 0 && at(`rename ${p8}.tmp -> ${p8}`) < at(`fsync ${d8}`),
      log8.join(' | '),
      true,
    );
  }
  check('the synced tombstone reloads', new PaymentStore(p8).isTombstoned('pay_00000000000000a8'));
  rmSync(d8, { recursive: true, force: true });

  // 9. A rename the disk would not confirm is a failed write: the tombstone call throws (the 404
  //    becomes a 500 and the daemon keeps its hold), and a repeat writes again rather than
  //    returning early on the in-memory entry.
  if (process.platform !== 'win32') {
    const p9 = tmpPath();
    const d9 = dirname(p9);
    let failDir = true;
    const log9: string[] = [];
    const s9 = new PaymentStore(p9, recordingFs(log9, () => (failDir ? d9 : null)));
    let threw = false;
    try {
      s9.tombstone('pay_00000000000000a9');
    } catch {
      threw = true;
    }
    check('a tombstone whose directory fsync fails throws', threw, log9.join(' | '), true);
    check('the id is still refused in this process', s9.isTombstoned('pay_00000000000000a9'));
    failDir = false;
    const before = log9.length;
    s9.tombstone('pay_00000000000000a9');
    check(
      'a repeat after the failed sync writes and syncs again',
      log9.slice(before).includes(`fsync ${d9}`),
      log9.slice(before).join(' | '),
      true,
    );
    rmSync(d9, { recursive: true, force: true });
  }

  // 10. A write that has to create its directories makes them durable too: each new directory is an
  //     entry in its parent, so every parent up to the one that already existed is synced, and a
  //     retry after a failed sync still syncs them (its mkdir finds the directories already there).
  if (process.platform !== 'win32') {
    const root = join(tmpdir(), `snapfall-store-test-${randomBytes(4).toString('hex')}`);
    const p10 = join(root, 'a', 'b', 'payments.json');
    const log10: string[] = [];
    let failParent = true;
    const s10 = new PaymentStore(p10, recordingFs(log10, () => (failParent ? tmpdir() : null)));
    let threw10 = false;
    try {
      s10.tombstone('pay_00000000000000b0');
    } catch {
      threw10 = true;
    }
    check('a write whose new-directory sync fails throws', threw10, log10.join(' | '), true);
    failParent = false;
    const from10 = log10.length;
    s10.tombstone('pay_00000000000000b0');
    const retry10 = log10.slice(from10);
    check(
      'the retry syncs every parent of a directory the store created, up to the one that existed',
      [join(root, 'a'), root, tmpdir()].every((d) => retry10.includes(`fsync ${d}`)),
      retry10.join(' | '),
      true,
    );
    const log10b: string[] = [];
    new PaymentStore(p10, recordingFs(log10b)).tombstone('pay_00000000000000b1');
    check(
      'once the directories exist, only the store directory is synced',
      log10b.includes(`fsync ${join(root, 'a', 'b')}`) && !log10b.includes(`fsync ${join(root, 'a')}`),
      log10b.join(' | '),
      true,
    );
    rmSync(root, { recursive: true, force: true });
  }

  console.log(
    failures === 0
      ? '\nPaymentStore green: atomic write, fail-closed load, restart survival, durable not-found tombstones.\n'
      : `\n${failures} check(s) FAILED.\n`,
  );
  process.exit(failures === 0 ? 0 : 1);
}

main();
