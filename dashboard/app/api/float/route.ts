import deployment from '../../../../deployments/arc-testnet.json';
import {
  assembleSnapshot,
  createRPCTransport,
  loadFloatViews,
  peekFloatHistory,
  scanFloatHistory,
  type FloatChainConfig,
} from '@/lib/floatChain';
import type { FloatSnapshot } from '@/lib/types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

function floatConfig(): { config: FloatChainConfig; includeHistory: boolean } {
  const privateRPC = process.env.ARC_TESTNET_RPC;
  return {
    config: {
      chainId: deployment.network.chainId,
      rpcUrl: privateRPC ?? deployment.network.rpcUrl,
      poolAddress: process.env.SNAPFALL_FLOAT_POOL_ADDRESS ?? deployment.contracts.floatPool.address,
      explorerUrl: deployment.network.explorerUrl,
      startBlock: Number(process.env.SNAPFALL_DEPLOYMENT_BLOCK ?? deployment.network.startBlock),
      orgAddress: process.env.SNAPFALL_TREASURY_ADDRESS,
    },
    // The public endpoint's historical eth_getLogs path is heavily rate-limited. A private
    // override enables the (now incremental, cached) scan; otherwise H2 supplies aggregates.
    includeHistory: Boolean(privateRPC) || process.env.SNAPFALL_FLOAT_SCAN_PUBLIC_RPC === '1',
  };
}

function json(snapshot: FloatSnapshot): Response {
  return Response.json(snapshot, {
    headers: { 'cache-control': 'no-store', 'x-snapfall-source': 'arc-testnet' },
  });
}

export async function GET(): Promise<Response> {
  const { config, includeHistory } = floatConfig();
  const rpc = createRPCTransport(config.rpcUrl);
  try {
    // The immediate views resolve in ~100ms and never wait on the log scan.
    const views = await loadFloatViews(config, rpc);

    if (!includeHistory) {
      return json(assembleSnapshot(views, null, 'unavailable'));
    }

    const cached = peekFloatHistory(config, views.orgAddress);
    if (cached) {
      // Warm cache: return complete now, and extend the accumulator toward the current
      // head in the background (fire-and-forget — scanned blocks are final on Arc).
      void scanFloatHistory(config, rpc, views.headBlock).catch(() => {});
      return json(assembleSnapshot(views, cached, 'complete'));
    }

    // Cold: kick the scan in the background and return the views now with historyStatus
    // 'pending'. The page's 15s poll returns the completed history once the scan caches;
    // after that first cold scan every subsequent scan is a cheap incremental extension.
    void scanFloatHistory(config, rpc, views.headBlock).catch(() => {});
    return json(assembleSnapshot(views, null, 'pending'));
  } catch (error) {
    const message = error instanceof Error ? error.message : 'FloatPool snapshot unavailable';
    return Response.json(
      { code: 'FLOAT_UNAVAILABLE', message },
      { status: 503, headers: { 'cache-control': 'no-store' } },
    );
  }
}
