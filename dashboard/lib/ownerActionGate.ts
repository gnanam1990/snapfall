/**
 * The fence between an owner action in flight (a customer-link mint) and the session it started
 * under, kept out of the page so it can be tested without React.
 *
 * The page used to learn that owner data was hidden from a React effect, which runs after the
 * commit that hid it: a mint response landing in between still stored the link and wrote it to the
 * clipboard. The gate is told about every stream status synchronously, as the stream client reports
 * it (useEventStream's onStatusNow), so it is closed before React has even scheduled the render.
 */

import { ownerActionsBlocked, type StreamStatus } from './streamClient';

export interface OwnerActionGate {
  /** Every status the stream reports, in order and synchronously. */
  observe(status: StreamStatus): void;
  /**
   * Start an action. Null when owner actions are blocked right now: the action must not start.
   * Otherwise a check, true while the action's result may still be used: no status since has
   * blocked owner actions (even one that was over again before the result arrived, since that
   * means the session changed), and no later action has started.
   */
  begin(): (() => boolean) | null;
}

export function createOwnerActionGate(initial: StreamStatus = 'connecting'): OwnerActionGate {
  let blocked = ownerActionsBlocked(initial);
  let generation = 0;
  return {
    observe(status) {
      if (ownerActionsBlocked(status)) {
        blocked = true;
        generation += 1;
      } else {
        blocked = false;
      }
    },
    begin() {
      if (blocked) return null;
      const mine = ++generation;
      return () => !blocked && generation === mine;
    },
  };
}
