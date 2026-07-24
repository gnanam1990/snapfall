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
