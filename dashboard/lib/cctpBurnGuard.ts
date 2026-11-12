import type { Hex } from 'viem';
import type { PendingBurn } from './cctp';

/**
 * The browser-side record that stops a CCTP burn from happening twice for one job.
 *
 * A burn moves to the next state only after the previous one is saved:
 *
 *   signing    the passkey is being asked; nothing can have been broadcast yet
 *   submitted  the signed user operation's hash is saved, THEN it is broadcast; the outcome is
 *              looked up from that hash (`resumeSubmitted`), never guessed
 *   {domain, tx}  the burn transaction is known; the panel waits for Circle and finishes
 *   completed  the job was paid; a stale tab must not burn again
 *
 * A `signing` record is safe to replace: broadcast only ever happens after `submitted` is
 * written, and every writer holds the job's exclusive Web Lock, so no live tab is behind it.
 * Any other record, and anything unrecognised, blocks a new burn. A user operation that was
 * included but whose call reverted moved nothing (the batch reverts as one), so its record is
 * cleared and the customer may try again. Protection is per browser, not across devices.
 */

type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem' | 'removeItem' | 'key' | 'length'>;

export interface SubmittedBurn {
  status: 'submitted';
  domain: number;
  userOpHash: Hex;
}

/** Thrown by a wallet when a user operation was included and its call reverted: final, nothing moved. */
export class UserOperationFailedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'UserOperationFailedError';
  }
}

const isFinalFailure = (e: unknown) => e instanceof Error && e.name === 'UserOperationFailedError';

export const burnStorageKey = (jobId: string) => `snapfall.cctp.${jobId.toLowerCase()}`;

const HASH = /^0x[0-9a-fA-F]{64}$/;
const BLOCKED = 'A prior transfer exists or its outcome is unknown. Do not burn again; resume or recover it first.';

/** Burns started in this tab and not yet settled into a record; the Web Lock covers other tabs. */
const inFlight = new Set<string>();

const ABSENT = Symbol('absent');

