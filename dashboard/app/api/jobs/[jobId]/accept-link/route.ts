/**
 * POST /api/jobs/[jobId]/accept-link — mints (or rotates) the customer's magic-link credential,
 * proxied to the daemon's owner-gated `POST /api/v1/jobs/{id}/accept-link`.
 *
 * V9's portal has existed and worked, but nothing in the product could produce a link to it: the
 * mint endpoint had no proxy and no UI, so the only way to reach the customer surface was to
 * assemble the URL by hand from a curl response. A demo path whose first step is "open a
 * terminal" is not a demo path.
 *
 * The owner token stays server-side, exactly as the other owner proxies do — the browser never
 * holds it. What comes back is a per-job credential for a DIFFERENT principal (the customer), so
 * it is returned to the owner to hand over deliberately, and never attached to a customer request
 * by this route. The owner guard runs first: a signed-in owner session, then a same-origin JSON
 * request, so neither another visitor nor a cross-site page can rotate a customer's link.
 */

import { ownerAuthHeaders, forwardJSON, requireOwnerRequest } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function POST(
  req: Request,
  { params }: { params: Promise<{ jobId: string }> },
): Promise<Response> {
  const gate = requireOwnerRequest(req);
  if (!gate.ok) return gate.response;
  const { jobId } = await params;

  try {
    // The daemon's status and body pass through verbatim: a 503 NOT_WIRED (the MintAccept seam
    // is nil) and a 404 (no such job) are both things the owner needs to see as themselves,
    // not flattened into one failure.
    const upstream = await fetch(`${gate.grant.base}/jobs/${encodeURIComponent(jobId)}/accept-link`, {
      method: 'POST',
      headers: ownerAuthHeaders(gate.grant, { 'content-type': 'application/json' }),
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ error: { code: 'UPSTREAM', message: 'owner API unreachable' } }, { status: 502 });
  }
}
