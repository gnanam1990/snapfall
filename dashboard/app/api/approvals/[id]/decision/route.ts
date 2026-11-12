/**
 * POST /api/approvals/[id]/decision — proxied to the daemon's H2
 * `POST /api/v1/approvals/{id}/decision`. The daemon's status and body pass through
 * verbatim so the client sees 409 STALE_VIEW (with the current view), 410
 * APPROVAL_EXPIRED, 409 ALREADY_DECIDED, 404, 400 — not a flattened success. The body
 * (kind, by, reason, intentHash) is forwarded unchanged; the daemon binds the decision
 * to the intentHash the owner was SHOWN.
 *
 * The owner guard runs first: a signed-in owner session, then a same-origin JSON request.
 */

import { ownerAuthHeaders, forwardJSON, requireOwnerRequest } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function POST(
  req: Request,
  { params }: { params: Promise<{ id: string }> },
): Promise<Response> {
  const gate = requireOwnerRequest(req);
  if (!gate.ok) return gate.response;
  const { id } = await params;

  const payload = await req.text();
  try {
    const upstream = await fetch(`${gate.grant.base}/approvals/${encodeURIComponent(id)}/decision`, {
      method: 'POST',
      headers: ownerAuthHeaders(gate.grant, { 'content-type': 'application/json' }),
      body: payload,
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ error: { code: 'UPSTREAM', message: 'owner API unreachable' } }, { status: 502 });
  }
}
