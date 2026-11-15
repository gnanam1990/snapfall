import assert from 'node:assert/strict';
import test from 'node:test';
import { chmodSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { encodeAbiParameters, encodeEventTopics, type Hex } from 'viem';
import { HISTORY_ABI, assembleHistory, fetchExplorerLogs, mergeLogs, type RawLog } from './chainHistory';
import { HistoryStore } from './historyStore';

const JOB = `0x${'ab'.repeat(32)}` as Hex;
const JOB2 = `0x${'cd'.repeat(32)}` as Hex;
const CUSTOMER = '0x1111111111111111111111111111111111111111';
const OPERATOR = '0x2222222222222222222222222222222222222222';
const LP = '0x3333333333333333333333333333333333333333';
const VAULT = '0xF3830D7C3B8ca873bB0b277c0e179999e3d52681';
const POOL = '0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5';

let block = 100;
function log(eventName: string, args: Record<string, unknown>, data: { types: string[]; values: unknown[] }, address = VAULT): RawLog {
  const topics = encodeEventTopics({ abi: HISTORY_ABI, eventName: eventName as never, args: args as never }) as string[];
  block += 1;
  return {
    address,
    topics,
    data: data.types.length ? encodeAbiParameters(data.types.map((type) => ({ type })), data.values as never) : '0x',
    blockNumber: block,
    logIndex: 0,
    txHash: `0x${block.toString(16).padStart(64, '0')}`,
    timestamp: 1_700_000_000 + block,
  };
}

function happyPath(): RawLog[] {
  block = 100;
  return [
    log('Deposited', { lp: LP }, { types: ['uint256', 'uint256'], values: [20_000_000n, 20_000_000n] }, POOL),
    log('JobCreated', { jobId: JOB, customer: CUSTOMER, operator: OPERATOR }, { types: ['uint256', 'uint256', 'bytes32', 'uint64'], values: [1_000_000n, 500_000n, JOB, 1_800_000_000n] }),
    log('JobFunded', { jobId: JOB }, { types: ['uint256'], values: [1_000_000n] }),
    log('AdvanceIssued', { jobId: JOB, org: OPERATOR }, { types: ['uint256', 'uint256', 'uint16'], values: [500_000n, 10_000n, 5000] }, POOL),
    log('WorkStarted', { jobId: JOB }, { types: [], values: [] }),
    log('ExpenseRecorded', { jobId: JOB }, { types: ['uint256', 'bytes32'], values: [40_000n, JOB] }),
    log('DeliverySubmitted', { jobId: JOB }, { types: ['bytes32'], values: [JOB] }),
    log('AdvanceRepaid', { jobId: JOB }, { types: ['uint256', 'uint256', 'uint256'], values: [500_000n, 10_000n, 2_000n] }, POOL),
    log('JobSettled', { jobId: JOB }, { types: ['uint256', 'uint256'], values: [510_000n, 490_000n] }),
    log('RateChanged', { org: OPERATOR }, { types: ['uint16'], values: [5500] }, POOL),
    log('JobCreated', { jobId: JOB2, customer: CUSTOMER, operator: OPERATOR }, { types: ['uint256', 'uint256', 'bytes32', 'uint64'], values: [2_000_000n, 0n, JOB2, 1_800_000_000n] }),
  ];
}

test('a job is followed from creation to settlement, with its advance and both settlement legs', () => {
  const h = assembleHistory(happyPath(), { network: 'arc-testnet', explorerUrl: 'https://x', source: 'explorer' });
  const job = h.jobs.find((j) => j.jobId === JOB)!;
  assert.equal(job.stage, 'Settled');
  assert.equal(job.paymentUsdc, '1000000');
  assert.equal(job.customer.toLowerCase(), CUSTOMER);
  assert.equal(job.expensesUsdc, '40000');
  assert.equal(job.expenseCount, 1);
  assert.deepEqual(job.advance && { p: job.advance.principalUsdc, s: job.advance.status, r: job.advance.rateBps }, { p: '500000', s: 'Repaid', r: 5000 });
  assert.deepEqual(job.settlement && [job.settlement.advanceRepaidUsdc, job.settlement.operatorNetUsdc], ['510000', '490000']);
  assert.equal(h.jobs.find((j) => j.jobId === JOB2)!.stage, 'Created');
});

test('totals count only what the events say happened', () => {
  const { totals } = assembleHistory(happyPath(), { network: 'arc-testnet', explorerUrl: 'https://x', source: 'explorer' });
  assert.equal(totals.jobs, 2);
  assert.equal(totals.settled, 1);
  assert.equal(totals.active, 0, 'an unpaid job is not active, and the settled one is done');
  assert.equal(totals.settledVolumeUsdc, '1000000');
  assert.equal(totals.operatorEarnedUsdc, '490000');
  assert.equal(totals.advancesIssued, 1);
  assert.equal(totals.advancesRepaid, 1);
  assert.equal(totals.feesEarnedUsdc, '10000');
  assert.equal(totals.depositedUsdc, '20000000');
  assert.equal(totals.latestRateBps, 5500);
});

test('the timeline is newest first, plain-language, and every entry keeps its transaction', () => {
  const h = assembleHistory(happyPath(), { network: 'arc-testnet', explorerUrl: 'https://x', source: 'explorer' });
  assert.equal(h.activity[0].kind, 'job.created', 'the last log is the newest entry');
  assert.equal(h.activity.at(-1)!.kind, 'pool.deposit');
  for (const a of h.activity) {
    assert.match(a.tx, /^0x[0-9a-f]{64}$/);
    assert.doesNotMatch(a.text, /JobVault|FloatPool|bytes32|uint/, 'no contract jargon in the sentence');
  }
  const settled = h.activity.find((a) => a.kind === 'job.settled')!;
  assert.match(settled.text, /pool repaid 0\.51, operator received 0\.49/);
});

test('order does not matter: logs are applied by block and log index', () => {
  const logs = happyPath();
  const a = assembleHistory(logs, { network: 'n', explorerUrl: 'x', source: 'rpc' });
  const b = assembleHistory([...logs].reverse(), { network: 'n', explorerUrl: 'x', source: 'rpc' });
  assert.deepEqual(b.totals, a.totals);
  assert.deepEqual(b.jobs.map((j) => j.stage), a.jobs.map((j) => j.stage));
});

test('a refund and a write-off end the job as refunded with the advance written off', () => {
  block = 500;
  const logs = [
    log('JobCreated', { jobId: JOB, customer: CUSTOMER, operator: OPERATOR }, { types: ['uint256', 'uint256', 'bytes32', 'uint64'], values: [1_000_000n, 0n, JOB, 1n] }),
    log('JobFunded', { jobId: JOB }, { types: ['uint256'], values: [1_000_000n] }),
    log('AdvanceIssued', { jobId: JOB, org: OPERATOR }, { types: ['uint256', 'uint256', 'uint16'], values: [500_000n, 10_000n, 5000] }, POOL),
    log('AdvanceWrittenOff', { jobId: JOB }, { types: ['uint256', 'uint256', 'uint256'], values: [0n, 2_000n, 498_000n] }, POOL),
    log('JobRefunded', { jobId: JOB }, { types: ['uint256'], values: [1_000_000n] }),
  ];
  const h = assembleHistory(logs, { network: 'n', explorerUrl: 'x', source: 'rpc' });
  assert.equal(h.jobs[0].stage, 'Refunded');
  assert.equal(h.jobs[0].advance?.status, 'WrittenOff');
  assert.equal(h.totals.writtenOff, 1);
  assert.equal(h.totals.refunded, 1);
});

test('logs the decoder does not know (wiring, admin) are skipped, not misread', () => {
  const unknown: RawLog = { address: VAULT, topics: [`0x${'99'.repeat(32)}`], data: '0x', blockNumber: 1, logIndex: 0, txHash: `0x${'1'.repeat(64)}`, timestamp: 1 };
  const h = assembleHistory([unknown, ...happyPath()], { network: 'n', explorerUrl: 'x', source: 'rpc' });
  assert.equal(h.activity.length, happyPath().length);
});

test('mergeLogs drops duplicates by transaction and log index', () => {
  const logs = happyPath();
  assert.equal(mergeLogs(logs, logs.slice(3)).length, logs.length);
});

test('the explorer reader retries a rate limit and pages past 1,000 results', async () => {
  const logs = happyPath();
  const row = (l: RawLog) => ({ address: l.address, topics: l.topics, data: l.data, blockNumber: `0x${l.blockNumber.toString(16)}`, logIndex: '0x0', timeStamp: '0x1', transactionHash: l.txHash });
  let calls = 0;
  const fake = (async () => {
    calls += 1;
    if (calls === 1) return new Response('slow down', { status: 429 });
    return new Response(JSON.stringify({ status: '1', message: 'OK', result: logs.map(row) }), { status: 200 });
  }) as typeof fetch;
  const out = await fetchExplorerLogs('https://explorer/api', VAULT, 0, fake);
  assert.equal(calls, 2, 'one 429, then the page');
  assert.equal(out.length, logs.length);
});

test('the store bootstraps once from the explorer, then only tails the RPC', async () => {
  const logs = happyPath();
  const row = (l: RawLog) => ({ address: l.address, topics: l.topics, data: l.data, blockNumber: `0x${l.blockNumber.toString(16)}`, logIndex: '0x0', timeStamp: '0x1', transactionHash: l.txHash });
  let explorerCalls = 0;
  const fake = (async () => {
    explorerCalls += 1;
    return new Response(JSON.stringify({ status: '1', message: 'OK', result: explorerCalls === 1 ? logs.map(row) : [] }), { status: 200 });
  }) as typeof fetch;
  const ranges: string[] = [];
  let head = 1_000;
  const rpc = (async (method: string, params: unknown[]) => {
    if (method === 'eth_blockNumber') return `0x${head.toString(16)}`;
    if (method === 'eth_getLogs') {
      const p = params[0] as { fromBlock: string; toBlock: string };
      ranges.push(`${parseInt(p.fromBlock, 16)}-${parseInt(p.toBlock, 16)}`);
      return [];
    }
    return null;
  }) as never;
  const cacheDir = mkdtempSync(path.join(os.tmpdir(), 'snapfall-hist-'));
  const store = new HistoryStore({ network: 'arc-testnet', explorerUrl: 'x', explorerApi: 'https://e/api', addresses: [VAULT, POOL], startBlock: 0, rpc, cacheDir, fetchImpl: fake });

  const first = await store.refresh();
  assert.equal(first.totals.jobs, 2);
  assert.equal(explorerCalls, 2, 'one call per address');
  head = 1_050;
  await store.refresh();
  assert.equal(explorerCalls, 2, 'no second trip to the explorer');
  assert.deepEqual(ranges, ['801-1000', '1001-1050'], 'the explorer margin, then only new blocks');

  // A restarted server reads the bootstrap from the cache file instead of the explorer.
  const again = new HistoryStore({ network: 'arc-testnet', explorerUrl: 'x', explorerApi: 'https://e/api', addresses: [VAULT, POOL], startBlock: 0, rpc, cacheDir, fetchImpl: fake });
  await again.refresh();
  assert.equal(explorerCalls, 2);

  // A cache that is not provably ours is ignored and the store bootstraps again.
  const [name] = readdirSync(cacheDir).filter((f) => f.endsWith('.json'));
  const file = path.join(cacheDir, name);
  const good = readFileSync(file, 'utf8');
  const fresh = () => new HistoryStore({ network: 'arc-testnet', explorerUrl: 'x', explorerApi: 'https://e/api', addresses: [VAULT, POOL], startBlock: 0, rpc, cacheDir, fetchImpl: fake });
  const plant = (body: string, mode = 0o600) => { rmSync(file, { force: true }); writeFileSync(file, body, { mode }); chmodSync(file, mode); };
  let calls = explorerCalls;

  const foreign = JSON.parse(good);
  foreign.logs[0].address = '0x000000000000000000000000000000000000dead';
  plant(JSON.stringify(foreign));
  await fresh().refresh();
  assert.equal(explorerCalls, calls += 2, 'a log from another contract voids the cache');

  plant(good, 0o666);
  await fresh().refresh();
  assert.equal(explorerCalls, calls += 2, 'a cache others can write is not trusted');

  const ahead = JSON.parse(good);
  ahead.through = head + 1_000_000;
  plant(JSON.stringify(ahead));
  await fresh().refresh();
  assert.equal(explorerCalls, calls += 2, 'a cache past the chain head is dropped');

  assert.equal(statSync(file).mode & 0o777, 0o600, 'the rewritten cache is private');
});
