/**
 * The dashboard's real-data spine: every JobVault and FloatPool event, turned into jobs, an
 * activity timeline and totals.
 *
 * Nothing here is invented. Each job, figure and timeline entry is decoded from an event log the
 * deployed contracts emitted, and every entry keeps its transaction hash so the page can link it
 * to the public explorer. Where the chain is silent (an agent's chat, an approval) this module is
 * silent too; that is the daemon's data, not the chain's.
 *
 * Logs come from one of two sources, chosen per network:
 *   - the explorer's Etherscan-compatible logs API (Blockscout `module=logs`), which answers a
 *     whole deployment's history in one request. Arc testnet's history spans millions of blocks,
 *     which the public RPC can only serve 10,000 at a time under a rate limit.
 *   - eth_getLogs over the RPC in bounded chunks, used when no explorer API is configured and the
 *     deployment's block span is small enough to scan (a fresh mainnet deployment).
 * Current state that must be exact (balances, open advances, the job struct) is still read from
 * the contracts by the existing routes; this module supplies history and discovery.
 */

import { decodeEventLog, parseAbi, type Hex } from 'viem';
import type { RPCTransport } from './floatChain';

// ── event ABI ───────────────────────────────────────────────────────────────

export const HISTORY_ABI = parseAbi([
  'event JobCreated(bytes32 indexed jobId, address indexed customer, address indexed operator, uint256 customerPayment, uint256 maxOperatingBudget, bytes32 termsHash, uint64 deadline)',
  'event JobFunded(bytes32 indexed jobId, uint256 amount)',
  'event WorkStarted(bytes32 indexed jobId)',
  'event ExpenseRecorded(bytes32 indexed jobId, uint256 amount, bytes32 receiptHash)',
  'event DeliverySubmitted(bytes32 indexed jobId, bytes32 deliveryHash)',
  'event JobSettled(bytes32 indexed jobId, uint256 advanceRepaid, uint256 operatorNet)',
  'event JobRefunded(bytes32 indexed jobId, uint256 customerAmount)',
  'event JobCancelled(bytes32 indexed jobId)',
  'event Deposited(address indexed lp, uint256 assets, uint256 shares)',
  'event Withdrawn(address indexed lp, uint256 assets, uint256 shares)',
  'event AdvanceIssued(bytes32 indexed jobId, address indexed org, uint256 principal, uint256 fee, uint16 rateBps)',
  'event AdvanceRepaid(bytes32 indexed jobId, uint256 principal, uint256 fee, uint256 toReserve)',
  'event AdvanceWrittenOff(bytes32 indexed jobId, uint256 bondSlashed, uint256 reserveUsed, uint256 socialized)',
  'event RateChanged(address indexed org, uint16 newRateBps)',
]);

// ── shapes ──────────────────────────────────────────────────────────────────

/** One raw log, normalised from either source. */
export interface RawLog {
  address: string;
  topics: string[];
  data: string;
  blockNumber: number;
  logIndex: number;
  txHash: string;
  /** Unix seconds; null when the source did not carry it and it was not looked up. */
  timestamp: number | null;
}

export type JobStage = 'Created' | 'Funded' | 'InProgress' | 'Delivered' | 'Settled' | 'Refunded' | 'Cancelled';

export interface ChainJob {
  jobId: string;
  customer: string;
  operator: string;
  /** Atomic USDC (6 dp) as decimal strings, so JSON carries them without precision loss. */
  paymentUsdc: string;
  budgetUsdc: string;
  expensesUsdc: string;
  expenseCount: number;
  deadline: number;
  stage: JobStage;
  createdAt: number | null;
  createdTx: string;
  lastAt: number | null;
  advance: {
    principalUsdc: string;
    feeUsdc: string;
    rateBps: number;
    status: 'Open' | 'Repaid' | 'WrittenOff';
    tx: string;
  } | null;
  settlement: { advanceRepaidUsdc: string; operatorNetUsdc: string; tx: string } | null;
  refundUsdc: string | null;
}

export type ActivityKind =
  | 'job.created' | 'job.funded' | 'job.started' | 'job.expense' | 'job.delivered' | 'job.settled'
  | 'job.refunded' | 'job.cancelled' | 'pool.deposit' | 'pool.withdraw' | 'advance.issued'
  | 'advance.repaid' | 'advance.writtenoff' | 'rate.changed';

export interface ChainActivity {
  id: string;
  kind: ActivityKind;
  /** Who acted, in plain words: Customer, Capital pool, Operator, Liquidity provider. */
  actor: string;
  /** One sentence, no contract jargon. */
  text: string;
  amountUsdc: string | null;
  jobId: string | null;
  at: number | null;
  block: number;
  tx: string;
  tone: 'neutral' | 'pos' | 'accent' | 'warn' | 'neg';
}

