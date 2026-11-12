/**
 * /api/owner/session — the dashboard's owner sign-in.
 *
 *   GET     → { ownerApi, accessConfigured, signedIn }: what the owner pages need to decide
 *             whether to show a sign-in prompt. Presence only; no value is ever returned.
 *   POST    → { secret } as same-origin JSON. On a constant-time match with
 *             SNAPFALL_DASHBOARD_OWNER_SECRET, sets the HttpOnly, SameSite=Strict owner session
 *             cookie (Secure in production) holding a signed, expiring value — never the secret.
 *   DELETE  → signs this browser out by clearing the cookie.
 *
 * This route never attaches the owner token; it only issues the session the owner routes'
 * guard (lib/ownerApi requireOwnerRequest) checks.
 */

import {
  checkHost,
  checkSameOriginJson,
  ownerAccessConfig,
  ownerApiConfigured,
  ownerSessionState,
  secureCookies,
} from '@/lib/ownerApi';
import { clearedSessionCookie, issueSessionValue, safeEqual, sessionCookie } from '@/lib/ownerSession';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const NO_STORE = { 'cache-control': 'no-store' } as const;
const MAX_BODY_BYTES = 4096;

function fail(status: number, code: string, message: string): Response {
  return Response.json({ error: { code, message } }, { status, headers: NO_STORE });
}

export async function GET(req: Request): Promise<Response> {
  const badHost = checkHost(req);
  if (badHost) return badHost;
  const config = ownerAccessConfig();
  return Response.json(
    {
      ownerApi: ownerApiConfigured(),
      accessConfigured: config.ok,
      signedIn: config.ok && ownerSessionState(req, config.secret) === 'valid',
    },
    { headers: NO_STORE },
  );
}

export async function POST(req: Request): Promise<Response> {
  const badHost = checkHost(req);
  if (badHost) return badHost;
  const csrf = checkSameOriginJson(req);
  if (csrf) return csrf;
  const config = ownerAccessConfig();
  if (!config.ok) return fail(503, 'OWNER_ACCESS_NOT_CONFIGURED', config.message);

  const raw = await req.text();
  if (raw.length > MAX_BODY_BYTES) return fail(413, 'BAD_REQUEST', 'Sign-in request too large.');
  let presented: unknown;
  try {
    presented = (JSON.parse(raw) as { secret?: unknown }).secret;
  } catch {
    return fail(400, 'BAD_REQUEST', 'Malformed sign-in request.');
  }
  if (typeof presented !== 'string' || !safeEqual(presented, config.secret)) {
    return fail(401, 'OWNER_SECRET_REJECTED', 'That is not this dashboard’s owner secret.');
  }

  const now = Date.now();
  const { value, expiresAt } = issueSessionValue(config.secret, now);
  return new Response(null, {
    status: 204,
    headers: { ...NO_STORE, 'set-cookie': sessionCookie(value, expiresAt, now, secureCookies()) },
  });
}

export async function DELETE(req: Request): Promise<Response> {
  const badHost = checkHost(req);
  if (badHost) return badHost;
  const csrf = checkSameOriginJson(req);
  if (csrf) return csrf;
  return new Response(null, {
    status: 204,
    headers: { ...NO_STORE, 'set-cookie': clearedSessionCookie(secureCookies()) },
  });
}
