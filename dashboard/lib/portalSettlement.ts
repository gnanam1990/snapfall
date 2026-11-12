/**
 * What the customer portal may say about an accepted delivery's settlement.
 *
 * "Accepted" and "settled" are two different facts. The daemon claims the job (delivery_ready ->
 * accepted) before it touches the chain, and the claim survives a settlement that reverted, was
 * dropped, failed before broadcast, or is still waiting on the chain, precisely so a later Accept
 * can retry it. The portal used to read `accepted: true` as "accepted and settled", hide the
 * Accept button, and thank the customer for a settlement that had not happened, leaving them no
 * way to trigger the retry.
 *
 * The daemon now states the settlement itself (`settlement` on GET .../acceptance and on the POST
 * accept response): exactly one of none | settled | pending | retry. An older daemon sends no
 * such field; the POST accept response's `state` (accepted-settled / accepted-pending-chain /
 * accepted-settlement-reverted, daemon/internal/brain/accept.go) is then the only settlement fact
 * the portal has, and without it the page must not claim "settled".
 *
 * Pure: no network, no React, so the mapping is tested directly (portalSettlement.test.ts).
 */

export type Settlement = 'none' | 'settled' | 'pending' | 'retry';

const SETTLEMENTS: readonly Settlement[] = ['none', 'settled', 'pending', 'retry'];

/** The daemon's POST accept states, as Brain.AcceptDelivery returns them. */
const ACCEPT_STATE_SETTLEMENT: Record<string, Settlement> = {
  'accepted-settled': 'settled',
  'accepted-pending-chain': 'pending',
  'accepted-settlement-reverted': 'retry',
};

function settlementField(value: unknown): Settlement | null {
  return typeof value === 'string' && (SETTLEMENTS as readonly string[]).includes(value) ? (value as Settlement) : null;
}

/** The customer's status read (GET /api/customer/{id}/acceptance), as the portal uses it. */
export interface AcceptanceRead {
  stage: string | null;
  accepted: boolean;
  vaultJobId: string | null;
  /** Null when the daemon did not state it: an older daemon, or a value this page does not know. */
  settlement: Settlement | null;
}

export function parseAcceptance(body: unknown): AcceptanceRead {
  const b = (body && typeof body === 'object' ? body : {}) as Record<string, unknown>;
  return {
    stage: typeof b.stage === 'string' ? b.stage : null,
    accepted: b.accepted === true,
    vaultJobId: typeof b.vaultJobId === 'string' && b.vaultJobId ? b.vaultJobId : null,
    settlement: settlementField(b.settlement),
  };
}

/**
 * The settlement a successful POST accept reported: its `settlement` field when the daemon sends
 * one, otherwise read from its `state`. Null when neither says (an unknown state is not a claim).
 */
export function settlementFromAcceptResponse(body: unknown): Settlement | null {
  const b = (body && typeof body === 'object' ? body : {}) as Record<string, unknown>;
  const stated = settlementField(b.settlement);
  if (stated) return stated;
  return typeof b.state === 'string' ? (ACCEPT_STATE_SETTLEMENT[b.state] ?? null) : null;
}

/**
 * What the portal shows.
 *
 *   open        not accepted yet: the normal Accept.
 *   settled     the settlement is recorded. The only view that may say "settled".
 *   pending     accepted, the settlement is in progress on chain: say so, offer a re-read, and keep
 *               Accept as a safe retry (the daemon settles a job at most once).
 *   retry       accepted, but the settlement did not go through: say so and offer Accept again.
 *   recorded    an older daemon: accepted, settlement not stated. Today's behaviour (no Accept
 *               button), without the claim that anything settled.
 */
export type PortalView = 'open' | 'settled' | 'pending' | 'retry' | 'recorded';

/**
 * The view for a status read, given what this page's own last successful Accept reported (null if
 * it sent none). The daemon's `settlement` on the read is authoritative and newest; the POST's
 * report is used only when the read does not carry one.
 */
export function portalView(read: AcceptanceRead, lastAccept: Settlement | null): PortalView {
  if (read.settlement) return read.settlement === 'none' ? 'open' : read.settlement;
  if (!read.accepted) return 'open';
  if (lastAccept && lastAccept !== 'none') return lastAccept;
  return 'recorded';
}

/** The status badge for a view: its text and the .status-badge tint class. */
export function portalBadge(view: PortalView, stage: string | null): { label: string; tone: string } {
  switch (view) {
    case 'settled':
      return { label: 'Accepted', tone: 'Accepted' };
    case 'pending':
      return { label: 'Settling', tone: 'InProgress' };
    case 'retry':
      return { label: 'Not settled', tone: 'AwaitingFunding' };
    case 'recorded':
      return { label: 'Accepted', tone: '' };
    case 'open':
      return { label: stage ?? 'checking…', tone: stage ? 'Delivered' : '' };
  }
}
