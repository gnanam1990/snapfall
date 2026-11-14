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

const OS = governanceChainInternals.OPERATOR_SELECTOR;
const OT = governanceChainInternals.OPERATOR_TOPIC;

/** The allowlist as the fake pool holds it. Absent ⇒ a pool that predates the allowlist. */
interface OperatorFake {
  delay?: bigint;
  allowed?: string[];
  queued?: Record<string, bigint>; // address → executableAt
  /** Logs the pool emitted: [blockNumber, topic0, operator]. */
  logs?: Array<[number, string, string]>;
  /** Make eth_getLogs fail (not a range cap). */
  getLogsFails?: boolean;
  /** Largest range eth_getLogs accepts before answering a range-cap error. */
  rangeCap?: number;
  getLogsCalls?: Array<{ from: number; to: number }>;
  /** Answer eth_getLogs after a real timer tick, so concurrent loads genuinely overlap. */
  getLogsDelayMs?: number;
  /** A block whose operator log comes back with a malformed operator topic. */
  malformedAt?: number;
  /** Addresses whose allowedOperator read fails (not a revert: the RPC errors). */
  unreadable?: string[];
  /** Make the OPERATOR_GRANT_DELAY read fail with a non-revert RPC error. */
  delayFails?: boolean;
  /** Addresses whose allowedOperator/operatorGrantExecutableAt reads never answer and ignore the
   *  abort signal, like an RPC that accepted the request and stalled. */
  stalled?: string[];
  /** Make the OPERATOR_GRANT_DELAY read never answer, ignoring the abort signal. */
  delayStalls?: boolean;
  /** Abort signals the operator-state reads were given. */
  signals?: AbortSignal[];
}

const never = () => new Promise<never>(() => {});

const missing = () => new RPCError('Arc RPC eth_call failed: execution reverted', 200, 3, 'execution reverted');

