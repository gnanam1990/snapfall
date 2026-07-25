import assert from 'node:assert/strict';
import test from 'node:test';
import { createRPCTransport, floatChainInternals, loadFloatSnapshot, type RPCTransport } from './floatChain';

const ORG = '0x7a9c0000000000000000000000000000000041d2';
const POOL = '0xde9f58a997cf7a3258d09a797eb5546877dc86e5';
const JOB_OPEN = `0x${'11'.repeat(32)}`;
const JOB_REPAID = `0x${'22'.repeat(32)}`;
const TX_OPEN = `0x${'aa'.repeat(32)}`;
const TX_REPAID = `0x${'bb'.repeat(32)}`;

const hex = (value: bigint | number) => `0x${BigInt(value).toString(16)}`;
const data = (...values: bigint[]) => `0x${values.map(floatChainInternals.word).join('')}`;
const addressTopic = (address: string) => `0x${address.slice(2).padStart(64, '0')}`;

function fixtureRPC(chainId = 5042002): RPCTransport {
  return async <T>(method: string, params: unknown[]): Promise<T> => {
    if (method === 'eth_chainId') return hex(chainId) as T;
    if (method === 'eth_blockNumber') return hex(100) as T;
    if (method === 'eth_call') {
      const call = params[0] as { data: string };
      const selector = call.data.slice(0, 10);
      const values: Record<string, bigint> = {
        [floatChainInternals.SELECTOR.totalAssets]: 150_200_000n,
        [floatChainInternals.SELECTOR.totalOutstanding]: 12_500_000n,
        [floatChainInternals.SELECTOR.reserve]: 50_000n,
        [floatChainInternals.SELECTOR.advanceRate]: 5_500n,
        [floatChainInternals.SELECTOR.acceptedJobs]: 1n,
        [floatChainInternals.SELECTOR.writtenOffJobs]: 0n,
      };
      return data(values[selector] ?? 0n) as T;
    }
    if (method === 'eth_getLogs') {
      return [
        {
          address: POOL,
          blockNumber: hex(95),
          logIndex: hex(0),
          transactionHash: TX_REPAID,
          topics: [floatChainInternals.TOPIC.repaid, JOB_REPAID],
          data: data(12_500_000n, 250_000n, 50_000n),
        },
        {
          address: POOL,
          blockNumber: hex(99),
          logIndex: hex(1),
          transactionHash: TX_OPEN,
          topics: [floatChainInternals.TOPIC.issued, JOB_OPEN, addressTopic(ORG)],
          data: data(12_500_000n, 250_000n, 5_000n),
        },
      ] as T;
    }
    if (method === 'eth_getBlockByNumber') return { timestamp: hex(1_753_350_000) } as T;
    throw new Error(`unexpected RPC method ${method}`);
  };
}

test('builds every Float metric and open advance from chain views and logs', async () => {
  const snapshot = await loadFloatSnapshot(
    {
      chainId: 5042002,
      rpcUrl: 'https://rpc.invalid',
      poolAddress: POOL,
      explorerUrl: 'https://testnet.arcscan.app',
      startBlock: 90,
      orgAddress: ORG,
    },
    fixtureRPC(),
  );

  assert.equal(snapshot.totalAssetsUsdc, '150200000');
  assert.equal(snapshot.totalOutstandingUsdc, '12500000');
  assert.equal(snapshot.availableLiquidityUsdc, '137700000');
  assert.equal(snapshot.utilizationBps, 832);
  assert.equal(snapshot.feesAccruedUsdc, '250000');
  assert.equal(snapshot.reserveUsdc, '50000');
  assert.equal(snapshot.orgRateBps, 5500);
  assert.equal(snapshot.acceptedJobs, 1);
  assert.equal(snapshot.writtenOffJobs, 0);
  assert.equal(snapshot.openAdvances?.length, 1);
  assert.equal(snapshot.openAdvances?.[0]?.jobId, JOB_OPEN);
  assert.equal(snapshot.openAdvances?.[0]?.openedAt, '2025-07-24T09:40:00.000Z');
  assert.equal(snapshot.openAdvances?.[0]?.explorerUrl, `https://testnet.arcscan.app/tx/${TX_OPEN}`);
  assert.equal(snapshot.historyStatus, 'complete');
});

