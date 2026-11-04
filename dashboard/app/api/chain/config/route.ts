/** GET /api/chain/config — the public chain facts the customer's own wallet needs: which
 *  chain, which JobVault, which USDC, and where to link a transaction. Nothing here is secret
 *  (it is all on chain); it is served rather than bundled so the browser honours the same
 *  SNAPFALL_JOB_VAULT_ADDRESS / ARC_USDC_ADDRESS overrides the server-side reads use, and a
 *  customer can never approve USDC to a different vault than the one the portal reads. */

import deployment from '../../../../../deployments/arc-testnet.json';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(): Promise<Response> {
  return Response.json(
    {
      chainId: deployment.network.chainId,
      jobVault: process.env.SNAPFALL_JOB_VAULT_ADDRESS ?? deployment.contracts.jobVault.address,
      usdc: process.env.ARC_USDC_ADDRESS ?? deployment.contracts.usdc.address,
      explorerUrl: deployment.network.explorerUrl,
    },
    { headers: { 'cache-control': 'no-store' } },
  );
}
