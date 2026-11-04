/** GET /api/cctp/fee?source=<domain> — Circle's CCTP fee table for <source> -> Arc, proxied for
 *  the same reasons as ../message: fixed host, listed domains only. */

import deployment from '../../../../../deployments/arc-testnet.json';
import { CCTP } from '@/lib/cctp';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(req: Request): Promise<Response> {
  const env = CCTP[deployment.network.chainId];
  const source = Number(new URL(req.url).searchParams.get('source'));
  if (!env || !env.sources.some((s) => s.domain === source)) {
    return Response.json({ error: 'unsupported source domain' }, { status: 400 });
  }
  const res = await fetch(`${env.irisBase}/v2/burn/USDC/fees/${source}/${env.arcDomain}`, { cache: 'no-store' });
  return new Response(await res.text(), {
    status: res.status,
    headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
  });
}