/** The stored record: ABSENT when the key is missing, the parsed JSON, or the raw string if it is not JSON. */
function read(storage: Storage, key: string): unknown {
  const raw = storage.getItem(key);
  if (raw === null) return ABSENT;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function isSigning(v: unknown): boolean {
  return typeof v === 'object' && v !== null && (v as { status?: unknown }).status === 'signing';
}

function isCompleted(v: unknown): boolean {
  return typeof v === 'object' && v !== null && (v as { status?: unknown }).status === 'completed';
}

function asKnownBurn(v: unknown): PendingBurn | null {
  if (typeof v !== 'object' || v === null) return null;
  const { domain, tx } = v as { domain?: unknown; tx?: unknown };
  if (typeof domain !== 'number' || typeof tx !== 'string' || !HASH.test(tx)) return null;
  return { domain, tx: tx as Hex };
}

/** A saved, broadcast-or-maybe-broadcast burn whose transaction hash is not known yet. */
export function parseSubmittedBurn(raw: string | null, domains: number[]): SubmittedBurn | null {
  if (raw === null) return null;
  let v: Partial<SubmittedBurn> | null;
  try {
    v = JSON.parse(raw) as Partial<SubmittedBurn> | null;
  } catch {
    return null;
  }
  if (!v || typeof v !== 'object' || v.status !== 'submitted') return null;
  if (typeof v.domain !== 'number' || !domains.includes(v.domain)) return null;
  if (typeof v.userOpHash !== 'string' || !HASH.test(v.userOpHash)) return null;
  return { status: 'submitted', domain: v.domain, userOpHash: v.userOpHash as Hex };
}

function write(storage: Storage, key: string, value: unknown): void {
  const json = JSON.stringify(value);
  storage.setItem(key, json);
  if (storage.getItem(key) !== json) throw new Error('Cannot save recovery state.');
}

/** Remove the record; false if storage refused or the record is still there. */
function remove(storage: Storage, key: string): boolean {
  try {
    storage.removeItem(key);
  } catch {
    return false;
  }
  return storage.getItem(key) === null;
}

/**
 * Burn at most once. Call under an exclusive Web Lock on `key`. `send` must call
 * `beforeBroadcast(userOpHash)` after signing and before broadcasting, and must not broadcast if
 * it throws.
 */
export async function submitBurnOnce(
  storage: Storage,
  key: string,
  domain: number,
  send: (beforeBroadcast: (userOpHash: Hex) => void) => Promise<string>,
): Promise<PendingBurn> {
  if (inFlight.has(key)) throw new Error(BLOCKED);
  // Legacy mixed-case keys: the same bytes32 job written under another spelling still blocks.
  for (let i = 0; i < storage.length; i++) {
    const existing = storage.key(i);
    if (existing && existing !== key && existing.toLowerCase() === key.toLowerCase() && storage.getItem(existing) !== null) {
      throw new Error(BLOCKED);
    }
  }
  const prior = read(storage, key);
  if (prior !== ABSENT && !isSigning(prior)) throw new Error(BLOCKED);

  inFlight.add(key);
  try {
    try {
      write(storage, key, { status: 'signing', domain });
    } catch {
      throw new Error('Cannot save recovery state. Nothing was submitted.');
    }
    let broadcastMayHaveHappened = false;
    let userOpHash: Hex | null = null;
    let tx: string;
    try {
      tx = await send((hash) => {
        if (!HASH.test(hash)) throw new Error('The wallet produced an invalid user operation hash. Nothing was submitted.');
        try {
          write(storage, key, { status: 'submitted', domain, userOpHash: hash });
        } catch {
          throw new Error('Cannot save recovery state. Nothing was submitted.');
        }
        userOpHash = hash;
        broadcastMayHaveHappened = true;
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!broadcastMayHaveHappened) {
        // Declined passkey, gas or sponsorship refusal, or a save failure: nothing left this tab.
        // A leftover `signing` record is replaceable, so a failed removal cannot block a retry.
        remove(storage, key);
        throw new Error(`Nothing was sent; you can try again. ${message}`);
      }
      if (isFinalFailure(error) && remove(storage, key)) {
        throw new Error(`The transfer failed on chain and no USDC moved; you can try again. ${message}`);
      }
      throw new Error(
        `Your transfer was sent but is not confirmed yet (user operation ${userOpHash}). Do not burn again; reload this page to finish it. ${message}`,
      );
    }
    if (!HASH.test(tx)) {
      throw new Error(`The wallet returned an invalid transaction hash for user operation ${userOpHash}. Do not burn again; reload this page to finish it.`);
    }
    const pending = { domain, tx: tx as Hex } satisfies PendingBurn;
    try {
      write(storage, key, pending);
    } catch {
      // The `submitted` record stays, so a reload still finds this burn by its user operation.
      throw new Error(`Transfer submitted: ${tx}. Recovery state could not be updated. Do not burn again; reload this page to finish it.`);
    }
    return pending;
  } finally {
    inFlight.delete(key);
  }
}

/**
 * Turn a `submitted` record into a known burn by asking the bundler what became of it. Call
 * under the job's Web Lock: the record is re-read first, because another tab may already have
 * resolved or finished it.
 */
export async function resumeSubmitted(
  storage: Storage,
  key: string,
  submitted: SubmittedBurn,
  confirm: (userOpHash: Hex) => Promise<string>,
): Promise<PendingBurn> {
  const current = read(storage, key);
  const known = asKnownBurn(current);
  if (known) return known;
  if (isCompleted(current)) throw new Error('This job was already paid from another tab or window.');
  const same = parseSubmittedBurn(typeof current === 'object' ? JSON.stringify(current) : null, [submitted.domain]);
  if (!same || same.userOpHash.toLowerCase() !== submitted.userOpHash.toLowerCase()) {
    throw new Error('The saved transfer changed in another tab or window. Reload this page.');
  }
  let tx: string;
  try {
    tx = await confirm(submitted.userOpHash);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (isFinalFailure(error) && remove(storage, key)) {
      throw new Error(`Your earlier transfer failed on chain and no USDC moved; you can start again. ${message}`);
    }
    throw new Error(
      `Your earlier transfer (user operation ${submitted.userOpHash}) is not confirmed yet. Do not burn again; try finishing it later. ${message}`,
    );
  }
  if (!HASH.test(tx)) throw new Error(`The wallet returned an invalid transaction hash for user operation ${submitted.userOpHash}.`);
  const pending = { domain: submitted.domain, tx: tx as Hex } satisfies PendingBurn;
  write(storage, key, pending);
  return pending;
}

/** Whether the stored record is still exactly this burn (not finished or replaced elsewhere). */
export function isCurrentBurn(storage: Storage, key: string, burn: PendingBurn): boolean {
  const known = asKnownBurn(read(storage, key));
  return known !== null && known.domain === burn.domain && known.tx.toLowerCase() === burn.tx.toLowerCase();
}

/** The job was paid by this burn: keep a marker so a stale tab cannot burn for it again. */
export function markBurnCompleted(storage: Storage, key: string, burn: PendingBurn): void {
  if (isCurrentBurn(storage, key, burn)) storage.setItem(key, JSON.stringify({ status: 'completed' }));
}

/**
 * This burn was minted but did not cover the price, so nothing was paid. Nothing is pending any
 * more and the customer must be able to bring the difference, so its record goes. Only this
 * burn's record is removed; false if storage would not remove it (another burn stays blocked).
 */
export function clearMintedBurn(storage: Storage, key: string, burn: PendingBurn): boolean {
  if (!isCurrentBurn(storage, key, burn)) return false;
  return remove(storage, key);
}
