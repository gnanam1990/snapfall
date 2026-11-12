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
 * written, and the caller holds an exclusive Web Lock, so no live tab is behind it. Any other
 * record, and anything unrecognised, blocks a new burn. Protection is per browser, not across
 * devices.
 */

type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem' | 'removeItem' | 'key' | 'length'>;

export interface SubmittedBurn {
  status: 'submitted';
  domain: number;
  userOpHash: Hex;
}

export const burnStorageKey = (jobId: string) => `snapfall.cctp.${jobId.toLowerCase()}`;

const HASH = /^0x[0-9a-fA-F]{64}$/;
const BLOCKED = 'A prior transfer exists or its outcome is unknown. Do not burn again; resume or recover it first.';

/** Burns started in this tab and not yet settled into a record; the Web Lock covers other tabs. */
const inFlight = new Set<string>();

function parse(raw: string | null): unknown {
  if (raw === null) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function isSigning(v: unknown): boolean {
  return typeof v === 'object' && v !== null && (v as { status?: unknown }).status === 'signing';
}

/** A saved, broadcast-or-maybe-broadcast burn whose transaction hash is not known yet. */
export function parseSubmittedBurn(raw: string | null, domains: number[]): SubmittedBurn | null {
  const v = parse(raw) as Partial<SubmittedBurn> | null;
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
  const prior = parse(storage.getItem(key));
  if (prior !== null && !isSigning(prior)) throw new Error(BLOCKED);

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
        try {
          storage.removeItem(key);
        } catch {
          // A leftover `signing` record is replaceable, so this cannot block a retry.
        }
        throw new Error(`Nothing was sent; you can try again. ${message}`);
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

/** Turn a `submitted` record into a known burn by asking the bundler what became of it. */
export async function resumeSubmitted(
  storage: Storage,
  key: string,
  submitted: SubmittedBurn,
  confirm: (userOpHash: Hex) => Promise<string>,
): Promise<PendingBurn> {
  let tx: string;
  try {
    tx = await confirm(submitted.userOpHash);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(
      `Your earlier transfer (user operation ${submitted.userOpHash}) is not confirmed yet. Do not burn again; try finishing it later. ${message}`,
    );
  }
  if (!HASH.test(tx)) throw new Error(`The wallet returned an invalid transaction hash for user operation ${submitted.userOpHash}.`);
  const pending = { domain: submitted.domain, tx: tx as Hex } satisfies PendingBurn;
  write(storage, key, pending);
  return pending;
}

/** The job was paid: keep a marker so a stale tab cannot burn for it again. */
export function markBurnCompleted(storage: Storage, key: string): void {
  storage.setItem(key, JSON.stringify({ status: 'completed' }));
}

/**
 * The burn was minted but did not cover the price, so nothing was paid. Nothing is pending any
 * more, and the customer needs to be able to bring the difference over, so the record goes.
 */
export function clearMintedBurn(storage: Storage, key: string): void {
  storage.removeItem(key);
}
