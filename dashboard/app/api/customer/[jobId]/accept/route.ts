/** POST /api/customer/[jobId]/accept — the customer's Accept (V9), proxied to the
 *  daemon's credential-gated POST /api/v1/customer/jobs/{id}/accept. The daemon fires
 *  the on-chain settlement waterfall; its status and body (accepted-settled /
 *  accepted-settlement-reverted / accepted-pending-chain, or FROZEN/NOT_READY) pass
 *  through verbatim. */

import { proxyCustomer } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function POST(req: Request, { params }: { params: Promise<{ jobId: string }> }): Promise<Response> {
  const { jobId } = await params;
  return proxyCustomer(req, jobId, 'accept', 'POST');
}
