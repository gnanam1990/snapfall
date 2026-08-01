import assert from 'node:assert/strict';
import test from 'node:test';
import { assembleSnapshot, createRPCTransport, floatChainInternals, loadFloatSnapshot, loadFloatViews, peekFloatHistory, RPCError, type RPCTransport } from './floatChain';

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
const writtenOffLog = (pool: string, job: string, tx: string, block: number) => ({
  address: pool, blockNumber: hex(block), logIndex: hex(3), transactionHash: tx,
  topics: [floatChainInternals.TOPIC.writtenOff, job],
  data: data(50_000n, 30_000n, 20_000n), // bondSlashed, reserveUsed, socialized
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

// The failure-atomicity regression, from the review of PR #50.
//
// mergeLogs advances the money accumulators, then an await sits between that and the cursor
// advance. When the cached ScanState was extended IN PLACE, anything throwing in that window
// banked the additions with a stale cursor, so the next scan refetched the same range and added
// every fee, loss and rate tick again. It was silent (route.ts swallows the error), it never
// self-healed (SCAN_CACHE has no eviction), and the payload still said historyStatus 'complete'.
//
// Fixture note that matters: a job Issued AND Repaid inside the same batch is deleted from `open`,
// so enrichOpenedAt makes no call and nothing throws. The window needs a STILL-OPEN advance.
const JOB_LOST = `0x${'33'.repeat(32)}`;
const TX_LOST = `0x${'dd'.repeat(32)}`;
const POOL_ATOMIC = '0xde9f58a997cf7a3258d09a797eb5546877dc0006';
// A separate address for the baseline: SCAN_CACHE is process-global and keyed by pool, so
// reusing another test's pool would fold that test's logs into this one's expectation.
const POOL_ATOMIC_REF = '0xde9f58a997cf7a3258d09a797eb5546877dc0007';

test('a scan that throws after merging does not double-count on the next scan', async () => {
  // Two jobs, deliberately. JOB_REPAID is issued in the WARM range and repaid in the EXTENSION
  // range, so the extension genuinely advances feesAccrued (that is the number that doubles).
  // JOB_OPEN is issued in the extension range and never repaid, so it survives in `open` and
  // enrichOpenedAt is actually called, which is what gives the failure something to throw from.
  const logs = [
    issuedLog(POOL_ATOMIC, JOB_REPAID, ORG, TX_R2, 95),
    repaidLog(POOL_ATOMIC, JOB_REPAID, TX_REPAID, 105),
    // written off in the extension window too, so bondSlashed/reserveUsed/socialized are
    // non-zero and the losses assertion below can actually discriminate a double count
    issuedLog(POOL_ATOMIC, JOB_LOST, ORG, TX_LOST, 96),
    writtenOffLog(POOL_ATOMIC, JOB_LOST, TX_LOST, 106),
    issuedLog(POOL_ATOMIC, JOB_OPEN, ORG, TX_OPEN, 107),
    rateLog(POOL_ATOMIC, ORG, TX_R1, 108, 5_500n),
  ];

  const headRef = { head: 100 };
  const fail = { blocks: false };
  const seen = { getLogs: 0, blocks: 0 };
  const base = rangeRPC(logs, headRef);
  const rpc: RPCTransport = async <T,>(method: string, params: unknown[]): Promise<T> => {
    if (method === 'eth_getLogs') seen.getLogs += 1;
    if (method === 'eth_getBlockByNumber') {
      seen.blocks += 1;
      if (fail.blocks) {
        throw new RPCError('Arc RPC eth_getBlockByNumber returned HTTP 429', 429, undefined, 'rate limited');
      }
    }
    return base<T>(method, params);
  };

  // 1. warm the cache to 100
  const warm = await loadFloatSnapshot(cfg(POOL_ATOMIC), rpc);
  const feesAt100 = warm.feesAccruedUsdc;

  // 2. extend to 110 with enrichOpenedAt failing. The route swallows this, so the caller sees
  //    whatever the snapshot says; what matters is what the CACHE now holds.
  headRef.head = 110;
  fail.blocks = true;
  const before = { ...seen };
  let threw = false;
  await loadFloatSnapshot(cfg(POOL_ATOMIC), rpc).catch(() => {
    threw = true;
  });
  // Three separate things, because a bare "it threw" would pass vacuously the moment the injection
  // stops firing, and would still pass if a refactor moved the enrich BEFORE the merge, at which
  // point the window this test exists for is no longer being exercised at all.
  assert.ok(threw, 'step 2 did not throw, so nothing exercised the failure path');
  assert.ok(seen.getLogs > before.getLogs,
    'the extension never fetched logs, so it failed before the merge and not in the merge-to-cursor window');
  assert.ok(seen.blocks > before.blocks,
    'eth_getBlockByNumber was never reached, so enrichOpenedAt no longer runs after the merge and this test needs a new injection point');

  // 3. retry with the RPC healthy. This must equal a single clean scan over 0..110, not double.
  fail.blocks = false;
  const after = await loadFloatSnapshot(cfg(POOL_ATOMIC), rpc);

  const clean = await loadFloatSnapshot(cfg(POOL_ATOMIC_REF), rangeRPC(
    logs.map((l) => ({ ...l, address: POOL_ATOMIC_REF })), { head: 110 },
  ));

  assert.equal(after.feesAccruedUsdc, clean.feesAccruedUsdc,
    'fees double-counted: the failed extension banked its merge with a stale cursor');
  assert.deepEqual(after.losses, clean.losses, 'loss totals double-counted');
  assert.notEqual(clean.losses?.bondSlashedUsdc, '0',
    'losses are all zero, so the parity check above cannot discriminate a double count');
  assert.deepEqual(after.rateHistoryBps, clean.rateHistoryBps,
    'rate history duplicated: same tick at the same block and txHash twice');
  assert.notEqual(after.feesAccruedUsdc, feesAt100,
    'the extension never landed at all, so this test is not exercising the path it claims');
});

// The second blocking finding from the review of PR #50.
//
// The warm path returns cached history immediately and extends toward the new head in the
// background. The view figures always come from eth_call at the CURRENT head, so when the cache
// lags, one payload carries two different observations. totalOutstanding and the sum of
// openAdvances are the same quantity (FloatPool.sol: totalOutstanding is the sum of open
// principals), so the lag does not read as staleness on screen, it reads as a contradiction, and
// nothing in the payload let a consumer detect it.
const POOL_LAG = '0xde9f58a997cf7a3258d09a797eb5546877dc0008';

test('history that lags the view head is reported pending, not complete', async () => {
  const logs = [
    issuedLog(POOL_LAG, JOB_REPAID, ORG, TX_R2, 95),
    // lands AFTER the cache is warmed, so a warm read at head 110 has not seen it
    issuedLog(POOL_LAG, JOB_OPEN, ORG, TX_OPEN, 105),
  ];
  const headRef = { head: 100 };
  const rpc = rangeRPC(logs, headRef);

  // warm to 100
  const warm = await loadFloatSnapshot(cfg(POOL_LAG), rpc);
  assert.equal(warm.historyStatus, 'complete');
  assert.equal(warm.historyScannedThroughBlock, 100);
  assert.equal(warm.blockNumber, 100, 'a fully scanned snapshot must agree with its own head');

  // The chain moves on. Assemble a payload the way the warm route does: fresh views at the new
  // head, plus the history still cached at the old one.
  headRef.head = 110;
  const views = await loadFloatViews(cfg(POOL_LAG), rpc);
  const stale = peekFloatHistory(cfg(POOL_LAG), views.orgAddress);
  assert.ok(stale, 'the cache should still hold the block-100 scan');
  const payload = assembleSnapshot(views, stale, 'complete');

  assert.equal(payload.blockNumber, 110);
  assert.equal(payload.historyScannedThroughBlock, 100);
  assert.equal(
    payload.historyStatus,
    'pending',
    'a complete request must be downgraded when the history does not reach the view head',
  );
});

// ── Forward-chunking + sticky shrink (Alchemy range-cap fix) ──────────────────

const POOL_CHUNK = '0xde9f58a997cf7a3258d09a797eb5546877dc0004';
const POOL_SHRINK = '0xde9f58a997cf7a3258d09a797eb5546877dc0005';

// A fake transport that enforces a per-request block-range CAP (like Alchemy) and counts
// eth_getLogs calls. Throws RPCError(-32600) when the requested window exceeds `cap`.
function cappedRPC(
  allLogs: Array<Record<string, unknown>>,
  headRef: { head: number },
  cap: number,
  counter: { getLogs: number },
): RPCTransport {
  return async <T>(method: string, params: unknown[]): Promise<T> => {
    if (method === 'eth_chainId') return hex(5042002) as T;
    if (method === 'eth_blockNumber') return hex(headRef.head) as T;
    if (method === 'eth_call') {
      const selector = (params[0] as { data: string }).data.slice(0, 10);
      const v: Record<string, bigint> = {
        [floatChainInternals.SELECTOR.totalAssets]: 150_200_000n,
        [floatChainInternals.SELECTOR.totalOutstanding]: 0n,
        [floatChainInternals.SELECTOR.reserve]: 50_000n,
        [floatChainInternals.SELECTOR.advanceRate]: 6_000n,
        [floatChainInternals.SELECTOR.acceptedJobs]: 2n,
        [floatChainInternals.SELECTOR.writtenOffJobs]: 0n,
      };
      return data(v[selector] ?? 0n) as T;
    }
    if (method === 'eth_getLogs') {
      counter.getLogs += 1;
      const f = params[0] as { fromBlock: string; toBlock: string };
      const from = BigInt(f.fromBlock);
      const to = BigInt(f.toBlock);
      if (Number(to - from) + 1 > cap) {
        throw new RPCError(`range too large`, 400, -32600, `up to a ${cap} block range`);
      }
      return allLogs.filter((l) => {
        const b = BigInt(l.blockNumber as string);
        return b >= from && b <= to;
      }) as T;
    }
    if (method === 'eth_getBlockByNumber') return { timestamp: hex(1_753_350_000) } as T;
    throw new Error(`unexpected RPC method ${method}`);
  };
}

test('forward-chunks the scan and stitches windows in order (request count = ceil(range/chunk))', async () => {
  const logs = [
    rateLog(POOL_CHUNK, ORG, TX_R1, 103, 5_500n),
    rateLog(POOL_CHUNK, ORG, TX_R2, 127, 6_000n),
  ];
  const counter = { getLogs: 0 };
  // range 100..130 = 31 blocks, chunk 5 -> ceil(31/5) = 7 windows.
  const snap = await loadFloatSnapshot(
    { ...cfg(POOL_CHUNK), startBlock: 100, scanChunkBlocks: 5 },
    cappedRPC(logs, { head: 130 }, 1_000_000, counter),
  );
  assert.equal(counter.getLogs, 7);
  assert.deepEqual(snap.rateHistoryBps, [
    { rateBps: 5000, blockNumber: 100, txHash: null },
    { rateBps: 5500, blockNumber: 103, txHash: TX_R1 },
    { rateBps: 6000, blockNumber: 127, txHash: TX_R2 },
  ]);
});

test('shrinks the window on a range cap and the smaller size STICKS (no re-try-large per chunk)', async () => {
  const logs = [
    rateLog(POOL_SHRINK, ORG, TX_R1, 103, 5_500n),
    rateLog(POOL_SHRINK, ORG, TX_R2, 127, 6_000n),
  ];
  const counter = { getLogs: 0 };
  // Configured chunk 10000, but the provider caps at 8 blocks. The scan must shrink to <=8
  // and complete. Sticky: after discovering the cap once, the rest scans at the small size
  // — so the count stays far below the non-sticky worst case (re-trying 10000 every chunk).
  const snap = await loadFloatSnapshot(
    { ...cfg(POOL_SHRINK), startBlock: 100, scanChunkBlocks: 10_000 },
    cappedRPC(logs, { head: 130 }, 8, counter),
  );
  assert.deepEqual(snap.rateHistoryBps, [
    { rateBps: 5000, blockNumber: 100, txHash: null },
    { rateBps: 5500, blockNumber: 103, txHash: TX_R1 },
    { rateBps: 6000, blockNumber: 127, txHash: TX_R2 },
  ]);
  // ~11 shrink attempts (10000->..-><=8) + ~8 windows of 4 across 31 blocks. Far below the
  // non-sticky worst case (~11 * 8 = 88).
  assert.ok(counter.getLogs < 30, `sticky shrink kept requests low, got ${counter.getLogs}`);
});
