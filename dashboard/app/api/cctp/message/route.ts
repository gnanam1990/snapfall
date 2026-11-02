/** GET /api/cctp/message?source=<domain>&tx=<hash> — Circle's attestation for one burn.
 *
 *  A proxy rather than a browser call, so the page does not depend on Circle's CORS policy.
 *  Not an open relay: the host is fixed by the deployment's chain (sandbox Iris for Arc
 *  Testnet, production Iris for Arc), the source must be one of the domains lib/cctp.ts lists,
 *  and the hash must be a hash. The answer is passed through untouched; lib/cctp.ts
 *  readAttestation decides whether it is safe to relay. */

import deployment from '../../../../../deployments/arc-testnet.json';
import { CCTP } from '@/lib/cctp';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const TX_RE = /^0x[0-9a-fA-F]{64}$/;

export async function GET(req: Request): Promise<Response> {
  const env = CCTP[deployment.network.chainId];
  const url = new URL(req.url);
  const source = Number(url.searchParams.get('source'));
  const tx = url.searchParams.get('tx') ?? '';
  if (!env || !env.sources.some((s) => s.domain === source) || !TX_RE.test(tx)) {
    return Response.json({ error: 'unsupported source domain or malformed transaction hash' }, { status: 400 });
  }
  const res = await fetch(`${env.irisBase}/v2/messages/${source}?transactionHash=${tx}`, { cache: 'no-store' });
  // Iris answers 404 until it has indexed the burn; that is "not yet", not an error.
  if (res.status === 404) return Response.json({ messages: [] }, { headers: { 'cache-control': 'no-store' } });
  return new Response(await res.text(), {
    status: res.status,
    headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
  });
}
