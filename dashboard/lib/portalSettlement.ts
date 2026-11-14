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
 * accept response): exactly one of none | settled | pending | retry | unknown, where `unknown`
 * (HTTP 200) means the daemon could not read the settlement record just now; an earlier daemon
 * answered that case with a 500 and no body the page could use. An older daemon sends no
 * such field; the POST accept response's `state` (accepted-settled / accepted-pending-chain /
 * accepted-settlement-reverted, daemon/internal/brain/accept.go) is then the only settlement fact
 * the portal has, and without it the page must not claim "settled".
 *
 * Pure: no network, no React, so the mapping is tested directly (portalSettlement.test.ts).
 */

export type Settlement = 'none' | 'settled' | 'pending' | 'retry' | 'unknown';

const SETTLEMENTS: readonly Settlement[] = ['none', 'settled', 'pending', 'retry', 'unknown'];

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
 *   unknown     accepted, and the daemon could not read the settlement just now: say exactly that,
 *               offer a re-read, and offer no Accept (nothing says one would help).
 *   recorded    an older daemon: accepted, settlement not stated. Today's behaviour (no Accept
 *               button), without the claim that anything settled.
 */
export type PortalView = 'open' | 'settled' | 'pending' | 'retry' | 'unknown' | 'recorded';

/**
 * The view for a status read, given what this page's own last successful Accept reported (null if
 * it sent none). The daemon's `settlement` on the read is authoritative and newest; the POST's
 * report is used only when the read does not carry one, or when the read could not tell and the
 * POST had seen a recorded settlement (settled is final, so an unreadable later read cannot undo it).
 */
export function portalView(read: AcceptanceRead, lastAccept: Settlement | null): PortalView {
  if (read.settlement === 'unknown') {
    // The settlement was unreadable, not the job: a job that is not accepted is still open.
    if (!read.accepted) return 'open';
    return lastAccept === 'settled' ? 'settled' : 'unknown';
  }
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
    case 'unknown':
    case 'recorded':
      return { label: 'Accepted', tone: '' };
    case 'open':
      return { label: stage ?? 'checking…', tone: stage ? 'Delivered' : '' };
  }
}

/**
 * Whether a refused POST accept was the operator's pause (the kill switch): the daemon answers 423
 * FROZEN (daemon/internal/ownerapi/accept.go) and checks it before claiming anything, so nothing
 * changed. The acceptance read does not carry the freeze, so this refusal is the page's only sign
 * of it; "Try settling again" would only meet it again.
 */
export function isSettlementPaused(status: number, body: unknown): boolean {
  if (status === 423) return true;
  const b = (body && typeof body === 'object' ? body : {}) as { error?: { code?: unknown } };
  return b.error?.code === 'FROZEN';
}

// ── What the page says and offers ───────────────────────────────────────────────────────────

/** Every sentence the settlement area may show. Kept here so the tests can read what is claimed. */
export const PORTAL_COPY = {
  unavailable:
    'We couldn’t read this job’s status right now. Nothing changes while this page is unavailable; try again in a moment.',
  settled: 'Thank you — this delivery is accepted and settled.',
  recorded:
    'Thank you — your acceptance is recorded. This page cannot confirm whether the payment has settled on chain yet; ask the operator if you need that confirmed.',
  open: 'Review is complete. Accepting releases your escrowed payment and settles the job on chain. This cannot be undone.',
  pending: 'Your acceptance is recorded, and the payment is still settling on chain. Check again in a moment.',
  pendingRetrySafe:
    'Your acceptance is recorded, and the payment is still settling on chain. Check again in a moment. Trying again is safe: the job can only settle once.',
  retry:
    'Your acceptance is recorded, but the payment did not settle on chain. It is still held in escrow, and you can try settling again.',
  unknown:
    'Your acceptance is recorded, but we couldn’t read whether the payment has settled just now. Check again in a moment.',
  paused: 'Settlement is paused by the operator. Check again later.',
  walletNotSettled:
    'Your acceptance is recorded, but the payment has not settled on chain yet. Settle it from your wallet below.',
  walletUnconfirmed:
    'Your acceptance is recorded. The payment has not been confirmed as settled yet; check again in a moment.',
  settledOnChain: 'Your payment is settled on chain; the record will update shortly.',
  settledOnChainUnrecorded:
    'Your payment is settled on chain, but your acceptance is not recorded here yet. Record it below; nothing more is paid.',
  settledOnChainPaused:
    'Your payment is settled on chain. Settlement is paused by the operator, so the record will update once it resumes.',
} as const;

export type PortalCopy = keyof typeof PORTAL_COPY;

export interface PortalPlanInput {
  view: PortalView;
  /** The daemon's stage, for the open view's badge. */
  stage: string | null;
  /** The status read failed outright (not a 401): nothing about the job is known. */
  statusUnavailable: boolean;
  /** The customer settles from their own passkey wallet; the custodial Accept is never offered. */
  selfCustody: boolean;
  /**
   * The job's status as JobVault reports it (lib/jobChain.ts), read for a self-custody customer;
   * null when it has not been read or could not be.
   */
  chainStatus: string | null;
  /** This page's last Accept was refused because settlement is paused (isSettlementPaused). */
  paused: boolean;
}

