/** GET /api/governance — the on-chain capital controls, for the Audit page and for monitoring.
 *
 *  docs/MAINNET.md tells an operator to watch for a queued cap raise, operator grant or admin
 *  handoff. This is the machine-readable form: poll it, and alert when
 *    - `pendingCapRaise` or a `pendingAdmin` is non-null,
 *    - `operatorAllowlist.pendingGrants` is non-empty,
 *    - `operatorAllowlist.operators` has an entry with `allowed: true, configured: false`
 *      (a borrower other than SNAPFALL_TREASURY_ADDRESS), or
 *    - `operatorAllowlist.discovery.status` is not `complete` for longer than a few polls
 *      (`partial` resumes on the next poll; `failed` means the RPC refused the log scan),
 *  and you did not start it. The state is read from contract storage at one block
 *  (lib/governanceChain); only the operator ADDRESSES come from FloatPool's events. */

import { createRPCTransport } from '@/lib/floatChain';
import { loadGovernance } from '@/lib/governanceChain';

/** This route's share of a load spent extending the operator-event scan. */
const OPERATOR_SCAN_BUDGET_MS = 4_000;
import { chainNetwork, clientSafeError, networkName, requireAddress } from '@/lib/network';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export async function GET(): Promise<Response> {
  const headers = { 'cache-control': 'no-store', 'x-snapfall-source': networkName() };
  try {
    const net = chainNetwork();
    const snapshot = await loadGovernance(
      {
        chainId: net.chainId,
        floatPoolAddress: requireAddress('floatPool'),
        jobVaultAddress: requireAddress('jobVault'),
        operatorAddress: process.env.SNAPFALL_TREASURY_ADDRESS,
        operatorDiscovery: {
          startBlock: net.startBlock,
          chunkBlocks: process.env.SNAPFALL_FLOAT_SCAN_CHUNK ? Number(process.env.SNAPFALL_FLOAT_SCAN_CHUNK) : undefined,
          budgetMs: OPERATOR_SCAN_BUDGET_MS,
        },
      },
      createRPCTransport(net.rpcUrl),
    );
    return Response.json(snapshot, { headers });
  } catch (error) {
    const message = clientSafeError(error, 'governance read failed');
    return Response.json({ code: 'GOVERNANCE_UNAVAILABLE', message }, { status: 503, headers });
  }
}