function fakeRPC(
  pool: Answers,
  vault: Answers = {},
  opts: { chainId?: number; missing?: boolean; operators?: OperatorFake; head?: number } = {},
): RPCTransport {
  const byPool = new Map<string, string>(Object.entries(pool).map(([k, v]) => [S[k as keyof typeof S], v as string]));
  const byVault = new Map<string, string>(Object.entries(vault).map(([k, v]) => [S[k as keyof typeof S], v as string]));
  const head = '0x' + (opts.head ?? 100).toString(16);
  const op = opts.operators;
  return (async (method: string, params: unknown[], options?: { signal?: AbortSignal }) => {
    if (method === 'eth_chainId') return '0x' + (opts.chainId ?? 5042).toString(16);
    if (method === 'eth_getBlockByNumber') return { number: head, timestamp: '0x' + NOW.toString(16) };
    if (method === 'eth_getLogs') {
      const [filter] = params as [{ address: string; fromBlock: string; toBlock: string; topics: string[][] }];
      assert.equal(filter.address, POOL);
      assert.deepEqual(filter.topics, [[OT.allowed, OT.queued, OT.cancelled]]);
      const from = Number(BigInt(filter.fromBlock));
      const to = Number(BigInt(filter.toBlock));
      op?.getLogsCalls?.push({ from, to });
      if (op?.getLogsDelayMs) await new Promise((r) => setTimeout(r, op.getLogsDelayMs));
      if (op?.getLogsFails) throw new RPCError('Arc RPC eth_getLogs HTTP 500: https://rpc.example/v2/fake-key', 500, -32000, 'boom');
      if (op?.rangeCap !== undefined && to - from + 1 > op.rangeCap) {
        throw new RPCError('Arc RPC eth_getLogs HTTP 400', 400, -32600, 'up to a 10 block range');
      }
      return (op?.logs ?? [])
        .filter(([b]) => b >= from && b <= to)
        .map(([b, topic, who]) => ({
          blockNumber: '0x' + b.toString(16),
          topics: [topic, b === op?.malformedAt ? '0x1234' : addr(who)],
          data: '0x',
        }));
    }
    assert.equal(method, 'eth_call');
    const [{ to, data }, block] = params as [{ to: string; data: string }, string];
    assert.equal(block, head, 'every read is pinned to the same block');
    if (opts.missing) throw missing();
    if (to === POOL && Object.values(OS).some((sel) => data.startsWith(sel))) {
      if (!op) throw missing();
      if (options?.signal) op.signals?.push(options.signal);
      if (data === OS.OPERATOR_GRANT_DELAY) {
        if (op.delayStalls) return never();
        if (op.delayFails) throw new RPCError('Arc RPC eth_call HTTP 503: https://rpc.example/v2/fake-key', 503, undefined, 'unavailable');
        return w(op.delay ?? 172_800n);
      }
      const who = '0x' + data.slice(-40);
      if ((op.stalled ?? []).some((a) => a.toLowerCase() === who)) return never();
      if ((op.unreadable ?? []).some((a) => a.toLowerCase() === who)) {
        throw new RPCError('Arc RPC eth_call HTTP 503: https://rpc.example/v2/fake-key', 503, undefined, 'unavailable');
      }
      if (data.startsWith(OS.allowedOperator)) return w((op.allowed ?? []).some((a) => a.toLowerCase() === who) ? 1 : 0);
      const queued = Object.entries(op.queued ?? {}).find(([a]) => a.toLowerCase() === who);
      return w(queued ? queued[1] : 0n);
    }
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

// ── Operator allowlist ───────────────────────────────────────────────────────

const TREASURY = '0x00000000000000000000000000000000000000B0';
const ROGUE = '0x00000000000000000000000000000000000000E1';
const OTHER = '0x00000000000000000000000000000000000000C2';
const REVOKED = '0x00000000000000000000000000000000000000D3';
const reset = governanceChainInternals.resetOperatorScan;

test('a pool that predates the operator allowlist says so, and the caps still read', async () => {
  reset();
  const g = await loadGovernance({ ...config, operatorAddress: TREASURY }, fakeRPC(basePool, baseVault));
  assert.ok(g.status === 'ok');
  if (g.status !== 'ok') return;
  assert.equal(g.operatorAllowlist.status, 'unsupported');
  assert.match(g.operatorAllowlist.status === 'unsupported' ? g.operatorAllowlist.reason : '', /predates the operator allowlist/);
  assert.equal(g.lossBudget.ceiling, '50000000');
});

test('without discovery only the configured operator is checked, and the snapshot says so', async () => {
  reset();
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY },
    fakeRPC(basePool, baseVault, { operators: { allowed: [TREASURY] } }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  const a = g.operatorAllowlist;
  assert.equal(a.grantDelaySeconds, 172_800);
  assert.equal(a.configuredOperator, TREASURY.toLowerCase());
  assert.deepEqual(a.allowed, [TREASURY.toLowerCase()]);
  assert.deepEqual(a.pendingGrants, []);
  assert.equal(a.discovery.status, 'off');
  assert.deepEqual(a.operators, [{ address: TREASURY.toLowerCase(), allowed: true, configured: true, pendingGrant: null }]);
});

test('a configured operator that is not allowed is still listed, as not allowed', async () => {
  reset();
  const g = await loadGovernance({ ...config, operatorAddress: TREASURY }, fakeRPC(basePool, baseVault, { operators: {} }));
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  assert.deepEqual(g.operatorAllowlist.operators, [
    { address: TREASURY.toLowerCase(), allowed: false, configured: true, pendingGrant: null },
  ]);
  assert.deepEqual(g.operatorAllowlist.allowed, []);
});

test('discovery surfaces a queued grant to an unknown address, and every allowed operator, from the logs', async () => {
  reset();
  const operators: OperatorFake = {
    allowed: [TREASURY, OTHER],
    queued: { [ROGUE]: NOW + 3600n, [OTHER]: NOW - 1n },
    logs: [
      [10, OT.allowed, TREASURY],
      [20, OT.allowed, OTHER],
      [30, OT.allowed, REVOKED],
      [31, OT.allowed, REVOKED], // revoked again later: no longer allowed, nothing queued
      [40, OT.queued, ROGUE],
      [41, OT.queued, OTHER],
    ],
  };
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorDiscovery: { startBlock: 5, chunkBlocks: 1000 } },
    fakeRPC(basePool, baseVault, { operators }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  const a = g.operatorAllowlist;
  assert.deepEqual(a.discovery, { status: 'complete', fromBlock: 5, throughBlock: 100 });
  assert.deepEqual(a.allowed.sort(), [TREASURY, OTHER].map((x) => x.toLowerCase()).sort());
  assert.ok(!a.operators.some((o) => o.address === REVOKED.toLowerCase()), 'a revoked address with nothing queued is not shown');
  // Soonest first; the grant at its deadline is executable, the rogue one is not yet.
  assert.deepEqual(a.pendingGrants, [
    { address: OTHER.toLowerCase(), executableAt: new Date(Number(NOW - 1n) * 1000).toISOString(), executable: true },
    { address: ROGUE.toLowerCase(), executableAt: new Date(Number(NOW + 3600n) * 1000).toISOString(), executable: false },
  ]);
  const rogue = a.operators.find((o) => o.address === ROGUE.toLowerCase());
  assert.deepEqual(rogue, {
    address: ROGUE.toLowerCase(),
    allowed: false,
    configured: false,
    pendingGrant: { executableAt: new Date(Number(NOW + 3600n) * 1000).toISOString(), executable: false },
  });
});

test('the operator scan is incremental: a second load scans only the new blocks', async () => {
  reset();
  const calls: Array<{ from: number; to: number }> = [];
  const discovery = { startBlock: 0, chunkBlocks: 1000 };
  const ops: OperatorFake = { allowed: [OTHER], logs: [[50, OT.allowed, OTHER]], getLogsCalls: calls };
  await loadGovernance({ ...config, operatorDiscovery: discovery }, fakeRPC(basePool, baseVault, { operators: ops }));
  assert.deepEqual(calls, [{ from: 0, to: 100 }]);
  const g = await loadGovernance(
    { ...config, operatorDiscovery: discovery },
    fakeRPC(basePool, baseVault, { operators: { ...ops, logs: [] }, head: 150 }),
  );
  assert.deepEqual(calls, [{ from: 0, to: 100 }, { from: 101, to: 150 }]);
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  assert.deepEqual(g.operatorAllowlist.allowed, [OTHER.toLowerCase()], 'an address found earlier is still checked');
});

test('a scan that runs out of budget is partial, says how far it got, and continues next load', async () => {
  reset();
  let clock = 0;
  const calls: Array<{ from: number; to: number }> = [];
  const ops: OperatorFake = { queued: { [ROGUE]: NOW + 10n }, logs: [[90, OT.queued, ROGUE]], getLogsCalls: calls };
  // Every getLogs costs 1s against a 2.5s budget: three chunks of 10, then stop.
  const rpc = fakeRPC(basePool, baseVault, { operators: ops });
  const slow = (async (method: string, params: unknown[]) => {
    if (method === 'eth_getLogs') clock += 1000;
    return rpc(method, params);
  }) as RPCTransport;
  const discovery = { startBlock: 51, chunkBlocks: 10, budgetMs: 2500, now: () => clock };
  const first = await loadGovernance({ ...config, operatorDiscovery: discovery }, slow);
  assert.ok(first.status === 'ok' && first.operatorAllowlist.status === 'ok');
  if (first.status !== 'ok' || first.operatorAllowlist.status !== 'ok') return;
  assert.equal(first.operatorAllowlist.discovery.status, 'partial');
  assert.equal(
    first.operatorAllowlist.discovery.status === 'partial' && first.operatorAllowlist.discovery.throughBlock,
    80,
  );
  assert.deepEqual(first.operatorAllowlist.pendingGrants, [], 'not found yet, and the snapshot does not claim complete');

  const second = await loadGovernance({ ...config, operatorDiscovery: discovery }, slow);
  assert.ok(second.status === 'ok' && second.operatorAllowlist.status === 'ok');
  if (second.status !== 'ok' || second.operatorAllowlist.status !== 'ok') return;
  assert.equal(second.operatorAllowlist.discovery.status, 'complete');
  assert.equal(second.operatorAllowlist.pendingGrants[0]?.address, ROGUE.toLowerCase());
  assert.equal(calls[3]?.from, 81, 'the second load resumed where the first stopped');
});

test('one eth_getLogs slower than the remaining budget is cut off: partial, cursor kept, next load resumes', async () => {
  reset();
  const ops: OperatorFake = { queued: { [ROGUE]: NOW + 10n }, logs: [[90, OT.queued, ROGUE]] };
  const fast = fakeRPC(basePool, baseVault, { operators: ops });
  const signals: AbortSignal[] = [];
  let getLogs = 0;
  // The second eth_getLogs never answers and ignores its signal: only the scan's own deadline can
  // end the wait. Real clock, so the budget is real time.
  const stalls = (async (method: string, params: unknown[], options?: { signal?: AbortSignal }) => {
    if (method === 'eth_getLogs') {
      if (options?.signal) signals.push(options.signal);
      getLogs += 1;
      if (getLogs === 2) return new Promise(() => {});
    }
    return fast(method, params);
  }) as RPCTransport;
  const discovery = { startBlock: 51, chunkBlocks: 10, budgetMs: 150 };

  const started = Date.now();
  const first = await loadGovernance({ ...config, operatorDiscovery: discovery }, stalls);
  const elapsed = Date.now() - started;
  assert.ok(elapsed < 1_500, `the load returned within its budget, not with the stalled request (${elapsed}ms)`);
  assert.ok(first.status === 'ok' && first.operatorAllowlist.status === 'ok');
  if (first.status !== 'ok' || first.operatorAllowlist.status !== 'ok') return;
  const d = first.operatorAllowlist.discovery;
  assert.equal(d.status, 'partial');
  assert.equal(d.status === 'partial' && d.throughBlock, 60, 'the stalled range (61-70) was not committed');
  assert.match(d.status === 'partial' ? d.reason : '', /outran the scan budget/);
  assert.equal(signals.length, 2, 'every eth_getLogs carries a deadline signal');
  assert.equal(signals[1].aborted, true, 'the stalled request was told to stop');
  assert.equal(signals[0].aborted, false, 'a request that answered in time was not');

  const calls: Array<{ from: number; to: number }> = [];
  const second = await loadGovernance(
    { ...config, operatorDiscovery: discovery },
    fakeRPC(basePool, baseVault, { operators: { ...ops, getLogsCalls: calls } }),
  );
  assert.ok(second.status === 'ok' && second.operatorAllowlist.status === 'ok');
  if (second.status !== 'ok' || second.operatorAllowlist.status !== 'ok') return;
  assert.equal(second.operatorAllowlist.discovery.status, 'complete');
  assert.equal(calls[0]?.from, 61, 'the next load asks for the cut-off range again');
  assert.equal(second.operatorAllowlist.pendingGrants[0]?.address, ROGUE.toLowerCase());
});

test('a provider range cap shrinks the scan window instead of failing', async () => {
  reset();
  const calls: Array<{ from: number; to: number }> = [];
  const g = await loadGovernance(
    { ...config, operatorDiscovery: { startBlock: 61, chunkBlocks: 40 } },
    fakeRPC(basePool, baseVault, { operators: { allowed: [OTHER], logs: [[99, OT.allowed, OTHER]], rangeCap: 10, getLogsCalls: calls } }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  assert.equal(g.operatorAllowlist.discovery.status, 'complete');
  assert.deepEqual(g.operatorAllowlist.allowed, [OTHER.toLowerCase()]);
});

test('a failed scan is reported as failed without the RPC message, and the configured operator is still read', async () => {
  reset();
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorDiscovery: { startBlock: 0 } },
    fakeRPC(basePool, baseVault, { operators: { allowed: [TREASURY], getLogsFails: true } }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  const d = g.operatorAllowlist.discovery;
  assert.equal(d.status, 'failed');
  assert.doesNotMatch(d.status === 'failed' ? d.reason : '', /rpc\.example|fake-key/);
  assert.deepEqual(g.operatorAllowlist.allowed, [TREASURY.toLowerCase()]);
});

test('a malformed configured operator address is refused, not ignored', async () => {
  reset();
  await assert.rejects(
    loadGovernance({ ...config, operatorAddress: '0x1234' }, fakeRPC(basePool, baseVault, { operators: {} })),
    /operatorAddress is not an address/,
  );
});

// ── One scan for concurrent loads, a remembered range cap, and failures that degrade ─────────

test('concurrent cold loads share one operator scan: N loads cost one scan of eth_getLogs calls', async () => {
  const discovery = { startBlock: 61, chunkBlocks: 40 };
  const ops = (calls: Array<{ from: number; to: number }>): OperatorFake => ({
    allowed: [OTHER],
    logs: [[99, OT.allowed, OTHER]],
    rangeCap: 10,
    getLogsCalls: calls,
    getLogsDelayMs: 1,
  });

  // What ONE cold load costs: two refused windows (40, 20), then four ranges of 10.
  reset();
  const single: Array<{ from: number; to: number }> = [];
  await loadGovernance({ ...config, operatorDiscovery: discovery }, fakeRPC(basePool, baseVault, { operators: ops(single) }));
  assert.equal(single.length, 6);

  reset();
  const shared: Array<{ from: number; to: number }> = [];
  const rpc = fakeRPC(basePool, baseVault, { operators: ops(shared) });
  const loads = await Promise.all(
    Array.from({ length: 10 }, () => loadGovernance({ ...config, operatorDiscovery: discovery }, rpc)),
  );
  assert.deepEqual(shared, single, 'ten concurrent loads made exactly the calls of one');
  for (const g of loads) {
    assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
    if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
    assert.equal(g.operatorAllowlist.discovery.status, 'complete');
    assert.deepEqual(g.operatorAllowlist.allowed, [OTHER.toLowerCase()], 'every load sees what the shared scan found');
  }
});

test('the provider range cap, once found, is kept: the next load starts at the window that works', async () => {
  reset();
  const calls: Array<{ from: number; to: number }> = [];
  const discovery = { startBlock: 61, chunkBlocks: 40 };
  const ops: OperatorFake = { allowed: [], rangeCap: 10, getLogsCalls: calls };
  await loadGovernance({ ...config, operatorDiscovery: discovery }, fakeRPC(basePool, baseVault, { operators: ops }));
  calls.length = 0;
  await loadGovernance({ ...config, operatorDiscovery: discovery }, fakeRPC(basePool, baseVault, { operators: ops, head: 150 }));
  assert.deepEqual(
    calls,
    [101, 111, 121, 131, 141].map((from) => ({ from, to: from + 9 })),
    'no refused 40- or 20-block window is asked again',
  );
});

test('a malformed operator log stops discovery, not the governance view: caps still read, scan resumes', async () => {
  reset();
  const calls: Array<{ from: number; to: number }> = [];
  const ops: OperatorFake = {
    allowed: [TREASURY, OTHER],
    logs: [
      [20, OT.allowed, OTHER],
      [70, OT.allowed, ROGUE],
    ],
    malformedAt: 70,
    getLogsCalls: calls,
  };
  const discovery = { startBlock: 0, chunkBlocks: 50 };
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorDiscovery: discovery },
    fakeRPC(basePool, baseVault, { operators: ops }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  assert.equal(g.lossBudget.ceiling, '50000000', 'the caps are still shown');
  const d = g.operatorAllowlist.discovery;
  assert.equal(d.status, 'failed');
  assert.equal(d.status === 'failed' && d.throughBlock, 49, 'the good chunk is kept; the bad one is not committed');
  assert.match(d.status === 'failed' ? d.reason : '', /blocks 50–99 is malformed/);
  assert.deepEqual(g.operatorAllowlist.allowed.sort(), [TREASURY, OTHER].map((x) => x.toLowerCase()).sort());

  // The next load asks for the same range again, not past it.
  const before = calls.length;
  await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorDiscovery: discovery },
    fakeRPC(basePool, baseVault, { operators: { ...ops, malformedAt: undefined } }),
  );
  assert.deepEqual(calls[before], { from: 50, to: 99 });
});

test('one operator whose state will not read is unknown, not "not allowed", and the rest still read', async () => {
  reset();
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorDiscovery: { startBlock: 0, chunkBlocks: 1000 } },
    fakeRPC(basePool, baseVault, {
      operators: {
        allowed: [TREASURY, ROGUE],
        logs: [
          [10, OT.allowed, TREASURY],
          [20, OT.allowed, ROGUE],
        ],
        unreadable: [ROGUE],
      },
    }),
  );
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  const a = g.operatorAllowlist;
  assert.equal(g.lossBudget.ceiling, '50000000');
  assert.deepEqual(a.allowed, [TREASURY.toLowerCase()]);
  assert.deepEqual(a.unknown, [ROGUE.toLowerCase()]);
  const rogue = a.operators.find((o) => o.address === ROGUE.toLowerCase());
  assert.equal(rogue?.allowed, null, 'unknown is not false');
  assert.match(rogue?.unknown ?? '', /could not be read at block 100/);
  assert.doesNotMatch(rogue?.unknown ?? '', /rpc\.example|fake-key/);
  assert.deepEqual(a.pendingGrants, []);
});

test('an OPERATOR_GRANT_DELAY read that fails (not a revert) leaves the allowlist unavailable and the caps shown', async () => {
  reset();
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY },
    fakeRPC(basePool, baseVault, { operators: { allowed: [TREASURY], delayFails: true } }),
  );
  assert.ok(g.status === 'ok');
  if (g.status !== 'ok') return;
  assert.equal(g.lossBudget.ceiling, '50000000');
  assert.equal(g.operatorAllowlist.status, 'unavailable');
  assert.doesNotMatch(g.operatorAllowlist.status === 'unavailable' ? g.operatorAllowlist.reason : '', /rpc\.example|fake-key/);
});

