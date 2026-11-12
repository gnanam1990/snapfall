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

/**
 * The request body as text, or null once it passes `limit` bytes. A declared Content-Length over
 * the limit is refused before anything is read, and the stream is read with a running count and
 * cancelled at the limit, so an unauthenticated caller cannot make this route buffer a body of
 * any size (req.text() reads all of it first, and only then could the length be checked).
 */
async function readCapped(req: Request, limit: number): Promise<string | null> {
  const declared = req.headers.get('content-length');
  if (declared !== null && /^\d+$/.test(declared.trim()) && Number(declared.trim()) > limit) return null;
  if (!req.body) return '';
  const reader = req.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel().catch(() => {});
      return null;
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const c of chunks) {
    bytes.set(c, offset);
    offset += c.byteLength;
  }
  return new TextDecoder().decode(bytes);
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

  const raw = await readCapped(req, MAX_BODY_BYTES);
  if (raw === null) return fail(413, 'BAD_REQUEST', 'Sign-in request too large.');
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
