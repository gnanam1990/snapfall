/** GET /api/jobs/[jobId] — the authoritative chain read behind the job detail page (V7).
 *
 *  The page shows money, so it reads the frozen contracts rather than a cached projection:
 *  JobVault.jobs for the lifecycle, quote, budget, expenses and delivery hash, and
 *  FloatPool.openAdvanceOf plus advanceRate for the advance card. No eth_getLogs, so the
 *  public endpoint's history rate limits are irrelevant here; the timeline is streamed from
 *  H2 by the page instead.
 *
 *  A job the vault has never seen is a 404 with an explicit body, not a zero-valued job. */

import { loadJobSnapshot } from '@/lib/jobChain';
import { chainNetwork, clientSafeError, requireAddress } from '@/lib/network';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const CACHE_MS = 4_000;
const cache = new Map<string, { expiresAt: number; body: string }>();

function config() {
  // The selected network's RPC (ARC_TESTNET_RPC or ARC_MAINNET_RPC) and addresses; lib/network.
  const net = chainNetwork();
  return {
    chainId: net.chainId,
    rpcUrl: net.rpcUrl,
    jobVaultAddress: requireAddress('jobVault'),
    floatPoolAddress: requireAddress('floatPool'),
    explorerUrl: net.explorerUrl,
  };
}

export async function GET(_req: Request, { params }: { params: Promise<{ jobId: string }> }): Promise<Response> {
  const { jobId } = await params;

  const hit = cache.get(jobId);
  if (hit && hit.expiresAt > Date.now()) {
    return new Response(hit.body, {
      status: 200,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
    });
  }

  try {
    const snapshot = await loadJobSnapshot(config(), jobId);
    const body = JSON.stringify(snapshot);

    if (!snapshot.exists) {
      return new Response(body, {
        status: 404,
        headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
      });
    }

    cache.set(jobId, { expiresAt: Date.now() + CACHE_MS, body });
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
    });
  } catch (error) {
    // A malformed job id is the caller's fault; anything else is an upstream failure.
    const message = clientSafeError(error, 'chain read failed');
    const badRequest = /bytes32 hex|not an address/.test(message);
    return new Response(JSON.stringify({ error: message, jobId }), {
      status: badRequest ? 400 : 502,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
    });
  }
}
