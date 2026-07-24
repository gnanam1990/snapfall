/**
 * GET /api/approvals — the pending-approvals inbox (V8), proxied to the daemon's H2
 * `GET /api/v1/approvals`. Falls back to an empty list when no daemon is configured, so
 * the page renders "nothing pending" in dev rather than erroring.
 */

import { ownerApiBase, ownerAuthHeaders, forwardJSON } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(): Promise<Response> {
  const base = ownerApiBase();
  if (!base) return Response.json({ approvals: [], source: 'mock' });

  try {
    const upstream = await fetch(`${base}/approvals`, {
      headers: ownerAuthHeaders({ accept: 'application/json' }),
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ approvals: [], error: 'owner API unreachable' }, { status: 502 });
  }
}
