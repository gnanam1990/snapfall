import type { PendingBurn } from './cctp';
type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem' | 'key' | 'length'>;
export const burnStorageKey = (jobId: string) => `snapfall.cctp.${jobId.toLowerCase()}`;
/** Call under an exclusive Web Lock. Preserve ambiguous wallet submissions, never reburn. */
export async function submitBurnOnce(storage: Storage, key: string, domain: number, send: () => Promise<string>): Promise<PendingBurn> {
  // Preserve legacy mixed-case keys: identical bytes32 jobs share one lock/identity.
  for (let i = 0; i < storage.length; i++) {
    const existing = storage.key(i);
    if (existing && existing.toLowerCase() === key.toLowerCase() && storage.getItem(existing) !== null) {
      throw new Error('A prior transfer exists or its outcome is unknown. Do not burn again; resume or recover it first.');
    }
  }
  if (storage.getItem(key) !== null) throw new Error('A prior transfer exists or its outcome is unknown. Do not burn again; resume or recover it first.');
  const marker = JSON.stringify({ status: 'submitting-or-unknown', domain });
  storage.setItem(key, marker);
  if (storage.getItem(key) !== marker) throw new Error('Cannot save recovery state. Nothing was submitted.');
  let tx: string;
  try { tx = await send(); }
  catch (error) {
    throw new Error(`Transfer submission outcome is unknown. Do not burn again; inspect or recover the existing transfer first. ${error instanceof Error ? error.message : String(error)}`);
  }
  const pending = { domain, tx } as PendingBurn;
  try { storage.setItem(key, JSON.stringify(pending)); }
  catch { throw new Error(`Transfer submitted: ${tx}. Recovery state could not be saved. Do not burn again; recover this transaction.`); }
  return pending;
}
