import type { FloatOpenAdvance, FloatSnapshot } from './types';

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const HEX_RE = /^0x[0-9a-fA-F]+$/;

const SELECTOR = {
  totalAssets: '0x01e1d114',
  totalOutstanding: '0x16078d04',
  reserve: '0xcd3293de',
  advanceRate: '0x95790ee5',
  acceptedJobs: '0x9aa60919',
  writtenOffJobs: '0xd30856e5',
} as const;

const TOPIC = {
  issued: '0x4e000615bb000c437ff360e4f54ea1722dc46e202857ff124e0668f955301da7',
  repaid: '0xb1a154c78bda0dfbf33f2c572b5d8ce519a400aa92b38315e90daa26e44f1b4c',
  writtenOff: '0x6a6428d29409279a788a4399a8204370bd90228631e24511ba87073e9f65a48f',
} as const;

interface RPCLog {
  address: string;
  blockNumber: string;
  data: string;
  logIndex: string;
  topics: string[];
  transactionHash: string;
}

interface RPCBlock {
  timestamp: string;
}

export interface FloatChainConfig {
  chainId: number;
  rpcUrl: string;
  poolAddress: string;
  explorerUrl: string;
  startBlock: number;
  orgAddress?: string;
}

export type RPCTransport = <T>(method: string, params: unknown[]) => Promise<T>;

export interface FloatLoadOptions {
  includeHistory?: boolean;
}

interface RPCTransportOptions {
  retryDelayMs?: number;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function isTransientRPCError(status: number, message = ''): boolean {
  return status === 429 || status === 502 || status === 503 || /rate.?limit|temporar|try again/i.test(message);
}

export function createRPCTransport(url: string, options: RPCTransportOptions = {}): RPCTransport {
  let id = 0;
  return async <T>(method: string, params: unknown[]): Promise<T> => {
    const requestId = ++id;
    let lastError = `Arc RPC ${method} failed`;
    for (let attempt = 0; attempt < 4; attempt += 1) {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: requestId, method, params }),
        cache: 'no-store',
      });
      if (!response.ok) {
        lastError = `Arc RPC ${method} returned HTTP ${response.status}`;
        if (attempt < 3 && isTransientRPCError(response.status)) {
          await delay((options.retryDelayMs ?? 350) * 2 ** attempt);
          continue;
        }
        throw new Error(lastError);
      }
      const body = (await response.json()) as {
        result?: T;
        error?: { code?: number; message?: string };
      };
      if (body.error) {
        lastError = `Arc RPC ${method} failed: ${body.error.message ?? body.error.code ?? 'unknown error'}`;
        if (attempt < 3 && isTransientRPCError(200, body.error.message)) {
          await delay((options.retryDelayMs ?? 350) * 2 ** attempt);
          continue;
        }
        throw new Error(lastError);
      }
      if (body.result === undefined) {
        throw new Error(`Arc RPC ${method} returned no result`);
      }
      return body.result;
    }
    throw new Error(lastError);
  };
}

function parseHex(value: string, label: string): bigint {
  if (!HEX_RE.test(value)) throw new Error(`${label} is not an unsigned hex quantity`);
  return BigInt(value);
}