export interface ChainTotals {
  jobs: number;
  funded: number;
  active: number;
  settled: number;
  refunded: number;
  /** Sum of customer payments on settled jobs. */
  settledVolumeUsdc: string;
  /** Sum of every escrow currently funded and not yet settled, refunded or cancelled. */
  inEscrowUsdc: string;
  advancesIssued: number;
  advancedUsdc: string;
  advancesRepaid: number;
  feesEarnedUsdc: string;
  writtenOff: number;
  operatorEarnedUsdc: string;
  depositedUsdc: string;
  withdrawnUsdc: string;
  latestRateBps: number | null;
}

export interface ChainHistory {
  network: string;
  explorerUrl: string;
  source: 'explorer' | 'rpc';
  fetchedAt: string;
  jobs: ChainJob[];
  activity: ChainActivity[];
  totals: ChainTotals;
  rateHistory: { rateBps: number; at: number | null; tx: string }[];
}

// ── assembly (pure) ─────────────────────────────────────────────────────────

const short = (a: string) => `${a.slice(0, 6)}…${a.slice(-4)}`;
const usdc = (v: bigint) => {
  const whole = v / 1_000_000n;
  const frac = (v % 1_000_000n).toString().padStart(6, '0').slice(0, 2);
  return `${whole.toLocaleString('en-US')}.${frac}`;
};

type Decoded = { eventName: string; args: Record<string, unknown> };

function decode(log: RawLog): Decoded | null {
  try {
    const topics = log.topics.filter((t): t is string => typeof t === 'string' && t.length > 0) as [Hex, ...Hex[]];
    const out = decodeEventLog({ abi: HISTORY_ABI, data: log.data as Hex, topics, strict: true });
    return { eventName: out.eventName, args: out.args as Record<string, unknown> };
  } catch {
    return null; // admin and wiring events (Wired, CapsSet, PauseSet...) are not history
  }
}

/**
 * Builds jobs, activity and totals from logs of the two contracts. Order-independent: logs are
 * sorted by (block, logIndex) first, which is the order the chain applied them.
 */
