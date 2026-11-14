/**
 * Owner/customer-API proxy helpers. The browser talks to Next.js route handlers; the
 * handlers talk to the daemon's H2 API server-side. This keeps the daemon on its
 * loopback posture and lets the mock stand in when SNAPFALL_OWNER_API_URL is unset.
 *
 *   SNAPFALL_OWNER_API_URL            e.g. http://127.0.0.1:4010/api/v1  (unset ⇒ mock/empty)
 *   SNAPFALL_OWNER_TOKEN              the OWNER bearer, forwarded only server-side, owner routes only
 *   SNAPFALL_DASHBOARD_OWNER_SECRET   what the owner types to sign in to THIS dashboard (≥ 32 chars,
 *                                     distinct from the daemon token). Required whenever the owner
 *                                     API is configured: without it every owner route refuses.
 *   SNAPFALL_DASHBOARD_HOSTS          optional comma-separated Host allowlist (e.g.
 *                                     `dash.example.com,localhost:3000`) for owner and sign-in routes.
 *                                     Matched against the request host (below).
 *   SNAPFALL_DASHBOARD_ORIGIN         optional comma-separated public origin(s) the owner's browser
 *                                     uses, e.g. `https://dash.example.com`. When set, a mutation's
 *                                     Origin must equal one of them exactly (scheme, host, port) and
 *                                     the Host header plays no part in the CSRF check. The fix for a
 *                                     reverse proxy that rewrites Host. A malformed entry fails closed.
 *   SNAPFALL_DASHBOARD_TRUST_PROXY    `1` only when a reverse proxy you control is in front and SETS
 *                                     X-Forwarded-Host. Then the request host is the last
 *                                     X-Forwarded-Host value instead of Host. Unset, X-Forwarded-Host
 *                                     is ignored: without a proxy overwriting it, any client sets it.
 *
 * The request host is the Host header, or the last X-Forwarded-Host value under TRUST_PROXY. The
 * Host allowlist checks it, and so does the CSRF check when SNAPFALL_DASHBOARD_ORIGIN is unset.
 *
 * Reaching the Next.js server is not owner identity. Every route that attaches the owner token
 * first calls requireOwnerRequest(), which demands a signed owner session cookie (lib/ownerSession)
 * and, for mutations, a same-origin JSON request. The owner headers can only be built from the
 * grant that guard returns, so a route cannot attach the token without passing the guard.
 *
 * The customer portal (V9) is different: its credential is the per-job accept token
 * from the magic link, forwarded from the CLIENT's request, never the owner token —
 * the two principals never cross even at the proxy layer.
 */

import {
  OWNER_SECRET_MIN_LENGTH,
  ownerSessionCookieName,
  readCookies,
  validSessionExpiry,
  verifySessionValues,
  type SessionCheck,
} from './ownerSession';

export function ownerApiBase(): string | null {
  const raw = process.env.SNAPFALL_OWNER_API_URL?.trim();
  return raw ? raw.replace(/\/$/, '') : null;
}

/** Whether a daemon is configured, for routes that only need to say so (never to reach it). */
export function ownerApiConfigured(): boolean {
  return ownerApiBase() !== null;
}

function deny(status: number, code: string, message: string): Response {
  return Response.json({ error: { code, message } }, { status, headers: { 'cache-control': 'no-store' } });
}

export type OwnerAccessConfig = { ok: true; secret: string } | { ok: false; message: string };

/** The dashboard owner secret, or why it cannot be used. A missing, short, or reused secret is a
 *  misconfiguration, and so is a malformed SNAPFALL_DASHBOARD_ORIGIN; the guard treats each as
 *  "owner access not configured". */
