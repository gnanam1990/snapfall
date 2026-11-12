/**
 * GET /api/approvals — the pending-approvals inbox (V8), proxied to the daemon's H2
 * `GET /api/v1/approvals`. Falls back to an empty list when no daemon is configured, so
 * the page renders "nothing pending" in dev rather than erroring. With a daemon, the
 * owner guard runs before the owner token is attached.
 */

import { ownerApiBase, ownerAuthHeaders, forwardJSON, requireOwnerRequest } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(req: Request): Promise<Response> {
  if (!ownerApiBase()) return Response.json({ approvals: [], source: 'mock' });

  const gate = requireOwnerRequest(req);
  if (!gate.ok) return gate.response;

  try {
    const upstream = await fetch(`${gate.grant.base}/approvals`, {
      headers: ownerAuthHeaders(gate.grant, { accept: 'application/json' }),
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ approvals: [], error: 'owner API unreachable' }, { status: 502 });
  }
}
