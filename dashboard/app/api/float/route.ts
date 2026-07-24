import deployment from '../../../../deployments/arc-testnet.json';
import { loadFloatSnapshot } from '@/lib/floatChain';
import type { FloatSnapshot } from '@/lib/types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const CACHE_MS = 8_000;
let cached: { expiresAt: number; snapshot: FloatSnapshot } | null = null;
let pending: Promise<FloatSnapshot> | null = null;

function configuredSnapshot(): Promise<FloatSnapshot> {
  if (cached && cached.expiresAt > Date.now()) return Promise.resolve(cached.snapshot);
  if (pending) return pending;

  const privateRPC = process.env.ARC_TESTNET_RPC;
  pending = loadFloatSnapshot(
    {
      chainId: deployment.network.chainId,
      rpcUrl: privateRPC ?? deployment.network.rpcUrl,
      poolAddress: process.env.SNAPFALL_FLOAT_POOL_ADDRESS ?? deployment.contracts.floatPool.address,
      explorerUrl: deployment.network.explorerUrl,
      startBlock: Number(process.env.SNAPFALL_DEPLOYMENT_BLOCK ?? deployment.network.startBlock),
      orgAddress: process.env.SNAPFALL_TREASURY_ADDRESS,
    },
    undefined,
    {
      // The public endpoint is appropriate for current views but its historical
      // eth_getLogs path is heavily rate-limited. A private override enables the
      // complete cold scan; otherwise H2 supplies history aggregates to the page.
      includeHistory: Boolean(privateRPC) || process.env.SNAPFALL_FLOAT_SCAN_PUBLIC_RPC === '1',
    },
  )
    .then((snapshot) => {
      cached = { snapshot, expiresAt: Date.now() + CACHE_MS };
      return snapshot;
    })
    .finally(() => {
      pending = null;
    });
  return pending;
}

export async function GET(): Promise<Response> {
  try {
    const snapshot = await configuredSnapshot();
    return Response.json(snapshot, {
      headers: { 'cache-control': 'no-store', 'x-snapfall-source': 'arc-testnet' },
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : 'FloatPool snapshot unavailable';
    return Response.json(
      { code: 'FLOAT_UNAVAILABLE', message },
      { status: 503, headers: { 'cache-control': 'no-store' } },
    );
  }
}
