/**
 * POST /api/workforce/[id]/hire — activates a worker through the daemon's owner-gated
 * `POST /api/v1/workforce/{id}/hire`.
 *
 * With no daemon configured this answers DAEMON_UNAVAILABLE without touching any owner
 * authority. With one, the owner guard runs before the owner token is attached: a signed-in
 * owner session, then a same-origin JSON request (the old Sec-Fetch-Site/Origin check alone
 * was CSRF defence, not authentication — a non-browser client could send those headers).
 */

import { ownerApiBase, ownerAuthHeaders, requireOwnerRequest } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function POST(req: Request, context: { params: Promise<{ id: string }> }): Promise<Response> {
  if (!ownerApiBase()) {
    return Response.json(
      { error: { code: 'DAEMON_UNAVAILABLE', message: 'Start the daemon and configure SNAPFALL_OWNER_API_URL to hire a watcher.' } },
      { status: 503 },
    );
  }
  const gate = requireOwnerRequest(req);
  if (!gate.ok) return gate.response;

  const { id } = await context.params;
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return Response.json({ error: { code: 'BAD_REQUEST', message: 'Malformed hire request.' } }, { status: 400 });
  }
  try {
    const upstream = await fetch(`${gate.grant.base}/workforce/${encodeURIComponent(id)}/hire`, {
      method: 'POST',
      headers: ownerAuthHeaders(gate.grant, { accept: 'application/json', 'content-type': 'application/json' }),
      body: JSON.stringify(body),
      cache: 'no-store',
      signal: AbortSignal.any([req.signal, AbortSignal.timeout(5_000)]),
    });
    return new Response(upstream.body, {
      status: upstream.status,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
    });
  } catch {
    return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Worker activation endpoint unavailable.' } }, { status: 502 });
  }
}