test('returns current pool views without inventing history on the public RPC path', async () => {
  const snapshot = await loadFloatSnapshot(
    {
      chainId: 5042002,
      rpcUrl: 'https://rpc.invalid',
      poolAddress: POOL,
      explorerUrl: 'https://testnet.arcscan.app',
      startBlock: 90,
      orgAddress: ORG,
    },
    fixtureRPC(),
    { includeHistory: false },
  );

  assert.equal(snapshot.totalAssetsUsdc, '150200000');
  assert.equal(snapshot.orgRateBps, 5500);
  assert.equal(snapshot.feesAccruedUsdc, null);
  assert.equal(snapshot.openAdvances, null);
  assert.equal(snapshot.losses, null);
  assert.equal(snapshot.historyStatus, 'unavailable');
});

test('fails closed when the RPC is not the configured chain', async () => {
  await assert.rejects(
    loadFloatSnapshot(
      {
        chainId: 5042002,
        rpcUrl: 'https://rpc.invalid',
        poolAddress: POOL,
        explorerUrl: 'https://testnet.arcscan.app',
        startBlock: 90,
      },
      fixtureRPC(1),
    ),
    /does not match configured chain ID/,
  );
});

test('retries a transient public-RPC rate limit without changing the request ID', async () => {
  const originalFetch = globalThis.fetch;
  const requestIds: number[] = [];
  let calls = 0;
  globalThis.fetch = (async (_input: string | URL | Request, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as { id: number };
    requestIds.push(request.id);
    calls += 1;
    if (calls === 1) return new Response('', { status: 429 });
    return Response.json({ jsonrpc: '2.0', id: request.id, result: '0x2a' });
  }) as typeof fetch;

  try {
    const rpc = createRPCTransport('https://rpc.invalid', { retryDelayMs: 0 });
    assert.equal(await rpc<string>('eth_blockNumber', []), '0x2a');
    assert.deepEqual(requestIds, [1, 1]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

// ── RateChanged → rateHistory (change 1) ──────────────────────────────────────

const POOL_RATE = '0xde9f58a997cf7a3258d09a797eb5546877dc0001';
const POOL_INC = '0xde9f58a997cf7a3258d09a797eb5546877dc0002';
const POOL_FULL = '0xde9f58a997cf7a3258d09a797eb5546877dc0003';
const TX_R1 = `0x${'c1'.repeat(32)}`;
const TX_R2 = `0x${'c2'.repeat(32)}`;
const TX_R3 = `0x${'c3'.repeat(32)}`;

const issuedLog = (pool: string, job: string, org: string, tx: string, block: number) => ({
  address: pool, blockNumber: hex(block), logIndex: hex(0), transactionHash: tx,
  topics: [floatChainInternals.TOPIC.issued, job, addressTopic(org)],
  data: data(12_500_000n, 250_000n, 5_000n),
});
const rateLog = (pool: string, org: string, tx: string, block: number, newBps: bigint) => ({
  address: pool, blockNumber: hex(block), logIndex: hex(1), transactionHash: tx,
  topics: [floatChainInternals.TOPIC.rateChanged, addressTopic(org)],
  data: data(newBps),
});
const repaidLog = (pool: string, job: string, tx: string, block: number) => ({
  address: pool, blockNumber: hex(block), logIndex: hex(2), transactionHash: tx,
  topics: [floatChainInternals.TOPIC.repaid, job],
  data: data(12_500_000n, 250_000n, 50_000n),
});

function rangeRPC(allLogs: Array<Record<string, unknown>>, headRef: { head: number }, chainId = 5042002): RPCTransport {
  return async <T>(method: string, params: unknown[]): Promise<T> => {
    if (method === 'eth_chainId') return hex(chainId) as T;
    if (method === 'eth_blockNumber') return hex(headRef.head) as T;
    if (method === 'eth_call') {
      const selector = (params[0] as { data: string }).data.slice(0, 10);
      const values: Record<string, bigint> = {
        [floatChainInternals.SELECTOR.totalAssets]: 150_200_000n,
        [floatChainInternals.SELECTOR.totalOutstanding]: 12_500_000n,
        [floatChainInternals.SELECTOR.reserve]: 50_000n,
        [floatChainInternals.SELECTOR.advanceRate]: 6_000n,
        [floatChainInternals.SELECTOR.acceptedJobs]: 2n,
        [floatChainInternals.SELECTOR.writtenOffJobs]: 0n,
      };
      return data(values[selector] ?? 0n) as T;
    }
    if (method === 'eth_getLogs') {
      const filter = params[0] as { fromBlock: string; toBlock: string };
      const from = BigInt(filter.fromBlock);
      const to = BigInt(filter.toBlock);
      return allLogs.filter((l) => {
        const b = BigInt(l.blockNumber as string);
        return b >= from && b <= to;
      }) as T;
    }
    if (method === 'eth_getBlockByNumber') return { timestamp: hex(1_753_350_000) } as T;
    throw new Error(`unexpected RPC method ${method}`);
  };
}

const cfg = (pool: string) => ({
  chainId: 5042002,
  rpcUrl: 'https://rpc.invalid',
  poolAddress: pool,
  explorerUrl: 'https://testnet.arcscan.app',
  startBlock: 90,
  orgAddress: ORG,
});

test('scans RateChanged into a block-ordered rateHistory with the base origin, non-monotonic', async () => {
  // Two climbs then a write-off tick that LOWERS the rate at a later block — the series
  // must order by block, so 4500 lands AFTER 6000 despite being the smaller value.
  const logs = [
    issuedLog(POOL_RATE, JOB_OPEN, ORG, TX_OPEN, 95),
    rateLog(POOL_RATE, ORG, TX_R1, 100, 5_500n),
    rateLog(POOL_RATE, ORG, TX_R2, 105, 6_000n),
    rateLog(POOL_RATE, ORG, TX_R3, 108, 4_500n),
  ];
  const snapshot = await loadFloatSnapshot(cfg(POOL_RATE), rangeRPC(logs, { head: 110 }));

  assert.deepEqual(snapshot.rateHistoryBps, [
    { rateBps: 5000, blockNumber: 90, txHash: null },
    { rateBps: 5500, blockNumber: 100, txHash: TX_R1 },
    { rateBps: 6000, blockNumber: 105, txHash: TX_R2 },
    { rateBps: 4500, blockNumber: 108, txHash: TX_R3 },
  ]);
});

test('incremental re-scan from the last cached block equals a single full scan', async () => {
  const logs = (pool: string) => [
    issuedLog(pool, JOB_OPEN, ORG, TX_OPEN, 95),
    rateLog(pool, ORG, TX_R1, 100, 5_500n),
    issuedLog(pool, JOB_REPAID, ORG, TX_R2, 103),
    rateLog(pool, ORG, TX_R2, 108, 6_000n),
    repaidLog(pool, JOB_OPEN, TX_REPAID, 109),
  ];

  // Full: one scan to head 110.
  const full = await loadFloatSnapshot(cfg(POOL_FULL), rangeRPC(logs(POOL_FULL), { head: 110 }));

  // Incremental: scan to 100, then again to 110 (only 101..110 is re-fetched).
  const headRef = { head: 100 };
  const incRPC = rangeRPC(logs(POOL_INC), headRef);
  await loadFloatSnapshot(cfg(POOL_INC), incRPC);
  headRef.head = 110;
  const inc = await loadFloatSnapshot(cfg(POOL_INC), incRPC);

  assert.equal(inc.feesAccruedUsdc, full.feesAccruedUsdc);
  assert.deepEqual(inc.rateHistoryBps, full.rateHistoryBps);
  assert.deepEqual(
    inc.openAdvances?.map((a) => a.jobId).sort(),
    full.openAdvances?.map((a) => a.jobId).sort(),
  );
  // The repaid advance dropped out of both; the still-open one remains.
  assert.equal(inc.openAdvances?.length, 1);
  assert.equal(inc.openAdvances?.[0]?.jobId, JOB_REPAID);
});
