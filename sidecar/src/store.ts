/**
 * Snapfall H3 — durable payment record store (idempotency + status).
 *
 * The double-pay fix hinges on this being DURABLE: the record is written before signing
 * and survives a sidecar restart, so a crash mid-payment recovers to the stored record
 * instead of re-executing. A JSON file is enough for a single-process demo sidecar;
 * production would use shared storage. Keyed by the intent nonce (the idempotency key);
 * paymentId is a secondary lookup for the status endpoint.
 *
 * Two review fixes make the durability real rather than nominal:
 *   - Writes are ATOMIC (temp file + rename), so a crash mid-write cannot leave a torn,
 *     unparseable file that erases every prior record.
 *   - Load FAILS CLOSED: an existing-but-unparseable store throws instead of silently
 *     starting empty (which would drop the very records the double-pay guard relies on).
 *     A genuinely absent file still starts empty — that is a fresh start, not corruption.
 */

import { readFileSync, writeFileSync, renameSync, mkdirSync, existsSync } from 'node:fs';
import { dirname } from 'node:path';

export type PaymentState = 'SIGNED' | 'SUBMITTED' | 'DELIVERED' | 'RECONCILING' | 'SETTLED' | 'FAILED' | 'EXPIRED';

export interface PaymentRecord {
  paymentId: string;
  idempotencyNonce: string;
  intentHash: string;
  state: PaymentState;
  amountReserved: string;
  amountPaid: string | null;
  receipt: unknown | null;
  /** The resource payload, so an idempotent replay returns the same result. */
  data: unknown | null;
  authorizationSignature: string | null;
  reason: string | null;
  createdAt: string;
  updatedAt: string;
}

/**
 * A paymentId that `status` has answered PAYMENT_NOT_FOUND for.
 *
 * The daemon releases its budget hold on PAYMENT_NOT_FOUND, so that answer is a promise: nothing
 * was signed for this id AND nothing ever will be. A `pay` can still be in transit when the answer
 * is given (sent, not yet read by this process, so invisible to the in-flight set), and without a
 * fence it would sign after the hold was released. The tombstone is that fence: written durably
 * before the 404 leaves, consulted by `pay` before anything is signed.
 *
 * It lives in the same file as the payment records, with the same lifetime (never pruned), which
 * is longer than any approval it could outlive. Stored as an array element with a `kind`
 * discriminator so the file stays a flat array for readers that scan it for payment records
 * (scripts/spine_run matches on state/amount fields a tombstone does not have).
 */
export interface NotFoundTombstone {
  kind: 'PAYMENT_NOT_FOUND_TOMBSTONE';
  paymentId: string;
  /** When status first reported the id not found. */
  reportedAt: string;
}

const TOMBSTONE_KIND: NotFoundTombstone['kind'] = 'PAYMENT_NOT_FOUND_TOMBSTONE';

export class PaymentStore {
  private byNonce = new Map<string, PaymentRecord>();
  private tombstones = new Map<string, NotFoundTombstone>();
  /** True from the start of a write until one completes: memory holds something the file may not
   *  (a tombstone among it), so the next tombstone() call must write again before it returns. */
  private unflushed = false;

  constructor(private readonly path: string) {
    this.load();
  }

  private load(): void {
    // A missing file is a fresh start. An existing file that will not parse is
    // CORRUPTION, and starting empty would silently drop the records the double-pay
    // guard depends on — so fail closed and make the operator look.
    if (!existsSync(this.path)) return;
    let raw: string;
    try {
      raw = readFileSync(this.path, 'utf8');
    } catch (e) {
      throw new Error(`H3 store ${this.path} exists but could not be read: ${(e as Error).message}`);
    }
    let arr: Array<PaymentRecord | NotFoundTombstone>;
    try {
      arr = JSON.parse(raw) as Array<PaymentRecord | NotFoundTombstone>;
    } catch (e) {
      throw new Error(
        `H3 store ${this.path} is corrupt (${(e as Error).message}). Refusing to start empty — ` +
          `that would drop payment records and reopen the double-pay window. Inspect or remove it deliberately.`,
      );
    }
    if (!Array.isArray(arr)) {
      throw new Error(`H3 store ${this.path} is not a record array; refusing to start.`);
    }
    for (const r of arr) {
      if (r && typeof r === 'object' && (r as NotFoundTombstone).kind === TOMBSTONE_KIND) {
        const t = r as NotFoundTombstone;
        // A tombstone that cannot name its paymentId fences nothing; dropping it silently would
        // reopen the late-pay window it exists to close. Fail closed like any other corruption.
        if (typeof t.paymentId !== 'string' || t.paymentId === '') {
          throw new Error(`H3 store ${this.path} holds a not-found tombstone without a paymentId; refusing to start.`);
        }
        this.tombstones.set(t.paymentId, t);
        continue;
      }
      const rec = r as PaymentRecord;
      this.byNonce.set(rec.idempotencyNonce, rec);
    }
  }

  private persist(): void {
    this.unflushed = true;
    mkdirSync(dirname(this.path), { recursive: true });
    // Atomic write: a crash mid-write leaves the OLD file intact, never a torn one.
    // rename() is atomic on the same filesystem, so a reader (or a restart) always sees
    // a complete document — the same posture as the Go daemon's memory files.
    const tmp = `${this.path}.tmp`;
    writeFileSync(tmp, JSON.stringify([...this.byNonce.values(), ...this.tombstones.values()], null, 2));
    renameSync(tmp, this.path);
    this.unflushed = false;
  }

  getByNonce(nonce: string): PaymentRecord | undefined {
    return this.byNonce.get(nonce);
  }

  getById(paymentId: string): PaymentRecord | undefined {
    for (const r of this.byNonce.values()) if (r.paymentId === paymentId) return r;
    return undefined;
  }

  upsert(rec: PaymentRecord): void {
    this.byNonce.set(rec.idempotencyNonce, rec);
    this.persist();
  }

  /** True once `status` has reported this paymentId not found: no `pay` may sign for it. */
  isTombstoned(paymentId: string): boolean {
    return this.tombstones.has(paymentId);
  }

  /**
   * Durably record that `paymentId` was reported PAYMENT_NOT_FOUND. Idempotent. Synchronous on
   * purpose: the caller writes it and answers in one uninterrupted block, so no `pay` can be
   * accepted between the check that found nothing and the fence going up.
   *
   * The in-memory entry is set BEFORE the write. If the write throws, the caller's 404 never goes
   * out (it becomes a 500, on which the daemon keeps the hold), and this process still refuses the
   * id: both failure directions refuse to sign rather than risk a release behind a signature.
   *
   * A repeat call returns early only once the tombstone is on disk. After a failed write the entry
   * is in memory alone, and returning there would let the next status answer 404 (the daemon
   * releases its hold) with nothing on disk to refuse a delayed pay after a restart. So it writes
   * again, and throws again if the disk still refuses.
   */
  tombstone(paymentId: string, at: string = new Date().toISOString()): void {
    if (this.tombstones.has(paymentId) && !this.unflushed) return;
    if (!this.tombstones.has(paymentId)) {
      this.tombstones.set(paymentId, { kind: TOMBSTONE_KIND, paymentId, reportedAt: at });
    }
    this.persist();
  }
}
