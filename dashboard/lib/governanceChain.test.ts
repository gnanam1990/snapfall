import assert from 'node:assert/strict';
import test from 'node:test';
import { loadGovernance, governanceChainInternals } from './governanceChain';
import { RPCError, type RPCTransport } from './floatChain';

const POOL = '0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5';
const VAULT = '0xF3830D7C3B8ca873bB0b277c0e179999e3d52681';
const ADMIN = '0x00000000000000000000000000000000000000A0';
const NEXT = '0x00000000000000000000000000000000000000A1';
const NOW = 1_800_000_000n;
const S = governanceChainInternals.SELECTOR;

const config = { chainId: 5042, floatPoolAddress: POOL, jobVaultAddress: VAULT };
const w = (v: bigint | number) => '0x' + BigInt(v).toString(16).padStart(64, '0');
const addr = (a: string) => '0x' + a.slice(2).toLowerCase().padStart(64, '0');

type Answers = Partial<Record<keyof typeof S, string>>;

function fakeRPC(pool: Answers, vault: Answers = {}, opts: { chainId?: number; missing?: boolean } = {}): RPCTransport {
  const byPool = new Map<string, string>(Object.entries(pool).map(([k, v]) => [S[k as keyof typeof S], v as string]));
  const byVault = new Map<string, string>(Object.entries(vault).map(([k, v]) => [S[k as keyof typeof S], v as string]));
  return (async (method: string, params: unknown[]) => {
    if (method === 'eth_chainId') return '0x' + (opts.chainId ?? 5042).toString(16);
    if (method === 'eth_getBlockByNumber') return { number: '0x64', timestamp: '0x' + NOW.toString(16) };
    assert.equal(method, 'eth_call');
    const [{ to, data }, block] = params as [{ to: string; data: string }, string];
    assert.equal(block, '0x64', 'every read is pinned to the same block');
    if (opts.missing) throw new RPCError('Arc RPC eth_call failed: execution reverted', 200, 3, 'execution reverted');
    const answer = (to === POOL ? byPool : byVault).get(data);
    if (answer === undefined) throw new Error(`unexpected read ${to} ${data}`);
    return answer;
  }) as RPCTransport;
}

const basePool: Answers = {
  maxTotalExposure: w(50_000_000),
  maxAdvance: w(10_000_000),
  totalOutstanding: w(12_500_000),
  totalWrittenOff: w(7_500_000),
  paused: w(0),
  pendingMaxTotalExposure: w(0),
  pendingMaxAdvance: w(0),
  capRaiseExecutableAt: w(0),
  admin: addr(ADMIN),
  pendingAdmin: addr('0x' + '0'.repeat(40)),
  adminTransferAcceptableAt: w(0),
};
const baseVault: Answers = {
  paused: w(1),
  maxJobPayment: w(25_000_000),
  admin: addr(ADMIN),
  pendingAdmin: addr('0x' + '0'.repeat(40)),
  adminTransferAcceptableAt: w(0),
};

test('reads caps and splits the lifetime loss budget into lent, lost and left', async () => {
  const g = await loadGovernance(config, fakeRPC(basePool, baseVault));
  assert.equal(g.status, 'ok');
  if (g.status !== 'ok') return;
  assert.deepEqual(g.lossBudget, { ceiling: '50000000', outstanding: '12500000', writtenOff: '7500000', remaining: '30000000' });
  assert.equal(g.pool.paused, false);
  assert.equal(g.vault.paused, true);
  assert.equal(g.vault.maxJobPayment, '25000000');
  assert.equal(g.pool.admin, ADMIN.toLowerCase());
  assert.equal(g.pendingCapRaise, null);
  assert.equal(g.pool.pendingAdmin, null);
  assert.equal(g.observedAt, new Date(Number(NOW) * 1000).toISOString());
});

test('a spent budget reads as zero remaining, never negative', async () => {
  const g = await loadGovernance(config, fakeRPC({ ...basePool, totalWrittenOff: w(60_000_000) }, baseVault));
  assert.equal(g.status === 'ok' && g.lossBudget.remaining, '0');
});

test('a queued cap raise is surfaced with its deadline, and whether it can execute yet', async () => {
  const later = NOW + 3600n;
  const g = await loadGovernance(
    config,
    fakeRPC({ ...basePool, pendingMaxTotalExposure: w(500_000_000), pendingMaxAdvance: w(10_000_000), capRaiseExecutableAt: w(later) }, baseVault),
  );
  assert.ok(g.status === 'ok' && g.pendingCapRaise);
  if (g.status !== 'ok' || !g.pendingCapRaise) return;
  assert.equal(g.pendingCapRaise.maxTotalExposure, '500000000');
  assert.equal(g.pendingCapRaise.executable, false);
  assert.equal(g.pendingCapRaise.executableAt, new Date(Number(later) * 1000).toISOString());

  const due = await loadGovernance(config, fakeRPC({ ...basePool, capRaiseExecutableAt: w(NOW) }, baseVault));
  assert.equal(due.status === 'ok' && due.pendingCapRaise?.executable, true, 'executable at exactly the deadline');
});

test('a pending admin handoff is surfaced per contract', async () => {
  const g = await loadGovernance(
    config,
    fakeRPC(
      { ...basePool, pendingAdmin: addr(NEXT), adminTransferAcceptableAt: w(NOW + 10n) },
      { ...baseVault, pendingAdmin: addr(NEXT), adminTransferAcceptableAt: w(NOW - 10n) },
    ),
  );
  assert.ok(g.status === 'ok');
  if (g.status !== 'ok') return;
  assert.equal(g.pool.pendingAdmin?.address, NEXT.toLowerCase());
  assert.equal(g.pool.pendingAdmin?.acceptable, false);
  assert.equal(g.vault.pendingAdmin?.acceptable, true);
});

test('contracts without the controls are unsupported, not zeros', async () => {
  // The live testnet contracts revert on these getters. Zero caps would claim "nothing may be lent".
  const g = await loadGovernance({ ...config, chainId: 5042002 }, fakeRPC({}, {}, { chainId: 5042002, missing: true }));
  assert.equal(g.status, 'unsupported');
  assert.match(g.status === 'unsupported' ? g.reason : '', /predate on-chain capital controls/);
});

test('a non-revert RPC failure is an error, not "unsupported"', async () => {
  const rpc = (async (method: string) => {
    if (method === 'eth_chainId') return '0x13b2';
    if (method === 'eth_getBlockByNumber') return { number: '0x64', timestamp: '0x1' };
    throw new RPCError('Arc RPC eth_call HTTP 503', 503, undefined, 'upstream unavailable');
  }) as RPCTransport;
  await assert.rejects(loadGovernance(config, rpc), /503/);
});

test('an RPC serving another chain is refused before any read', async () => {
  await assert.rejects(loadGovernance(config, fakeRPC(basePool, baseVault, { chainId: 5042002 })), /chain ID 5042002 does not match/);
});