export function ownerAccessConfig(): OwnerAccessConfig {
  const origins = expectedOrigins();
  if (origins.kind === 'invalid') return { ok: false, message: origins.message };
  const secret = process.env.SNAPFALL_DASHBOARD_OWNER_SECRET ?? '';
  if (secret.trim() === '') {
    return { ok: false, message: 'Owner access is not configured: set SNAPFALL_DASHBOARD_OWNER_SECRET on the dashboard.' };
  }
  if (secret.length < OWNER_SECRET_MIN_LENGTH) {
    return {
      ok: false,
      message: `Owner access is not configured: SNAPFALL_DASHBOARD_OWNER_SECRET must be at least ${OWNER_SECRET_MIN_LENGTH} characters.`,
    };
  }
  if (secret === process.env.SNAPFALL_OWNER_TOKEN) {
    return {
      ok: false,
      message: 'Owner access is not configured: SNAPFALL_DASHBOARD_OWNER_SECRET must differ from SNAPFALL_OWNER_TOKEN.',
    };
  }
  return { ok: true, secret };
}

/** Whether the session cookie should carry `Secure`. Production builds always set it. */
export function secureCookies(): boolean {
  return process.env.NODE_ENV === 'production';
}

/** SNAPFALL_DASHBOARD_TRUST_PROXY: an explicit statement that a proxy in front overwrites
 *  X-Forwarded-Host. Anything other than `1` or `true` is off. */
export function trustProxy(): boolean {
  const v = process.env.SNAPFALL_DASHBOARD_TRUST_PROXY?.trim().toLowerCase();
  return v === '1' || v === 'true';
}

/**
 * The host this request was addressed to, as the browser saw it.
 *
 * Without TRUST_PROXY it is the Host header, full stop: X-Forwarded-Host is an ordinary request
 * header that any client can send, so honouring it unasked would let a caller pick the host the
 * allowlist and the CSRF check compare against. With TRUST_PROXY it is the LAST X-Forwarded-Host
 * value, the one the nearest proxy wrote (a proxy that appends leaves the client's value first).
 */
export function requestHost(req: Request): string {
  if (trustProxy()) {
    const forwarded = req.headers
      .get('x-forwarded-host')
      ?.split(',')
      .map((h) => h.trim().toLowerCase())
      .filter(Boolean);
    const last = forwarded?.[forwarded.length - 1];
    if (last) return last;
  }
  const header = req.headers.get('host')?.trim().toLowerCase();
  if (header) return header;
  try {
    return new URL(req.url).host.toLowerCase();
  } catch {
    return '';
  }
}

function allowedHosts(): string[] | null {
  const raw = process.env.SNAPFALL_DASHBOARD_HOSTS;
  if (!raw || raw.trim() === '') return null;
  return raw
    .split(',')
    .map((h) => h.trim().toLowerCase())
    .filter(Boolean);
}

type ExpectedOrigins = { kind: 'host' } | { kind: 'explicit'; origins: string[] } | { kind: 'invalid'; message: string };

