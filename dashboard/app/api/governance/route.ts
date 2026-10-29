/** GET /api/governance — the on-chain capital controls, for the Audit page and for monitoring.
 *
 *  docs/MAINNET.md tells an operator to watch for a queued cap raise or admin handoff. This is
 *  the machine-readable form: poll it, and alert when `pendingCapRaise` or a `pendingAdmin` is
 *  non-null. Read from contract state at one block (lib/governanceChain), not from events. */

import { createRPCTransport } from '@/lib/floatChain';
import { loadGovernance } from '@/lib/governanceChain';
import { chainNetwork, networkName, requireAddress } from '@/lib/network';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(): Promise<Response> {
  const headers = { 'cache-control': 'no-store', 'x-snapfall-source': networkName() };
  try {
    const net = chainNetwork();
    const snapshot = await loadGovernance(
      { chainId: net.chainId, floatPoolAddress: requireAddress('floatPool'), jobVaultAddress: requireAddress('jobVault') },
      createRPCTransport(net.rpcUrl),
    );
    return Response.json(snapshot, { headers });
  } catch (error) {
    const message = error instanceof Error ? error.message : 'governance read failed';
    return Response.json({ code: 'GOVERNANCE_UNAVAILABLE', message }, { status: 503, headers });
  }
}
