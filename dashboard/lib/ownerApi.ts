/**
 * Owner-API proxy helpers. The browser talks to Next.js route handlers; the handlers
 * talk to the daemon's H2 owner API server-side. This keeps the daemon on its loopback
 * posture (no CORS, no browser-exposed token) and lets the mock stand in when
 * SNAPFALL_OWNER_API_URL is unset (dev without a running daemon).
 *
 * Same env the SSE stream route already reads:
 *   SNAPFALL_OWNER_API_URL  e.g. http://127.0.0.1:4010/api/v1  (unset ⇒ mock/empty)
 *   SNAPFALL_OWNER_TOKEN    the bearer, forwarded only server-side when set
 */

export function ownerApiBase(): string | null {
  return process.env.SNAPFALL_OWNER_API_URL?.replace(/\/$/, '') ?? null;
}

export function ownerAuthHeaders(extra?: HeadersInit): Headers {
  const h = new Headers(extra);
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) h.set('authorization', `Bearer ${token}`);
  return h;
}

/** Forward an upstream JSON response verbatim — status AND body — so the client sees
 *  the daemon's real codes (409 STALE_VIEW, 410 APPROVAL_EXPIRED, …), never a flattened
 *  200. */
export function forwardJSON(status: number, body: string): Response {
  return new Response(body, { status, headers: { 'content-type': 'application/json' } });
}