/** SNAPFALL_DASHBOARD_ORIGIN parsed: each entry must be a bare http(s) origin. */
export function expectedOrigins(): ExpectedOrigins {
  const raw = process.env.SNAPFALL_DASHBOARD_ORIGIN;
  if (!raw || raw.trim() === '') return { kind: 'host' };
  const origins: string[] = [];
  for (const entry of raw.split(',').map((e) => e.trim()).filter(Boolean)) {
    let u: URL;
    try {
      u = new URL(entry);
    } catch {
      return invalidOrigin(entry);
    }
    const bare = (u.pathname === '/' || u.pathname === '') && !u.search && !u.hash && !u.username && !u.password;
    if ((u.protocol !== 'https:' && u.protocol !== 'http:') || !bare || !/^https?:\/\/[^/?#]+\/?$/i.test(entry)) {
      return invalidOrigin(entry);
    }
    origins.push(u.origin);
  }
  return origins.length ? { kind: 'explicit', origins } : { kind: 'host' };
}

function invalidOrigin(entry: string): ExpectedOrigins {
  return {
    kind: 'invalid',
    message:
      `Owner access is not configured: SNAPFALL_DASHBOARD_ORIGIN entry ${JSON.stringify(entry)} is not a bare ` +
      'http(s) origin such as https://dash.example.com (no path, query or credentials).',
  };
}

/** Optional Host allowlist (SNAPFALL_DASHBOARD_HOSTS), matched against requestHost(). Unset ⇒ any
 *  Host; the session cookie is host-bound regardless, so a rebound hostname never carries it. */
export function checkHost(req: Request): Response | null {
  const allowed = allowedHosts();
  if (!allowed) return null;
  if (allowed.includes(requestHost(req))) return null;
  return deny(403, 'FORBIDDEN_HOST', 'This dashboard does not answer owner requests for that host.');
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * CSRF defence in depth for a state-changing request: the browser must say it is same-origin and
 * the body must be declared JSON, which a cross-site form or no-cors fetch cannot do without a
 * preflight. Same-origin means Sec-Fetch-Site, when sent, is `same-origin`, and Origin is
 *   - one of SNAPFALL_DASHBOARD_ORIGIN exactly, when that is set (behind a Host-rewriting proxy), or
 *   - for the request host (requestHost) otherwise.
 * These are client-controlled headers, so this is never authentication — the cookie is.
 */
export function checkSameOriginJson(req: Request): Response | null {
  const contentType = req.headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase();
  if (contentType !== 'application/json') {
    return deny(415, 'UNSUPPORTED_MEDIA_TYPE', 'Owner actions must be sent as application/json.');
  }
  const fetchSite = req.headers.get('sec-fetch-site')?.trim().toLowerCase();
  if (fetchSite && fetchSite !== 'same-origin') {
    return deny(403, 'FORBIDDEN', 'Owner actions must come from this dashboard.');
  }
  const expected = expectedOrigins();
  if (expected.kind === 'invalid') return deny(503, 'OWNER_ACCESS_NOT_CONFIGURED', expected.message);

  const origin = req.headers.get('origin');
  let parsed: URL | null = null;
  try {
    parsed = origin ? new URL(origin) : null;
  } catch {
    parsed = null;
  }
  const ok =
    parsed !== null &&
    (expected.kind === 'explicit'
      ? expected.origins.includes(parsed.origin)
      : parsed.host.toLowerCase() !== '' && parsed.host.toLowerCase() === requestHost(req));
  if (!ok) return deny(403, 'FORBIDDEN', 'Owner actions must come from this dashboard.');
  return null;
}

/** Every presented owner-session cookie is checked, so a stray same-named cookie (a sibling
 *  subdomain's, in development) cannot shadow a valid one. Production reads only the `__Host-`
 *  name, which no other host can set. */
export function ownerSessionState(req: Request, secret: string, now: number = Date.now()): SessionCheck {
  const name = ownerSessionCookieName(secureCookies());
  return verifySessionValues(secret, readCookies(req.headers.get('cookie'), name), now);
}

/** When the longest-lived valid owner session on this request ends, or null when there is none. */
export function ownerSessionExpiry(req: Request, secret: string, now: number = Date.now()): number | null {
  const name = ownerSessionCookieName(secureCookies());
  return validSessionExpiry(secret, readCookies(req.headers.get('cookie'), name), now);
}

/** Proof that requireOwnerRequest admitted this request. Nominal (private field) and checked at
 *  runtime, so ownerAuthHeaders cannot be fed a hand-made object. `sessionExpiresAt` is when the
 *  session that admitted it ends: a response held open past then (the event stream) must close. */
class OwnerGrantImpl {
  readonly #admitted = true;
  constructor(
    readonly base: string,
    readonly sessionExpiresAt: number,
  ) {}
  static is(value: unknown): value is OwnerGrantImpl {
    return typeof value === 'object' && value !== null && #admitted in value;
  }
}
export type OwnerGrant = OwnerGrantImpl;

export type OwnerGate = { ok: true; grant: OwnerGrant } | { ok: false; response: Response };

/**
 * The single owner-route guard. Call it first in EVERY route that talks to the owner API with the
 * owner token, and build the upstream headers from the grant it returns:
 *
 *   const gate = requireOwnerRequest(req);
 *   if (!gate.ok) return gate.response;
 *   fetch(`${gate.grant.base}/…`, { headers: ownerAuthHeaders(gate.grant, …) });
 *
 * Fails closed, in this order: no owner API (503 NO_DAEMON), no usable dashboard secret (503
 * OWNER_ACCESS_NOT_CONFIGURED), Host outside SNAPFALL_DASHBOARD_HOSTS (403), no valid unexpired
 * owner session (401 OWNER_SIGN_IN_REQUIRED), then — for any non-GET — a cross-origin or non-JSON
 * request (403 / 415).
 */
export function requireOwnerRequest(req: Request, now: number = Date.now()): OwnerGate {
  const base = ownerApiBase();
  if (!base) {
    return { ok: false, response: deny(503, 'NO_DAEMON', 'owner API not configured (SNAPFALL_OWNER_API_URL unset)') };
  }
  const config = ownerAccessConfig();
  if (!config.ok) return { ok: false, response: deny(503, 'OWNER_ACCESS_NOT_CONFIGURED', config.message) };

  const badHost = checkHost(req);
  if (badHost) return { ok: false, response: badHost };

  const sessionExpiresAt = ownerSessionExpiry(req, config.secret, now);
  if (sessionExpiresAt === null) {
    return { ok: false, response: deny(401, 'OWNER_SIGN_IN_REQUIRED', 'Sign in as the owner to use this dashboard’s owner controls.') };
  }

  if (!SAFE_METHODS.has(req.method.toUpperCase())) {
    const csrf = checkSameOriginJson(req);
    if (csrf) return { ok: false, response: csrf };
  }
  return { ok: true, grant: new OwnerGrantImpl(base, sessionExpiresAt) };
}

/** Upstream headers carrying the owner bearer. Only obtainable with a grant from
 *  requireOwnerRequest; anything else throws rather than attaching the token. */
export function ownerAuthHeaders(grant: OwnerGrant, extra?: HeadersInit): Headers {
  if (!OwnerGrantImpl.is(grant)) throw new Error('ownerAuthHeaders requires a grant from requireOwnerRequest');
  const h = new Headers(extra);
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) h.set('authorization', `Bearer ${token}`);
  return h;
}

/** Forward an upstream JSON response verbatim — status AND body — so the client sees
 *  the daemon's real codes (409 STALE_VIEW, 410 APPROVAL_EXPIRED, …), never a flattened
 *  200. */
export function forwardJSON(status: number, body: string): Response {
  return new Response(body, { status, headers: { 'content-type': 'application/json', 'cache-control': 'no-store' } });
}

/** Proxy a customer-portal request (V9). The credential is the CLIENT's — the per-job
 *  accept token from the magic link, forwarded from the incoming Authorization header,
 *  NOT the server's owner token. This never attaches SNAPFALL_OWNER_TOKEN, so it does not
 *  pass through requireOwnerRequest. */
export async function proxyCustomer(
  req: Request,
  jobId: string,
  subpath: 'acceptance' | 'accept' | 'invoice',
  method: 'GET' | 'POST',
): Promise<Response> {
  const base = ownerApiBase();
  if (!base) {
    return Response.json(
      { error: { code: 'NO_DAEMON', message: 'owner API not configured (SNAPFALL_OWNER_API_URL unset)' } },
      { status: 503 },
    );
  }
  const headers = new Headers({ accept: 'application/json' });
  const auth = req.headers.get('authorization'); // the customer's magic-link credential
  if (auth) headers.set('authorization', auth);
  let body: string | undefined;
  if (method === 'POST') {
    headers.set('content-type', 'application/json');
    body = await req.text();
  }
  try {
    const upstream = await fetch(`${base}/customer/jobs/${encodeURIComponent(jobId)}/${subpath}`, {
      method,
      headers,
      body,
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ error: { code: 'UPSTREAM', message: 'owner API unreachable' } }, { status: 502 });
  }
}
