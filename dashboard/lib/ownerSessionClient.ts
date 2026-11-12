/**
 * Browser-side view of the owner session (the server side is lib/ownerApi + lib/ownerSession).
 * The cookie is HttpOnly, so the browser cannot read it; it asks /api/owner/session instead.
 */

/** Dispatched on window after a sign-in or sign-out, so open streams and pages re-read. */
export const OWNER_SESSION_EVENT = 'snapfall:owner-session';

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

export function announceOwnerSessionChange(): void {
  window.dispatchEvent(new Event(OWNER_SESSION_EVENT));
}
