/**
 * The capital controls as the chain reports them, for the Audit page.
 *
 * A server component: it reads FloatPool and JobVault directly (lib/governanceChain) at render
 * time, so what an operator sees here is contract state, not a projection. Every state that is
 * not "read successfully" says what it is: not deployed on this network, contracts that predate
 * the controls, or an RPC that did not answer. None of them renders as zeros.
 *
 * The operator allowlist (who may borrow) is shown the same way: every allowed operator and every
 * queued grant with its deadline, and how far the operator-event scan got. A pool without the
 * allowlist says so rather than showing an empty list, which would read as "nobody may borrow".
 */

import { createRPCTransport } from '@/lib/floatChain';
import { formatUsdcExact } from '@/lib/format';
import {
  loadGovernance,
  type GovernanceSnapshot,
  type OperatorAllowlist,
  type OperatorDiscovery,
  type PendingAdmin,
} from '@/lib/governanceChain';
import { chainNetwork, clientSafeError, contractAddress, networkLabel } from '@/lib/network';

const READ_TIMEOUT_MS = 6_000;
/** Kept well inside READ_TIMEOUT_MS: a cold operator scan must not cost the caps view. */
const OPERATOR_SCAN_BUDGET_MS = 2_500;

type Outcome = { kind: 'read'; snapshot: GovernanceSnapshot } | { kind: 'not-deployed' } | { kind: 'error'; message: string };

async function read(): Promise<Outcome> {
  const floatPoolAddress = contractAddress('floatPool');
  const jobVaultAddress = contractAddress('jobVault');
  if (!floatPoolAddress || !jobVaultAddress) return { kind: 'not-deployed' };
  const net = chainNetwork();
  try {
    const snapshot = await Promise.race([
      loadGovernance(
        {
          chainId: net.chainId,
          floatPoolAddress,
          jobVaultAddress,
          operatorAddress: process.env.SNAPFALL_TREASURY_ADDRESS,
          operatorDiscovery: {
            startBlock: net.startBlock,
            chunkBlocks: process.env.SNAPFALL_FLOAT_SCAN_CHUNK ? Number(process.env.SNAPFALL_FLOAT_SCAN_CHUNK) : undefined,
            budgetMs: OPERATOR_SCAN_BUDGET_MS,
          },
        },
        createRPCTransport(net.rpcUrl),
      ),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the RPC did not answer in time')), READ_TIMEOUT_MS)),
    ]);
    return { kind: 'read', snapshot };
  } catch (error) {
    return { kind: 'error', message: clientSafeError(error, 'read failed') };
  }
}

const usdc = (atomic: string) => `${formatUsdcExact(atomic)} USDC`;

function pending(p: PendingAdmin | null): string {
  if (!p) return 'none';
  return `${p.address} — ${p.acceptable ? 'can accept now' : `acceptable from ${p.acceptableAt}`}`;
}

function discoveryText(d: OperatorDiscovery): string {
  switch (d.status) {
    case 'complete':
      return d.throughBlock < d.fromBlock
        ? 'every operator event (no blocks since deployment yet)'
        : `every operator event, blocks ${d.fromBlock}–${d.throughBlock}`;
    case 'partial':
      return `incomplete: ${d.reason}. Reload; until it completes, an operator not listed here may exist`;
    case 'failed':
      return `incomplete: ${d.reason}. Only the addresses listed were checked`;
    case 'off':
      return d.reason;
  }
}

/** The allowlist rows, inside the same facts list as the caps. */
function OperatorRows({ allowlist }: { allowlist: OperatorAllowlist }) {
  if (allowlist.status === 'unsupported') {
    return (
      <div>
        <dt>operator allowlist</dt>
        <dd>not supported by this deployment</dd>
      </div>
    );
  }
  const configured = allowlist.operators.find((o) => o.configured);
  return (
    <>
      <div>
        <dt>queued operator grants</dt>
        <dd className="settings-mono">
          {allowlist.pendingGrants.length === 0
            ? 'none'
            : allowlist.pendingGrants.map((g, i) => (
                <span key={g.address}>
                  {i > 0 && <br />}
                  {g.address} — {g.executable ? 'executable now' : `executable from ${g.executableAt}`}
                </span>
              ))}
        </dd>
      </div>
      <div>
        <dt>allowed operators</dt>
        <dd className="settings-mono">
          {allowlist.allowed.length === 0
            ? 'none'
            : allowlist.operators
                .filter((o) => o.allowed)
                .map((o, i) => (
                  <span key={o.address}>
                    {i > 0 && <br />}
                    {o.address}
                    {o.configured ? ' (SNAPFALL_TREASURY_ADDRESS)' : ' (not the configured treasury)'}
                  </span>
                ))}
        </dd>
      </div>
      <div>
        <dt>configured operator</dt>
        <dd className="settings-mono">
          {!allowlist.configuredOperator
            ? 'SNAPFALL_TREASURY_ADDRESS not set'
            : `${allowlist.configuredOperator} — ${
                configured?.allowed ? 'allowed to borrow' : 'not allowed: its advances revert OperatorNotAllowed()'
              }`}
        </dd>
      </div>
      <div>
        <dt>operators scanned</dt>
        <dd>{discoveryText(allowlist.discovery)}</dd>
      </div>
    </>
  );
}

