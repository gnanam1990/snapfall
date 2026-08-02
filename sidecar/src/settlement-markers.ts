/**
 * What counts as "not actually settled", in one place.
 *
 * This lived inside `capture-v1-fixture.ts`, which is a CLI: importing it to read the set runs
 * the capture. So the test copied the values instead, and a copied set passes while the real gate
 * drifts -- the same class of defect as a mock that cannot express the thing it mocks.
 *
 * Both the gate and its tests import from here now.
 */

import { NOT_BROADCAST, PENDING_SETTLEMENT } from './facilitator.js';

/**
 * Markers that mean no confirmed payment, whatever else they mean.
 *
 * Compared upper-cased by the gate. NOT_BROADCAST and PENDING_SETTLEMENT are pulled from the
 * facilitator rather than retyped, so a rename there cannot leave a marker the gate no longer
 * recognises -- which would let it through as evidence.
 */
export const DRY_RUN_SETTLEMENTS: ReadonlySet<string> = new Set([
  NOT_BROADCAST.toUpperCase(), // the seller's honest marker: nothing was attempted
  PENDING_SETTLEMENT.toUpperCase(), // submitted, outcome unknown; money may still move
  'DRY_RUN',
  'SIMULATED',
  'MOCK',
  'NONE',
  'UNIMPLEMENTED',
]);

/** The only shape that can be opened on an explorer and checked. */
export const TX_HASH_RE = /^0x[0-9a-fA-F]{64}$/;

/**
 * True when this settlement string is evidence of a payment.
 *
 * Deliberately a whitelist of ONE shape rather than a blacklist of markers. A blacklist only
 * catches the words we thought of, so `settled` or `ok` would sail through it; requiring a real
 * transaction hash cannot be talked around.
 */
export function isConfirmedSettlement(settlement: string): boolean {
  const s = settlement.trim();
  if (s === '' || DRY_RUN_SETTLEMENTS.has(s.toUpperCase())) return false;
  return TX_HASH_RE.test(s);
}
