/**
 * Snapfall H3 — durable payment record store (idempotency + status).
 *
 * The review's double-pay fix hinges on this being DURABLE: the record is written before
 * signing and survives a sidecar restart, so a crash mid-payment recovers to the stored
 * record instead of re-executing. A JSON file is enough for a single-process demo sidecar;
 * production would use shared storage. Keyed by the intent nonce (the idempotency key);
 * paymentId is a secondary lookup for the status endpoint.
 */

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
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
    try {
      const arr = JSON.parse(readFileSync(this.path, 'utf8')) as PaymentRecord[];
      for (const r of arr) this.byNonce.set(r.idempotencyNonce, r);
    } catch {
      /* no file yet — start empty */
    }
  }

  private persist(): void {
    mkdirSync(dirname(this.path), { recursive: true });
    writeFileSync(this.path, JSON.stringify([...this.byNonce.values()], null, 2));
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