export default async function CapitalControls() {
  const outcome = await read();

  return (
    <section className="card mt" aria-labelledby="audit-controls">
      <h2 className="card-title" id="audit-controls">
        Capital controls
      </h2>
      <p className="audit-lede">
        Read from the contracts on {networkLabel()} at render time. The ceiling bounds open principal
        plus every loss already taken; raising it, allowing a new borrower, or moving admin waits out
        an on-chain delay that shows here while it runs.
      </p>

      {outcome.kind === 'not-deployed' && (
        <p className="settings-note">Not deployed on {networkLabel()} yet: no FloatPool and JobVault addresses are configured.</p>
      )}
      {outcome.kind === 'error' && <p className="settings-note">Unavailable: {outcome.message}</p>}
      {outcome.kind === 'read' && outcome.snapshot.status === 'unsupported' && (
        <p className="settings-note">{outcome.snapshot.reason}</p>
      )}
      {outcome.kind === 'read' && outcome.snapshot.status === 'ok' && (
        <dl className="settings-facts">
          <div>
            <dt>loss ceiling (lifetime)</dt>
            <dd>{usdc(outcome.snapshot.lossBudget.ceiling)}</dd>
          </div>
          <div>
            <dt>lent now · lost for good</dt>
            <dd>
              {usdc(outcome.snapshot.lossBudget.outstanding)} · {usdc(outcome.snapshot.lossBudget.writtenOff)}
            </dd>
          </div>
          <div>
            <dt>budget remaining</dt>
            <dd>{usdc(outcome.snapshot.lossBudget.remaining)}</dd>
          </div>
          <div>
            <dt>max advance · max job</dt>
            <dd>
              {usdc(outcome.snapshot.pool.maxAdvance)} · {usdc(outcome.snapshot.vault.maxJobPayment)}
            </dd>
          </div>
          <div>
            <dt>paused (pool · vault)</dt>
            <dd>
              {outcome.snapshot.pool.paused ? 'yes' : 'no'} · {outcome.snapshot.vault.paused ? 'yes' : 'no'}
            </dd>
          </div>
          <div>
            <dt>queued cap raise</dt>
            <dd>
              {outcome.snapshot.pendingCapRaise
                ? `to ${usdc(outcome.snapshot.pendingCapRaise.maxTotalExposure)} / ${usdc(outcome.snapshot.pendingCapRaise.maxAdvance)} — ${
                    outcome.snapshot.pendingCapRaise.executable
                      ? 'executable now'
                      : `executable from ${outcome.snapshot.pendingCapRaise.executableAt}`
                  }`
                : 'none'}
            </dd>
          </div>
          <OperatorRows allowlist={outcome.snapshot.operatorAllowlist} />
          <div>
            <dt>admin</dt>
            <dd className="settings-mono">{outcome.snapshot.pool.admin}</dd>
          </div>
          <div>
            <dt>pending admin (pool)</dt>
            <dd className="settings-mono">{pending(outcome.snapshot.pool.pendingAdmin)}</dd>
          </div>
          <div>
            <dt>pending admin (vault)</dt>
            <dd className="settings-mono">{pending(outcome.snapshot.vault.pendingAdmin)}</dd>
          </div>
        </dl>
      )}
      {outcome.kind === 'read' &&
        outcome.snapshot.status === 'ok' &&
        outcome.snapshot.operatorAllowlist.status === 'unsupported' && (
          <p className="settings-note">{outcome.snapshot.operatorAllowlist.reason}</p>
        )}
      {outcome.kind === 'read' && outcome.snapshot.status === 'ok' && (
        <p className="settings-note">
          A queued raise, a queued operator grant, an allowed operator you do not run, or a pending admin you
          did not start is an incident: lower the caps (which cancels the raise), call{' '}
          <code>setOperatorAllowed(op, false)</code> (which revokes at once and drops a queued grant), or call{' '}
          <code>transferAdmin</code> with your own address, then follow docs/MAINNET.md. Machine-readable at{' '}
          <code>/api/governance</code> for alerting.
        </p>
      )}
    </section>
  );
}
