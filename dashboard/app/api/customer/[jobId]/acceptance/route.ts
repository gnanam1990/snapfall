/** GET /api/customer/[jobId]/acceptance — the customer's job-status read (V9), proxied
 *  to the daemon's credential-gated GET /api/v1/customer/jobs/{id}/acceptance. The
 *  credential rides the client's Authorization header (the magic-link token). */

import { proxyCustomer } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(req: Request, { params }: { params: Promise<{ jobId: string }> }): Promise<Response> {
  const { jobId } = await params;
  return proxyCustomer(req, jobId, 'acceptance', 'GET');
}
