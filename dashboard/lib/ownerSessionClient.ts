/**
 * Browser-side view of the owner session (the server side is lib/ownerApi + lib/ownerSession).
 * The cookie is HttpOnly, so the browser cannot read it; it asks /api/owner/session instead.
 */

/** Dispatched on window after a sign-in or sign-out, so open streams and pages re-read. A change
 *  announced in one tab is re-dispatched in every other tab of this origin (see below). */
export const OWNER_SESSION_EVENT = 'snapfall:owner-session';

/** The BroadcastChannel that carries a session change to this origin's other tabs. */
export const OWNER_SESSION_CHANNEL = 'snapfall-owner-session';

/** The error codes an owner route answers when the visitor is not (or cannot be) signed in. */
export const OWNER_LOCKED_CODES: ReadonlySet<string> = new Set(['OWNER_SIGN_IN_REQUIRED', 'OWNER_ACCESS_NOT_CONFIGURED']);

export type OwnerSessionStatus = { ownerApi: boolean; accessConfigured: boolean; signedIn: boolean };

export async function readOwnerSession(): Promise<OwnerSessionStatus | null> {
  try {
    const res = await fetch('/api/owner/session', { cache: 'no-store' });
    if (!res.ok) return null;
    return (await res.json()) as OwnerSessionStatus;
  } catch {
    return null;
  }
}

/** True when an owner-route response means "sign in first" rather than a daemon answer. */
export function isOwnerLocked(status: number, body: unknown): boolean {
  const code = (body as { error?: { code?: unknown } } | null)?.error?.code;
  return (status === 401 || status === 503) && typeof code === 'string' && OWNER_LOCKED_CODES.has(code);
}

// One channel per tab, used both to post and to listen. A BroadcastChannel never delivers a message
// to the object that posted it, so this tab hears its own change once (the window event), and every
// other tab hears it once (the channel). The cookie is shared by all tabs but HttpOnly, so no tab
// can see a sign-out happen elsewhere; without this, a Jobs tab kept its stream and its owner data
// after the owner signed out in Settings.
let channel: BroadcastChannel | null | undefined;

function sessionChannel(): BroadcastChannel | null {
  if (channel !== undefined) return channel;
  channel = null;
  if (typeof BroadcastChannel === 'undefined' || typeof window === 'undefined') return channel;
  try {
    const opened = new BroadcastChannel(OWNER_SESSION_CHANNEL);
    opened.onmessage = () => window.dispatchEvent(new Event(OWNER_SESSION_EVENT));
    channel = opened;
  } catch {
    /* no cross-tab notice; each tab still learns on its next reconnect */
  }
  return channel;
}

/** Start relaying other tabs' session changes into this tab's OWNER_SESSION_EVENT. Idempotent;
 *  every stream subscriber calls it, so any page with a live stream hears a sign-out elsewhere. */
export function listenForOwnerSessionChanges(): void {
  sessionChannel();
}

export function announceOwnerSessionChange(): void {
  window.dispatchEvent(new Event(OWNER_SESSION_EVENT));
  try {
    sessionChannel()?.postMessage({ kind: 'owner-session-changed' });
  } catch {
    /* a closed channel: this tab's own listeners already heard it */
  }
}

/** Test hook: forget this tab's channel so a test can install a fresh window. */
export function resetOwnerSessionChannelForTests(): void {
  channel?.close();
  channel = undefined;
}