test('a stalled operator-state read is unknown after the deadline, and the caps and other operators still read', async () => {
  reset();
  const signals: AbortSignal[] = [];
  const started = Date.now();
  const g = await loadGovernance(
    {
      ...config,
      operatorAddress: TREASURY,
      operatorDiscovery: { startBlock: 0, chunkBlocks: 1000 },
      operatorReadBudgetMs: 40,
    },
    fakeRPC(basePool, baseVault, {
      operators: {
        allowed: [TREASURY, ROGUE],
        logs: [
          [10, OT.allowed, TREASURY],
          [20, OT.allowed, ROGUE],
        ],
        stalled: [ROGUE],
        signals,
      },
    }),
  );
  const elapsed = Date.now() - started;
  assert.ok(elapsed < 1_000, `the stalled read did not hold the load (${elapsed}ms)`);
  assert.ok(g.status === 'ok' && g.operatorAllowlist.status === 'ok');
  if (g.status !== 'ok' || g.operatorAllowlist.status !== 'ok') return;
  assert.equal(g.lossBudget.ceiling, '50000000', 'the caps already read are returned');
  const a = g.operatorAllowlist;
  assert.deepEqual(a.allowed, [TREASURY.toLowerCase()]);
  assert.deepEqual(a.unknown, [ROGUE.toLowerCase()]);
  const rogue = a.operators.find((o) => o.address === ROGUE.toLowerCase());
  assert.equal(rogue?.allowed, null, 'a read that timed out is unknown, never "not allowed"');
  assert.match(rogue?.unknown ?? '', /did not answer within 40 ms at block 100/);
  assert.ok(signals.some((s) => s.aborted), 'the stalled request was told to stop');
});

test('a stalled OPERATOR_GRANT_DELAY read leaves the allowlist unavailable after the deadline, and the caps shown', async () => {
  reset();
  const started = Date.now();
  const g = await loadGovernance(
    { ...config, operatorAddress: TREASURY, operatorReadBudgetMs: 40 },
    fakeRPC(basePool, baseVault, { operators: { allowed: [TREASURY], delayStalls: true } }),
  );
  assert.ok(Date.now() - started < 1_000);
  assert.ok(g.status === 'ok');
  if (g.status !== 'ok') return;
  assert.equal(g.lossBudget.ceiling, '50000000');
  assert.equal(g.operatorAllowlist.status, 'unavailable');
  assert.match(
    g.operatorAllowlist.status === 'unavailable' ? g.operatorAllowlist.reason : '',
    /did not answer the OPERATOR_GRANT_DELAY\(\) read within 40 ms/,
  );
});
