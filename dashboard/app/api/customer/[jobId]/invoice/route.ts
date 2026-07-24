/** GET /api/customer/[jobId]/invoice — the customer's receipt (V9), proxied to the
 *  daemon's credential-gated GET /api/v1/customer/jobs/{id}/invoice, which serves the
 *  CUSTOMER copy of the billing record (plain-language gaps, no owner internals, no
 *  reconciliation, no alerts) — the copy-serving decision, made concrete. */

import { proxyCustomer } from '@/lib/ownerApi';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(req: Request, { params }: { params: Promise<{ jobId: string }> }): Promise<Response> {
  const { jobId } = await params;
  return proxyCustomer(req, jobId, 'invoice', 'GET');
}
