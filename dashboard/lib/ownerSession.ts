/**
 * The dashboard's owner session: a signed, expiring cookie value proving the visitor once
 * presented SNAPFALL_DASHBOARD_OWNER_SECRET.
 *
 * The cookie never carries the secret. It carries `v1.<expiresAtMs>.<nonce>.<mac>`, where the MAC
 * is HMAC-SHA256 under a key derived from the secret. Rotating the secret therefore invalidates
 * every outstanding session at once, which is the revocation story: the value is stateless, so
 * signing out clears this browser's cookie and rotating the secret signs out everyone.
 *
 * Pure functions over explicit inputs (secret, clock), so the guard in lib/ownerApi.ts is the only
 * place that reads the environment.
 */

import { createHash, createHmac, randomBytes, timingSafeEqual } from 'node:crypto';

/** The cookie name over plain-http development. */
export const OWNER_SESSION_COOKIE = 'snapfall_owner_session';
/**
 * The cookie name whenever cookies are Secure (production). The `__Host-` prefix makes the browser
 * refuse the cookie unless it is Secure, Path=/ and carries no Domain, so a sibling subdomain
 * (evil.example.com next to dash.example.com) cannot plant or shadow it. Plain-http development
 * cannot use the prefix, so it keeps the bare name and relies on readCookies trying every value.
 */
export const OWNER_SESSION_COOKIE_SECURE = '__Host-snapfall_owner_session';

export function ownerSessionCookieName(secure: boolean): string {
  return secure ? OWNER_SESSION_COOKIE_SECURE : OWNER_SESSION_COOKIE;
}

/** At most this many same-named cookie values are checked per request. */
export const MAX_SESSION_CANDIDATES = 8;
/** How long one sign-in lasts. */
export const OWNER_SESSION_TTL_MS = 12 * 60 * 60 * 1000;
/** Shortest dashboard owner secret accepted. Matches the daemon's non-loopback token floor. */
export const OWNER_SECRET_MIN_LENGTH = 32;

const VERSION = 'v1';
const KEY_LABEL = 'snapfall-dashboard-owner-session/v1';

function sessionKey(secret: string): Buffer {
  return createHmac('sha256', secret).update(KEY_LABEL).digest();
}

function mac(secret: string, payload: string): string {
  return createHmac('sha256', sessionKey(secret)).update(payload).digest('base64url');
}

/** Constant-time string equality. Both sides are hashed first, so differing lengths take the same
 *  path and leak nothing about the expected value's length. */
export function safeEqual(a: string, b: string): boolean {
  const x = createHash('sha256').update(a, 'utf8').digest();
  const y = createHash('sha256').update(b, 'utf8').digest();
  return timingSafeEqual(x, y);
}

export function issueSessionValue(secret: string, now: number = Date.now()): { value: string; expiresAt: number } {
  const expiresAt = now + OWNER_SESSION_TTL_MS;
  const nonce = randomBytes(16).toString('base64url');
  const payload = `${VERSION}.${expiresAt}.${nonce}`;
  return { value: `${payload}.${mac(secret, payload)}`, expiresAt };
}

export type SessionCheck = 'valid' | 'missing' | 'malformed' | 'bad-signature' | 'expired';

export function verifySessionValue(secret: string, value: string | null | undefined, now: number = Date.now()): SessionCheck {
  if (!value) return 'missing';
  const parts = value.split('.');
  if (parts.length !== 4) return 'malformed';
  const [version, exp, nonce, presentedMac] = parts;
  if (version !== VERSION || !/^\d{1,16}$/.test(exp) || !/^[A-Za-z0-9_-]{16,64}$/.test(nonce)) return 'malformed';
  const expected = mac(secret, `${version}.${exp}.${nonce}`);
  if (!safeEqual(presentedMac, expected)) return 'bad-signature';
  const expiresAt = Number(exp);
  // A signed value is unforgeable, but a lifetime longer than one TTL can only come from a clock
  // that was wrong at issue time; refuse it rather than honour an indefinite session.
  if (expiresAt <= now || expiresAt > now + OWNER_SESSION_TTL_MS + 60_000) return 'expired';
  return 'valid';
}

/**
 * Every value of one cookie in a Cookie header, in header order, without a dependency.
 *
 * A browser sends one `name=value` per matching cookie, and a cookie set for a parent domain by a
 * sibling subdomain arrives alongside ours under the same name, often first. Taking only the first
 * would let that cookie shadow a valid session (a denial, never a forgery, since every value is
 * MAC-checked), so callers check all of them.
 */
export function readCookies(header: string | null, name: string): string[] {
  if (!header) return [];
  const out: string[] = [];
  for (const part of header.split(';')) {
    const eq = part.indexOf('=');
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() !== name) continue;
    const raw = part.slice(eq + 1).trim();
    try {
      out.push(decodeURIComponent(raw));
    } catch {
      out.push(raw);
    }
  }
  return out;
}

/** The first value of one cookie, or null. */
export function readCookie(header: string | null, name: string): string | null {
  return readCookies(header, name)[0] ?? null;
}

/** 'valid' when any presented value is a valid session; otherwise the first value's failure. */
export function verifySessionValues(secret: string, values: readonly string[], now: number = Date.now()): SessionCheck {
  if (values.length === 0) return 'missing';
  let first: SessionCheck | null = null;
  for (const value of values.slice(0, MAX_SESSION_CANDIDATES)) {
    const check = verifySessionValue(secret, value, now);
    if (check === 'valid') return 'valid';
    first ??= check;
  }
  return first ?? 'missing';
}

function cookieAttributes(maxAgeSeconds: number, secure: boolean): string {
  return [
    'Path=/',
    'HttpOnly',
    'SameSite=Strict',
    `Max-Age=${maxAgeSeconds}`,
    ...(secure ? ['Secure'] : []),
  ].join('; ');
}

// No Domain attribute, ever: the cookie stays host-only, which `__Host-` also requires.
export function sessionCookie(value: string, expiresAt: number, now: number, secure: boolean): string {
  const maxAge = Math.max(0, Math.floor((expiresAt - now) / 1000));
  return `${ownerSessionCookieName(secure)}=${value}; ${cookieAttributes(maxAge, secure)}`;
}

export function clearedSessionCookie(secure: boolean): string {
  return `${ownerSessionCookieName(secure)}=; ${cookieAttributes(0, secure)}`;
}