export interface PortalPlan {
  /** The sentence in the live region, or null for none. */
  copy: PortalCopy | null;
  badge: { label: string; tone: string };
  /** Show the customer's own wallet panel (self-custody only). */
  wallet: boolean;
  /** The POST accept, or null when it is not offered. `busy` is its label while it runs. */
  accept: { label: string; busy: string; primary: boolean } | null;
  /** The re-read, or null when it is not offered. */
  checkAgain: { primary: boolean } | null;
}

const ACCEPTED_UNSETTLED: readonly PortalView[] = ['pending', 'retry', 'unknown'];

/**
 * What the page says and offers, decided in one place. Every view that is not settled leaves the
 * customer something that can move it: an Accept that the daemon (or, in self-custody, the chain)
 * would act on, or a re-read when the next fact can only come from elsewhere.
 *
 * Self-custody is read against the chain, because there the settlement is the customer's own
 * transaction and the daemon only records it. The daemon reports `retry` both when the chain is
 * still Delivered (the wallet's accept is what moves it) and when the chain already shows the job
 * Accepted and no record has caught up (nothing to sign: the daemon records what it observes). A
 * passkey sign-in in that second case would lead to a wallet with nothing to do.
 */
export function portalPlan(input: PortalPlanInput): PortalPlan {
  const { view, selfCustody, chainStatus, paused } = input;
  const plain = (label: string) => ({ label, tone: '' });

  if (input.statusUnavailable) {
    return { copy: 'unavailable', badge: plain('status unavailable'), wallet: false, accept: null, checkAgain: { primary: true } };
  }
  const badge = portalBadge(view, input.stage);
  if (view === 'settled') return { copy: 'settled', badge, wallet: false, accept: null, checkAgain: null };
  if (view === 'recorded') return { copy: 'recorded', badge, wallet: false, accept: null, checkAgain: null };

  if (selfCustody && chainStatus === 'Accepted') {
    // The chain is the authority on a self-custody settlement: it happened, whatever the daemon
    // has recorded so far, and there is nothing left for the customer to sign.
    if (view === 'open' && !paused) {
      // Settled from the wallet, but the daemon never heard the Accept (the wallet panel records it
      // once its transaction succeeds; that call can be lost). Recording it is the POST accept,
      // which on a job the chain shows settled records the observation and submits nothing.
      return {
        copy: 'settledOnChainUnrecorded',
        badge: plain('Settled on chain'),
        wallet: false,
        accept: { label: 'Record my acceptance', busy: 'Recording…', primary: true },
        checkAgain: { primary: false },
      };
    }
    return {
      copy: paused ? 'settledOnChainPaused' : 'settledOnChain',
      badge: plain('Settled on chain'),
      wallet: false,
      accept: null,
      checkAgain: { primary: true },
    };
  }

  if (view === 'open') {
    // The pause is checked before anything is claimed, so nothing was accepted.
    if (paused) return { copy: 'paused', badge: plain('Paused'), wallet: false, accept: null, checkAgain: { primary: true } };
    // The re-read stays available beside the wallet: a settlement made from it whose Accept record
    // then failed (or one made elsewhere) is only found by reading the chain again.
    if (selfCustody) return { copy: null, badge, wallet: true, accept: null, checkAgain: { primary: false } };
    return { copy: 'open', badge, wallet: false, accept: { label: 'Accept & settle', busy: 'Settling…', primary: true }, checkAgain: null };
  }

  // Accepted and not settled.
  if (!ACCEPTED_UNSETTLED.includes(view)) throw new Error(`unhandled portal view ${view}`);

  if (selfCustody) {
    // The kill switch stops the daemon, not the chain: the wallet's own accept still settles, and
    // the record follows once settlement resumes.
    if (chainStatus === 'Delivered') {
      return {
        copy: view === 'unknown' ? 'unknown' : 'walletNotSettled',
        badge,
        wallet: true,
        accept: null,
        checkAgain: { primary: false },
      };
    }
    // The chain was not read, or shows another status: the panel reads it itself and explains, and
    // nothing here claims escrow or settlement either way.
    return {
      copy: view === 'pending' ? 'pending' : view === 'unknown' ? 'unknown' : 'walletUnconfirmed',
      badge,
      wallet: true,
      accept: null,
      checkAgain: { primary: true },
    };
  }

  if (paused) return { copy: 'paused', badge: plain('Paused'), wallet: false, accept: null, checkAgain: { primary: true } };
  switch (view) {
    case 'pending':
      return {
        copy: 'pendingRetrySafe',
        badge,
        wallet: false,
        accept: { label: 'Try settling again', busy: 'Settling…', primary: false },
        checkAgain: { primary: true },
      };
    case 'retry':
      return {
        copy: 'retry',
        badge,
        wallet: false,
        accept: { label: 'Try settling again', busy: 'Settling…', primary: true },
        checkAgain: { primary: false },
      };
    default:
      // unknown: the daemon could not say, so nothing says an Accept would help. Re-read.
      return { copy: 'unknown', badge, wallet: false, accept: null, checkAgain: { primary: true } };
  }
}