export function assembleHistory(
  logs: RawLog[],
  meta: { network: string; explorerUrl: string; source: 'explorer' | 'rpc'; now?: Date },
): ChainHistory {
  const sorted = [...logs].sort((a, b) => a.blockNumber - b.blockNumber || a.logIndex - b.logIndex);
  const jobs = new Map<string, ChainJob & { _expenses: bigint }>();
  const activity: ChainActivity[] = [];
  const rateHistory: ChainHistory['rateHistory'] = [];
  let advancedUsdc = 0n;
  let feesEarned = 0n;
  let advancesIssued = 0;
  let advancesRepaid = 0;
  let writtenOff = 0;
  let deposited = 0n;
  let withdrawn = 0n;
  let operatorEarned = 0n;

  const touch = (id: string, at: number | null) => {
    const j = jobs.get(id);
    if (j && at !== null) j.lastAt = at;
    return j;
  };
  const push = (log: RawLog, a: Omit<ChainActivity, 'id' | 'at' | 'block' | 'tx'>) =>
    activity.push({ ...a, id: `${log.txHash}:${log.logIndex}`, at: log.timestamp, block: log.blockNumber, tx: log.txHash });

  for (const log of sorted) {
    const d = decode(log);
    if (!d) continue;
    const a = d.args;
    const jobId = typeof a.jobId === 'string' ? a.jobId.toLowerCase() : null;
    const at = log.timestamp;
    switch (d.eventName) {
      case 'JobCreated': {
        if (!jobId) break;
        const pay = a.customerPayment as bigint;
        jobs.set(jobId, {
          jobId,
          customer: String(a.customer),
          operator: String(a.operator),
          paymentUsdc: pay.toString(),
          budgetUsdc: (a.maxOperatingBudget as bigint).toString(),
          expensesUsdc: '0',
          expenseCount: 0,
          _expenses: 0n,
          deadline: Number(a.deadline as bigint),
          stage: 'Created',
          createdAt: at,
          createdTx: log.txHash,
          lastAt: at,
          advance: null,
          settlement: null,
          refundUsdc: null,
        });
        push(log, { kind: 'job.created', actor: 'Customer', text: `New job opened for ${usdc(pay)} USDC`, amountUsdc: pay.toString(), jobId, tone: 'neutral' });
        break;
      }
      case 'JobFunded': {
        if (!jobId) break;
        const j = touch(jobId, at);
        if (j) j.stage = 'Funded';
        push(log, { kind: 'job.funded', actor: 'Customer', text: `Payment locked in escrow`, amountUsdc: (a.amount as bigint).toString(), jobId, tone: 'pos' });
        break;
      }
      case 'WorkStarted': {
        if (!jobId) break;
        const j = touch(jobId, at);
        if (j) j.stage = 'InProgress';
        push(log, { kind: 'job.started', actor: 'Operator', text: 'Agents started the work', amountUsdc: null, jobId, tone: 'accent' });
        break;
      }
      case 'ExpenseRecorded': {
        if (!jobId) break;
        const amt = a.amount as bigint;
        const j = touch(jobId, at);
        if (j) {
          j._expenses += amt;
          j.expensesUsdc = j._expenses.toString();
          j.expenseCount += 1;
        }
        push(log, { kind: 'job.expense', actor: 'Agent', text: 'Agent paid for a resource to do the work', amountUsdc: amt.toString(), jobId, tone: 'neutral' });
        break;
      }
      case 'DeliverySubmitted': {
        if (!jobId) break;
        const j = touch(jobId, at);
        if (j) j.stage = 'Delivered';
        push(log, { kind: 'job.delivered', actor: 'Operator', text: 'Work delivered to the customer', amountUsdc: null, jobId, tone: 'accent' });
        break;
      }
      case 'JobSettled': {
        if (!jobId) break;
        const repaid = a.advanceRepaid as bigint;
        const net = a.operatorNet as bigint;
        operatorEarned += net;
        const j = touch(jobId, at);
        if (j) {
          j.stage = 'Settled';
          j.settlement = { advanceRepaidUsdc: repaid.toString(), operatorNetUsdc: net.toString(), tx: log.txHash };
        }
        push(log, {
          kind: 'job.settled',
          actor: 'Customer',
          text: repaid > 0n
            ? `Accepted and settled: pool repaid ${usdc(repaid)}, operator received ${usdc(net)}`
            : `Accepted and settled: operator received ${usdc(net)}`,
          amountUsdc: (repaid + net).toString(),
          jobId,
          tone: 'pos',
        });
        break;
      }
      case 'JobRefunded': {
        if (!jobId) break;
        const amt = a.customerAmount as bigint;
        const j = touch(jobId, at);
        if (j) {
          j.stage = 'Refunded';
          j.refundUsdc = amt.toString();
        }
        push(log, { kind: 'job.refunded', actor: 'Customer', text: 'Job refunded to the customer', amountUsdc: amt.toString(), jobId, tone: 'warn' });
        break;
      }
      case 'JobCancelled': {
        if (!jobId) break;
        const j = touch(jobId, at);
        if (j) j.stage = 'Cancelled';
        push(log, { kind: 'job.cancelled', actor: 'Customer', text: 'Job cancelled before funding', amountUsdc: null, jobId, tone: 'neutral' });
        break;
      }
      case 'Deposited': {
        const amt = a.assets as bigint;
        deposited += amt;
        push(log, { kind: 'pool.deposit', actor: 'Liquidity provider', text: `Added capital to the pool (${short(String(a.lp))})`, amountUsdc: amt.toString(), jobId: null, tone: 'pos' });
        break;
      }
      case 'Withdrawn': {
        const amt = a.assets as bigint;
        withdrawn += amt;
        push(log, { kind: 'pool.withdraw', actor: 'Liquidity provider', text: `Took capital out of the pool (${short(String(a.lp))})`, amountUsdc: amt.toString(), jobId: null, tone: 'neutral' });
        break;
      }
      case 'AdvanceIssued': {
        if (!jobId) break;
        const principal = a.principal as bigint;
        const fee = a.fee as bigint;
        const rate = Number(a.rateBps);
        advancesIssued += 1;
        advancedUsdc += principal;
        const j = touch(jobId, at);
        if (j) j.advance = { principalUsdc: principal.toString(), feeUsdc: fee.toString(), rateBps: rate, status: 'Open', tx: log.txHash };
        push(log, {
          kind: 'advance.issued',
          actor: 'Capital pool',
          text: `Advanced working capital at ${(rate / 100).toFixed(0)}% of the job`,
          amountUsdc: principal.toString(),
          jobId,
          tone: 'accent',
        });
        break;
      }
      case 'AdvanceRepaid': {
        if (!jobId) break;
        const principal = a.principal as bigint;
        const fee = a.fee as bigint;
        advancesRepaid += 1;
        feesEarned += fee;
        const j = touch(jobId, at);
        if (j?.advance) j.advance.status = 'Repaid';
        push(log, { kind: 'advance.repaid', actor: 'Capital pool', text: `Advance repaid first, plus a ${usdc(fee)} USDC fee`, amountUsdc: (principal + fee).toString(), jobId, tone: 'pos' });
        break;
      }
      case 'AdvanceWrittenOff': {
        if (!jobId) break;
        writtenOff += 1;
        const j = touch(jobId, at);
        if (j?.advance) j.advance.status = 'WrittenOff';
        const loss = (a.bondSlashed as bigint) + (a.reserveUsed as bigint) + (a.socialized as bigint);
        push(log, { kind: 'advance.writtenoff', actor: 'Capital pool', text: 'Advance written off after a refund', amountUsdc: loss.toString(), jobId, tone: 'neg' });
        break;
      }
      case 'RateChanged': {
        const rate = Number(a.newRateBps);
        rateHistory.push({ rateBps: rate, at, tx: log.txHash });
        push(log, { kind: 'rate.changed', actor: 'Capital pool', text: `Advance rate moved to ${(rate / 100).toFixed(0)}% after a delivered job`, amountUsdc: null, jobId: null, tone: 'accent' });
        break;
      }
    }
  }

  const list = [...jobs.values()].map(({ _expenses: _e, ...j }) => j);
  const isOpen = (s: JobStage) => s === 'Funded' || s === 'InProgress' || s === 'Delivered';
  const sum = (xs: string[]) => xs.reduce((t, x) => t + BigInt(x), 0n);

  return {
    network: meta.network,
    explorerUrl: meta.explorerUrl,
    source: meta.source,
    fetchedAt: (meta.now ?? new Date()).toISOString(),
    // Newest first: the page leads with what just happened.
    jobs: list.sort((a, b) => (b.lastAt ?? 0) - (a.lastAt ?? 0)),
    activity: activity.reverse(),
    rateHistory,
    totals: {
      jobs: list.length,
      funded: list.filter((j) => j.stage !== 'Created' && j.stage !== 'Cancelled').length,
      active: list.filter((j) => isOpen(j.stage)).length,
      settled: list.filter((j) => j.stage === 'Settled').length,
      refunded: list.filter((j) => j.stage === 'Refunded').length,
      settledVolumeUsdc: sum(list.filter((j) => j.stage === 'Settled').map((j) => j.paymentUsdc)).toString(),
      inEscrowUsdc: sum(list.filter((j) => isOpen(j.stage)).map((j) => j.paymentUsdc)).toString(),
      advancesIssued,
      advancedUsdc: advancedUsdc.toString(),
      advancesRepaid,
      feesEarnedUsdc: feesEarned.toString(),
      writtenOff,
      operatorEarnedUsdc: operatorEarned.toString(),
      depositedUsdc: deposited.toString(),
      withdrawnUsdc: withdrawn.toString(),
      latestRateBps: rateHistory.length ? rateHistory[rateHistory.length - 1].rateBps : null,
    },
  };
}

