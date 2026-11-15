/**
 * GET /api/chain/history — jobs, activity and totals decoded from the deployment's own event logs
 * (lib/chainHistory, kept current by lib/historyStore). Public: everything in it is already public
 * on chain, and every entry carries the transaction hash that proves it.
 *
 * Refreshes at most once per FRESH_MS however many dashboards are open, and a failed refresh
 * serves the last good history marked stale rather than nothing.
 */

import { explorerApiFor } from '@/lib/chainHistory';
import { createRPCTransport } from '@/lib/floatChain';
import { HistoryStore } from '@/lib/historyStore';
import { chainNetwork, clientSafeError, requireAddress } from '@/lib/network';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const FRESH_MS = 15_000;
let store: HistoryStore | null = null;
let refreshedAt = 0;
let inflight: Promise<unknown> | null = null;

function getStore(): HistoryStore {
  if (store) return store;
  const net = chainNetwork();
  store = new HistoryStore({
    network: net.network,
    explorerUrl: net.explorerUrl,
    explorerApi: explorerApiFor(net.network),
    addresses: [requireAddress('jobVault'), requireAddress('floatPool')],
    startBlock: net.startBlock,
    rpc: createRPCTransport(net.rpcUrl),
  });
  return store;
}

export async function GET(): Promise<Response> {
  const headers = { 'content-type': 'application/json', 'cache-control': 'no-store' };
  let s: HistoryStore;
  try {
    s = getStore();
  } catch (error) {
    const message = clientSafeError(error, 'chain history is not configured');
    return new Response(JSON.stringify({ error: { code: 'HISTORY_UNAVAILABLE', message } }), { status: 500, headers });
  }
  try {
    if (Date.now() - refreshedAt > FRESH_MS) {
      inflight ??= s.refresh(AbortSignal.timeout(30_000)).finally(() => {
        inflight = null;
      });
      await inflight;
      refreshedAt = Date.now();
    }
    return new Response(JSON.stringify({ ...s.snapshot(), stale: false }), { headers });
  } catch (error) {
    const message = clientSafeError(error, 'could not read the chain history');
    if (s.loaded) {
      return new Response(JSON.stringify({ ...s.snapshot(), stale: true, error: message }), { headers });
    }
    return new Response(JSON.stringify({ error: { code: 'HISTORY_UNAVAILABLE', message } }), { status: 502, headers });
  }
}
