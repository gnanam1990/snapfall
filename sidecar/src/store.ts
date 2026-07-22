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

export class PaymentStore {
  private byNonce = new Map<string, PaymentRecord>();

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
    let arr: PaymentRecord[];
    try {
      arr = JSON.parse(raw) as PaymentRecord[];
    } catch (e) {
      throw new Error(
        `H3 store ${this.path} is corrupt (${(e as Error).message}). Refusing to start empty — ` +
          `that would drop payment records and reopen the double-pay window. Inspect or remove it deliberately.`,
      );
    }
    if (!Array.isArray(arr)) {
      throw new Error(`H3 store ${this.path} is not a record array; refusing to start.`);
    }
    for (const r of arr) this.byNonce.set(r.idempotencyNonce, r);
  }

  private persist(): void {
    mkdirSync(dirname(this.path), { recursive: true });
    // Atomic write: a crash mid-write leaves the OLD file intact, never a torn one.
    // rename() is atomic on the same filesystem, so a reader (or a restart) always sees
    // a complete document — the same posture as the Go daemon's memory files.
    const tmp = `${this.path}.tmp`;
    writeFileSync(tmp, JSON.stringify([...this.byNonce.values()], null, 2));
    renameSync(tmp, this.path);
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
}