// ── sources ─────────────────────────────────────────────────────────────────

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener('abort', () => {
      clearTimeout(t);
      reject(signal.reason ?? new Error('aborted'));
    }, { once: true });
  });

const hexNum = (v: unknown): number => (typeof v === 'string' ? Number.parseInt(v, v.startsWith('0x') ? 16 : 10) : Number(v));

interface ExplorerLog {
  address: string;
  topics: (string | null)[];
  data: string;
  blockNumber: string;
  logIndex: string;
  timeStamp?: string;
  transactionHash: string;
}

/**
 * Every log of one address from a Blockscout/Etherscan-compatible API. The API caps a response at
 * 1,000 logs, so a full page continues from its last block (re-reading that block, then dropping
 * what was already seen) until a short page ends it.
 */
export async function fetchExplorerLogs(
  apiBase: string,
  address: string,
  fromBlock: number,
  fetchImpl: typeof fetch = fetch,
  signal?: AbortSignal,
): Promise<RawLog[]> {
  const out: RawLog[] = [];
  const seen = new Set<string>();
  let from = fromBlock;
  for (let page = 0; page < 50; page += 1) {
    const url = `${apiBase}?module=logs&action=getLogs&fromBlock=${from}&toBlock=latest&address=${address}`;
    let res: Response | null = null;
    // The public explorer rate-limits bursts (HTTP 429): back off and retry a few times before
    // reporting the read as failed.
    for (let attempt = 0; attempt < 4; attempt += 1) {
      res = await fetchImpl(url, { cache: 'no-store', signal, headers: { accept: 'application/json' } });
      if (res.status !== 429 && res.status < 500) break;
      if (attempt < 3) await sleep(800 * 2 ** attempt, signal);
    }
    if (!res || !res.ok) throw new Error(`explorer logs API answered HTTP ${res?.status ?? 'no response'}`);
    const body = (await res.json()) as { status?: string; message?: string; result?: ExplorerLog[] | string };
    const rows = Array.isArray(body.result) ? body.result : [];
    if (!Array.isArray(body.result) && body.message && !/no (records|logs) found/i.test(body.message)) {
      throw new Error(`explorer logs API: ${body.message}`);
    }
    for (const r of rows) {
      const key = `${r.transactionHash}:${r.logIndex}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({
        address: r.address,
        topics: r.topics.filter((t): t is string => typeof t === 'string'),
        data: r.data,
        blockNumber: hexNum(r.blockNumber),
        logIndex: hexNum(r.logIndex),
        txHash: r.transactionHash,
        timestamp: r.timeStamp ? hexNum(r.timeStamp) : null,
      });
    }
    if (rows.length < 1000) break;
    from = hexNum(rows[rows.length - 1].blockNumber);
  }
  return out;
}

interface RpcLog {
  address: string;
  topics: string[];
  data: string;
  blockNumber: string;
  logIndex: string;
  transactionHash: string;
  blockTimestamp?: string;
}

/**
 * Every log of the given addresses over [fromBlock, head] by eth_getLogs, in chunks. Refuses a
 * span it could not finish in reasonable time on a rate-limited endpoint rather than returning a
 * partial history that would look complete.
 */
export async function rpcHead(rpc: RPCTransport): Promise<number> {
  return hexNum(await rpc<string>('eth_blockNumber', []));
}

export async function fetchRpcLogs(
  rpc: RPCTransport,
  addresses: string[],
  fromBlock: number,
  options: { chunk?: number; maxSpan?: number; toBlock?: number } = {},
): Promise<{ logs: RawLog[]; head: number }> {
  const chunk = options.chunk ?? 10_000;
  const head = options.toBlock ?? (await rpcHead(rpc));
  const span = head - fromBlock;
  const maxSpan = options.maxSpan ?? 2_000_000;
  if (span > maxSpan) {
    throw new Error(
      `history spans ${span.toLocaleString('en-US')} blocks, too many to scan over this RPC; set SNAPFALL_EXPLORER_API to the network's explorer API`,
    );
  }
  const out: RawLog[] = [];
  for (let from = fromBlock; from <= head; from += chunk) {
    const to = Math.min(head, from + chunk - 1);
    const rows = await rpc<RpcLog[]>('eth_getLogs', [
      { address: addresses, fromBlock: `0x${from.toString(16)}`, toBlock: `0x${to.toString(16)}` },
    ]);
    for (const r of rows) {
      out.push({
        address: r.address,
        topics: r.topics,
        data: r.data,
        blockNumber: hexNum(r.blockNumber),
        logIndex: hexNum(r.logIndex),
        txHash: r.transactionHash,
        timestamp: r.blockTimestamp ? hexNum(r.blockTimestamp) : null,
      });
    }
  }
  // Fill timestamps the node did not attach, one header per distinct block.
  const missing = [...new Set(out.filter((l) => l.timestamp === null).map((l) => l.blockNumber))];
  const times = new Map<number, number>();
  for (const b of missing.slice(0, 400)) {
    const block = await rpc<{ timestamp: string } | null>('eth_getBlockByNumber', [`0x${b.toString(16)}`, false]);
    if (block) times.set(b, hexNum(block.timestamp));
  }
  for (const l of out) if (l.timestamp === null) l.timestamp = times.get(l.blockNumber) ?? null;
  return { logs: out, head };
}

/** Merges two log lists, dropping duplicates by (transaction, log index). */
export function mergeLogs(a: RawLog[], b: RawLog[]): RawLog[] {
  const seen = new Set<string>();
  const out: RawLog[] = [];
  for (const l of [...a, ...b]) {
    const k = `${l.txHash.toLowerCase()}:${l.logIndex}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(l);
  }
  return out;
}

/** Explorer API base for a network, unless SNAPFALL_EXPLORER_API overrides it. */
export function explorerApiFor(network: string, env: Record<string, string | undefined> = process.env): string | null {
  const override = env.SNAPFALL_EXPLORER_API?.trim();
  if (override) return override.replace(/\/$/, '');
  // Arc testnet's Blockscout answers the logs API publicly. The mainnet explorer sits behind a
  // browser challenge, so mainnet scans the RPC (its deployment is recent and small).
  if (network === 'arc-testnet') return 'https://explorer.testnet.arc.io/api';
  return null;
}