function toSafeNumber(value: bigint, label: string): number {
  if (value > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error(`${label} exceeds JavaScript's safe integer range`);
  return Number(value);
}

function word(value: bigint): string {
  return value.toString(16).padStart(64, '0');
}

function words(data: string, minimum: number): bigint[] {
  if (!/^0x(?:[0-9a-fA-F]{64})+$/.test(data)) throw new Error('FloatPool log data is malformed');
  const raw = data.slice(2);
  const output: bigint[] = [];
  for (let offset = 0; offset < raw.length; offset += 64) {
    output.push(BigInt(`0x${raw.slice(offset, offset + 64)}`));
  }
  if (output.length < minimum) throw new Error('FloatPool log data is incomplete');
  return output;
}

function encodeAddressCall(selector: string, address: string): string {
  return `${selector}${address.slice(2).toLowerCase().padStart(64, '0')}`;
}

function topicAddress(value: string): string {
  if (!/^0x[0-9a-fA-F]{64}$/.test(value)) throw new Error('indexed organization address is malformed');
  return `0x${value.slice(-40).toLowerCase()}`;
}

function normalizedAddress(value: string, label: string): string {
  if (!ADDRESS_RE.test(value)) throw new Error(`${label} is not an EVM address`);
  return value.toLowerCase();
}

async function ethCall(rpc: RPCTransport, poolAddress: string, data: string, label: string): Promise<bigint> {
  const result = await rpc<string>('eth_call', [{ to: poolAddress, data }, 'latest']);
  return parseHex(result, label);
}

async function getLogsAdaptive(
  rpc: RPCTransport,
  poolAddress: string,
  fromBlock: bigint,
  toBlock: bigint,
  depth = 0,
): Promise<RPCLog[]> {
  if (toBlock < fromBlock) return [];
  try {
    return await rpc<RPCLog[]>('eth_getLogs', [
      {
        address: poolAddress,
        fromBlock: `0x${fromBlock.toString(16)}`,
        toBlock: `0x${toBlock.toString(16)}`,
        topics: [[TOPIC.issued, TOPIC.repaid, TOPIC.writtenOff]],
      },
    ]);
  } catch (error) {
    if (fromBlock === toBlock || depth >= 16) throw error;
    const midpoint = (fromBlock + toBlock) / 2n;
    // Keep split queries sequential: a constrained RPC has already rejected the
    // larger range, and parallel recursion would amplify its rate-limit pressure.
    const left = await getLogsAdaptive(rpc, poolAddress, fromBlock, midpoint, depth + 1);
    const right = await getLogsAdaptive(rpc, poolAddress, midpoint + 1n, toBlock, depth + 1);
    return [...left, ...right];
  }
}

function compareLogs(a: RPCLog, b: RPCLog): number {
  const block = parseHex(a.blockNumber, 'block number') - parseHex(b.blockNumber, 'block number');
  if (block !== 0n) return block < 0n ? -1 : 1;
  const index = parseHex(a.logIndex, 'log index') - parseHex(b.logIndex, 'log index');
  return index === 0n ? 0 : index < 0n ? -1 : 1;
}

/** Reads the FloatPool views and lifecycle logs at one chain head. */
export async function loadFloatSnapshot(
  config: FloatChainConfig,
  rpc: RPCTransport = createRPCTransport(config.rpcUrl),
  options: FloatLoadOptions = {},
): Promise<FloatSnapshot> {
  const includeHistory = options.includeHistory ?? true;
  const poolAddress = normalizedAddress(config.poolAddress, 'FloatPool address');
  const explicitOrg = config.orgAddress ? normalizedAddress(config.orgAddress, 'organization address') : undefined;
  if (!Number.isSafeInteger(config.chainId) || config.chainId <= 0) throw new Error('chain ID must be positive');
  if (!Number.isSafeInteger(config.startBlock) || config.startBlock < 0) throw new Error('start block must be non-negative');

  // Keep public-RPC pressure deliberately low. Arc's shared testnet endpoint rate-limits
  // short concurrent bursts even when each individual request is a cheap current-state read.
  const chainHex = await rpc<string>('eth_chainId', []);
  const headHex = await rpc<string>('eth_blockNumber', []);
  const totalAssets = await ethCall(rpc, poolAddress, SELECTOR.totalAssets, 'totalAssets');
  const totalOutstanding = await ethCall(rpc, poolAddress, SELECTOR.totalOutstanding, 'totalOutstanding');
  const reserve = await ethCall(rpc, poolAddress, SELECTOR.reserve, 'reserve');
  const chainId = toSafeNumber(parseHex(chainHex, 'chain ID'), 'chain ID');
  if (chainId !== config.chainId) {
    throw new Error(`Arc RPC chain ID ${chainId} does not match configured chain ID ${config.chainId}`);
  }
  if (totalOutstanding > totalAssets) throw new Error('FloatPool outstanding principal exceeds total assets');

  const head = parseHex(headHex, 'head block');
  const logs = includeHistory
    ? (await getLogsAdaptive(rpc, poolAddress, BigInt(config.startBlock), head)).sort(compareLogs)
    : [];

  const open = new Map<string, FloatOpenAdvance>();
  let feesAccrued = 0n;
  let bondSlashed = 0n;
  let reserveUsed = 0n;
  let socialized = 0n;
  let latestObservedOrg: string | undefined;

  for (const log of logs) {
    const eventTopic = log.topics[0]?.toLowerCase();
    const jobId = log.topics[1]?.toLowerCase();
    if (!jobId || !/^0x[0-9a-f]{64}$/.test(jobId)) throw new Error('FloatPool log job ID is malformed');
    if (eventTopic === TOPIC.issued) {
      const org = topicAddress(log.topics[2] ?? '');
      const [principal, fee, rate] = words(log.data, 3);
      latestObservedOrg = org;
      open.set(jobId, {
        jobId,
        org,
        principalUsdc: principal!.toString(),
        feeUsdc: fee!.toString(),
        rateBps: toSafeNumber(rate!, 'advance rate'),
        status: 'Issued',
        openedAt: null,
        txHash: log.transactionHash,
        explorerUrl: `${config.explorerUrl.replace(/\/$/, '')}/tx/${log.transactionHash}`,
      });
    } else if (eventTopic === TOPIC.repaid) {
      const [, fee] = words(log.data, 3);
      feesAccrued += fee!;
      open.delete(jobId);
    } else if (eventTopic === TOPIC.writtenOff) {
      const [bond, reserveDraw, lpLoss] = words(log.data, 3);
      bondSlashed += bond!;
      reserveUsed += reserveDraw!;
      socialized += lpLoss!;
      open.delete(jobId);
    }
  }

  const blockByJob = new Map<string, string>();
  for (const log of logs) {
    const jobId = log.topics[1]?.toLowerCase();
    if (jobId && open.has(jobId) && log.topics[0]?.toLowerCase() === TOPIC.issued) {
      blockByJob.set(jobId, log.blockNumber);
    }
  }
  await Promise.all(
    [...open.entries()].map(async ([jobId, advance]) => {
      const blockHex = blockByJob.get(jobId);
      if (!blockHex) return;
      const block = await rpc<RPCBlock | null>('eth_getBlockByNumber', [blockHex, false]);
      if (!block) return;
      const timestamp = parseHex(block.timestamp, 'block timestamp');
      advance.openedAt = new Date(toSafeNumber(timestamp, 'block timestamp') * 1000).toISOString();
    }),
  );

  const orgAddress = explicitOrg ?? latestObservedOrg ?? null;
  let orgRateBps: number | null = null;
  let acceptedJobs: number | null = null;
  let writtenOffJobs: number | null = null;
  if (orgAddress) {
    const rate = await ethCall(rpc, poolAddress, encodeAddressCall(SELECTOR.advanceRate, orgAddress), 'advanceRate');
    const accepted = await ethCall(rpc, poolAddress, encodeAddressCall(SELECTOR.acceptedJobs, orgAddress), 'acceptedJobs');
    const writtenOff = await ethCall(
      rpc,
      poolAddress,
      encodeAddressCall(SELECTOR.writtenOffJobs, orgAddress),
      'writtenOffJobs',
    );
    orgRateBps = toSafeNumber(rate, 'advance rate');
    acceptedJobs = toSafeNumber(accepted, 'accepted jobs');
    writtenOffJobs = toSafeNumber(writtenOff, 'written-off jobs');
  }

  const available = totalAssets - totalOutstanding;
  const utilizationBps = totalAssets === 0n ? 0 : toSafeNumber((totalOutstanding * 10_000n) / totalAssets, 'utilization');

  return {
    chainId,
    blockNumber: toSafeNumber(head, 'head block'),
    poolAddress,
    explorerUrl: `${config.explorerUrl.replace(/\/$/, '')}/address/${poolAddress}`,
    totalAssetsUsdc: totalAssets.toString(),
    totalOutstandingUsdc: totalOutstanding.toString(),
    availableLiquidityUsdc: available.toString(),
    utilizationBps,
    feesAccruedUsdc: includeHistory ? feesAccrued.toString() : null,
    reserveUsdc: reserve.toString(),
    orgAddress,
    orgRateBps,
    acceptedJobs,
    writtenOffJobs,
    openAdvances: includeHistory ? [...open.values()] : null,
    losses: includeHistory
      ? {
          bondSlashedUsdc: bondSlashed.toString(),
          reserveUsedUsdc: reserveUsed.toString(),
          socializedUsdc: socialized.toString(),
        }
      : null,
    historyStatus: includeHistory ? 'complete' : 'unavailable',
    observedAt: new Date().toISOString(),
  };
}

export const floatChainInternals = { SELECTOR, TOPIC, word };
