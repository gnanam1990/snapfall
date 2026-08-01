import type { FloatOpenAdvance, FloatLossTotals, FloatSnapshot, RateHistoryPoint } from './types';

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const HEX_RE = /^0x[0-9a-fA-F]+$/;

// FloatPool.advanceRate base: BASE_BPS = 5000 (FloatPool.sol:108-109, acceptedJobs=0).
// RateChanged only fires on a tick, so the starting 50% is never emitted — it is the
// origin the rate history is anchored to.
const BASE_RATE_BPS = 5000;

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
  // RateChanged(address indexed org, uint16 newRateBps) — FloatPool.sol:60, fires on the
  // accept path (263) and the write-off path (307). Its topic1 is the org, not a job id.
  rateChanged: '0xec739c9af710a6df2b3e3656f38b5d59af57d3022cd5a88ca4516db96a4ca5c7',
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
  /** Max blocks per eth_getLogs request — the provider's range cap. Defaults to
   *  DEFAULT_SCAN_CHUNK_BLOCKS (10000, Alchemy PAYG). Set from SNAPFALL_FLOAT_SCAN_CHUNK. */
  scanChunkBlocks?: number;
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
  // Rate / request-count limits: back off and retry. -32011 is the JSON-RPC request-limit
  // code some providers use. DISTINCT from a block-range cap (which is handled by shrinking
  // the scan window, not retrying) — see RPCError / isRangeError.
  return (
    status === 429 ||
    status === 502 ||
    status === 503 ||
    /rate.?limit|temporar|try again|request limit|-32011/i.test(message)
  );
}

/** Carries the RPC's own code/message so callers can classify the failure (a block-range
 *  cap vs a rate limit) instead of seeing an opaque "HTTP 400". */
export class RPCError extends Error {
  constructor(
    message: string,
    readonly httpStatus: number,
    readonly rpcCode?: number,
    readonly rpcMessage: string = '',
  ) {
    super(message);
    this.name = 'RPCError';
  }
}

// A block-range cap, not a rate limit: Alchemy returns HTTP 400 + code -32600 ("up to a
// 10000 block range"), the public Arc node returns HTTP 200 + code -32012 ("requested
// range too large"). The response body is what names it; the message match is a generic
// backstop, deliberately NOT a parse of any provider's suggested-range string.
export function isRangeError(error: unknown): boolean {
  if (!(error instanceof RPCError)) return false;
  if (error.rpcCode === -32600 || error.rpcCode === -32012) return true;
  return /range too large|block range|exceeds|too many blocks|range is too/i.test(error.rpcMessage);
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
        // READ the body — providers name the problem there (a range cap even suggests a
        // working range). Throwing on status alone discarded that and left callers with an
        // opaque "HTTP 400". The parsed code/message rides on RPCError for classification.
        const raw = await response.text().catch(() => ''); // read the body ONCE
        let rpcCode: number | undefined;
        let rpcMessage = '';
        try {
          const errBody = JSON.parse(raw) as { error?: { code?: number; message?: string } };
          rpcCode = errBody.error?.code;
          rpcMessage = errBody.error?.message ?? '';
        } catch {
          rpcMessage = raw.slice(0, 300);
        }
        lastError = `Arc RPC ${method} HTTP ${response.status}${rpcMessage ? `: ${rpcMessage}` : ''}`;
        if (attempt < 3 && isTransientRPCError(response.status, rpcMessage)) {
          await delay((options.retryDelayMs ?? 350) * 2 ** attempt);
          continue;
        }
        throw new RPCError(lastError, response.status, rpcCode, rpcMessage);
      }
      const body = (await response.json()) as {
        result?: T;
        error?: { code?: number; message?: string };
      };
      if (body.error) {
        const rpcMessage = body.error.message ?? String(body.error.code ?? 'unknown error');
        lastError = `Arc RPC ${method} failed: ${rpcMessage}`;
        if (attempt < 3 && isTransientRPCError(200, rpcMessage)) {
          await delay((options.retryDelayMs ?? 350) * 2 ** attempt);
          continue;
        }
        throw new RPCError(lastError, 200, body.error.code, rpcMessage);
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

// The default scan window. This is a per-request BLOCK-RANGE cap — the max span a single
// eth_getLogs may cover — NOT a rate limit. It is provider-dependent: Alchemy PAYG caps at
// 10000 blocks (HTTP 400 / -32600 above that), Alchemy's free tier at 10, and the public
// Arc node accepts any span but limits request COUNT. 10000 is the PAYG cap (the intended
// production RPC); override with SNAPFALL_FLOAT_SCAN_CHUNK. Do NOT shrink this back to a
// tiny value to appease rate limits — that is a different constraint (see isTransientRPCError,
// which backs off) and a small chunk turns the ~1.45M-block scan into tens of thousands of
// requests.
export const DEFAULT_SCAN_CHUNK_BLOCKS = 10_000;

function getLogsRange(rpc: RPCTransport, poolAddress: string, fromBlock: bigint, toBlock: bigint): Promise<RPCLog[]> {
  return rpc<RPCLog[]>('eth_getLogs', [
    {
      address: poolAddress,
      fromBlock: `0x${fromBlock.toString(16)}`,
      toBlock: `0x${toBlock.toString(16)}`,
      topics: [[TOPIC.issued, TOPIC.repaid, TOPIC.writtenOff, TOPIC.rateChanged]],
    },
  ]);
}

// Scan [fromBlock, toBlock] forward in fixed windows — deterministic and ~totalRange/chunk
// requests, versus the old halve-from-the-top approach that burned a large-range rejection
// before every success. If a window still trips a range cap (a stricter provider than the
// configured chunk assumes), the window is halved and the smaller size STICKS for the rest
// of the scan — so we discover the provider's real cap once, not on every chunk. Rate
// limits are handled inside the transport (back-off retry); only range errors shrink here.
async function scanLogs(
  rpc: RPCTransport,
  poolAddress: string,
  fromBlock: bigint,
  toBlock: bigint,
  chunkBlocks: number,
): Promise<RPCLog[]> {
  if (toBlock < fromBlock) return [];
  const out: RPCLog[] = [];
  let cursor = fromBlock;
  let window = BigInt(Math.max(1, Math.floor(chunkBlocks)));
  while (cursor <= toBlock) {
    const end = cursor + window - 1n < toBlock ? cursor + window - 1n : toBlock;
    try {
      out.push(...(await getLogsRange(rpc, poolAddress, cursor, end)));
      cursor = end + 1n;
    } catch (error) {
      if (isRangeError(error) && window > 1n) {
        window = window / 2n > 0n ? window / 2n : 1n; // sticky shrink to the provider's real cap
        continue;
      }
      throw error; // rate limits (already retried in transport) and everything else propagate
    }
  }
  return out;
}

function compareLogs(a: RPCLog, b: RPCLog): number {
  const block = parseHex(a.blockNumber, 'block number') - parseHex(b.blockNumber, 'block number');
  if (block !== 0n) return block < 0n ? -1 : 1;
  const index = parseHex(a.logIndex, 'log index') - parseHex(b.logIndex, 'log index');
  return index === 0n ? 0 : index < 0n ? -1 : 1;
}

// ── The immediate views ──────────────────────────────────────────────────────

export interface FloatViews {
  chainId: number;
  headBlock: bigint;
  blockNumber: number;
  poolAddress: string;
  explorerUrl: string;
  totalAssetsUsdc: string;
  totalOutstandingUsdc: string;
  availableLiquidityUsdc: string;
  utilizationBps: number;
  reserveUsdc: string;
  orgAddress: string | null;
  orgRateBps: number | null;
  acceptedJobs: number | null;
  writtenOffJobs: number | null;
}

/** The immediate FloatPool state — all eth_call, no log scan (~100ms). These resolve long
 *  before the historical scan and are what the page shows first. The org-scoped views
 *  (rate/accepted/write-offs) need the org up front, so they come from config.orgAddress;
 *  without it they are null until the scan resolves the observed org. */
export async function loadFloatViews(config: FloatChainConfig, rpc: RPCTransport): Promise<FloatViews> {
  const poolAddress = normalizedAddress(config.poolAddress, 'FloatPool address');
  const explicitOrg = config.orgAddress ? normalizedAddress(config.orgAddress, 'organization address') : null;
  if (!Number.isSafeInteger(config.chainId) || config.chainId <= 0) throw new Error('chain ID must be positive');
  if (!Number.isSafeInteger(config.startBlock) || config.startBlock < 0) throw new Error('start block must be non-negative');

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

  let orgRateBps: number | null = null;
  let acceptedJobs: number | null = null;
  let writtenOffJobs: number | null = null;
  if (explicitOrg) {
    const rate = await ethCall(rpc, poolAddress, encodeAddressCall(SELECTOR.advanceRate, explicitOrg), 'advanceRate');
    const accepted = await ethCall(rpc, poolAddress, encodeAddressCall(SELECTOR.acceptedJobs, explicitOrg), 'acceptedJobs');
    const writtenOff = await ethCall(rpc, poolAddress, encodeAddressCall(SELECTOR.writtenOffJobs, explicitOrg), 'writtenOffJobs');
    orgRateBps = toSafeNumber(rate, 'advance rate');
    acceptedJobs = toSafeNumber(accepted, 'accepted jobs');
    writtenOffJobs = toSafeNumber(writtenOff, 'written-off jobs');
  }

  const available = totalAssets - totalOutstanding;
  const utilizationBps = totalAssets === 0n ? 0 : toSafeNumber((totalOutstanding * 10_000n) / totalAssets, 'utilization');

  return {
    chainId,
    headBlock: head,
    blockNumber: toSafeNumber(head, 'head block'),
    poolAddress,
    explorerUrl: `${config.explorerUrl.replace(/\/$/, '')}/address/${poolAddress}`,
    totalAssetsUsdc: totalAssets.toString(),
    totalOutstandingUsdc: totalOutstanding.toString(),
    availableLiquidityUsdc: available.toString(),
    utilizationBps,
    reserveUsdc: reserve.toString(),
    orgAddress: explicitOrg,
    orgRateBps,
    acceptedJobs,
    writtenOffJobs,
  };
}

// ── Incremental history-scan cache ───────────────────────────────────────────
//
// The historical log scan spans ~345k blocks from the deployment. Re-running it on every
// request is wasteful, and it is safe to cache: Arc has instant finality — per
// deployments/README.md ("transactions are final on inclusion, no reorg risk after one
// confirmation. This is why confirmationDepth is 0 in arc-testnet.json"), a scanned block
// never changes. So we accumulate scan state and extend it only from the last scanned
// block forward. This is a documented Arc property, NOT an assumption of this code.
//
// State lives on globalThis, not a module-level `let`: Next.js re-evaluates route/lib
// modules on dev hot-reload, which would reset a module-scoped cache and force a fresh cold
// scan on every file save. globalThis survives HMR within the Node process.
//
// A per-key in-flight promise (SCAN_PENDING) serialises scans: the accumulator is shared
// mutable state, so two concurrent cold scans would double-merge. Concurrent callers await
// the same scan — no thundering herd on first load.

interface ScanState {
  scannedThroughBlock: bigint;
  feesAccrued: bigint;
  bondSlashed: bigint;
  reserveUsed: bigint;
  socialized: bigint;
  open: Map<string, FloatOpenAdvance>;
  rateTicks: { org: string; rateBps: number; blockNumber: number; txHash: string }[];
  latestObservedOrg?: string;
}

const scanGlobals = globalThis as unknown as {
  __snapfallFloatScan?: Map<string, ScanState>;
  __snapfallFloatScanPending?: Map<string, Promise<ScanState>>;
};
scanGlobals.__snapfallFloatScan ??= new Map();
scanGlobals.__snapfallFloatScanPending ??= new Map();
const SCAN_CACHE = scanGlobals.__snapfallFloatScan;
const SCAN_PENDING = scanGlobals.__snapfallFloatScanPending;

function scanKey(poolAddress: string, startBlock: number): string {
  return `${poolAddress}:${startBlock}`;
}

// Apply one block-ordered batch of logs to the accumulator. The advance-tracking branches
// (issued/repaid/writtenOff, including the open.delete on repay) are the original single-
// pass loop VERBATIM — only relocated so the accumulator persists across incremental scans;
// their behaviour is unchanged. RateChanged is handled FIRST, before the job-id check,
// because its topic1 is the org address, not a job id.
function mergeLogs(
  state: ScanState,
  logs: RPCLog[],
  config: FloatChainConfig,
  newOpenBlocks: Map<string, string>,
): void {
  for (const log of logs) {
    const eventTopic = log.topics[0]?.toLowerCase();
    if (eventTopic === TOPIC.rateChanged) {
      const org = topicAddress(log.topics[1] ?? '');
      const [rate] = words(log.data, 1);
      state.rateTicks.push({
        org,
        rateBps: toSafeNumber(rate!, 'rate-changed bps'),
        blockNumber: toSafeNumber(parseHex(log.blockNumber, 'block number'), 'block number'),
        txHash: log.transactionHash,
      });
      continue;
    }
    const jobId = log.topics[1]?.toLowerCase();
    if (!jobId || !/^0x[0-9a-f]{64}$/.test(jobId)) throw new Error('FloatPool log job ID is malformed');
    if (eventTopic === TOPIC.issued) {
      const org = topicAddress(log.topics[2] ?? '');
      const [principal, fee, rate] = words(log.data, 3);
      state.latestObservedOrg = org;
      state.open.set(jobId, {
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
      newOpenBlocks.set(jobId, log.blockNumber);
    } else if (eventTopic === TOPIC.repaid) {
      const [, fee] = words(log.data, 3);
      state.feesAccrued += fee!;
      state.open.delete(jobId);
    } else if (eventTopic === TOPIC.writtenOff) {
      const [bond, reserveDraw, lpLoss] = words(log.data, 3);
      state.bondSlashed += bond!;
      state.reserveUsed += reserveDraw!;
      state.socialized += lpLoss!;
      state.open.delete(jobId);
    }
  }
}

async function enrichOpenedAt(
  rpc: RPCTransport,
  state: ScanState,
  newOpenBlocks: Map<string, string>,
): Promise<void> {
  await Promise.all(
    [...newOpenBlocks.entries()].map(async ([jobId, blockHex]) => {
      const advance = state.open.get(jobId);
      if (!advance || advance.openedAt) return;
      const block = await rpc<RPCBlock | null>('eth_getBlockByNumber', [blockHex, false]);
      if (!block) return;
      const timestamp = parseHex(block.timestamp, 'block timestamp');
      advance.openedAt = new Date(toSafeNumber(timestamp, 'block timestamp') * 1000).toISOString();
    }),
  );
}

/** Incrementally scan FloatPool lifecycle + RateChanged logs up to `head`, caching the
 *  accumulator so subsequent calls only extend from the last scanned block. */
/**
 * Deep-enough copy of a cached scan for failure-atomic extension.
 *
 * The advance objects are copied too, not just the Map: enrichOpenedAt assigns openedAt on each
 * entry, so a shallow Map copy would still write through to the cached advances. rateTicks is
 * copied because mergeLogs pushes onto it.
 */
function cloneScanState(s: ScanState): ScanState {
  const open = new Map<string, FloatOpenAdvance>();
  for (const [k, v] of s.open) open.set(k, { ...v });
  return {
    scannedThroughBlock: s.scannedThroughBlock,
    feesAccrued: s.feesAccrued,
    bondSlashed: s.bondSlashed,
    reserveUsed: s.reserveUsed,
    socialized: s.socialized,
    open,
    rateTicks: [...s.rateTicks],
    latestObservedOrg: s.latestObservedOrg,
  };
}

export async function scanFloatHistory(config: FloatChainConfig, rpc: RPCTransport, head: bigint): Promise<ScanState> {
  const poolAddress = normalizedAddress(config.poolAddress, 'FloatPool address');
  const key = scanKey(poolAddress, config.startBlock);
  const inflight = SCAN_PENDING.get(key);
  if (inflight) return inflight;

  const run = (async (): Promise<ScanState> => {
    // Extend a COPY, never the cached object.
    //
    // The accumulators are advanced by mergeLogs before the cursor is advanced, and there is an
    // await between the two. Mutating the cached state in place therefore banks the additions
    // while leaving scannedThroughBlock stale whenever anything in that window throws, so the
    // next scan refetches the identical range and adds every fee, loss and rate tick a second
    // time. It is silent (both call sites in route.ts swallow the error), it never self-heals
    // (SCAN_CACHE has no eviction), and the payload still claims historyStatus 'complete'.
    //
    // Cloning makes the extension atomic: a throw leaves the cache exactly as it was, so the
    // range is simply rescanned. Cold scans were already safe, since their fresh object never
    // reached the cache.
    const cached = SCAN_CACHE.get(key);
    const state: ScanState = cached
      ? cloneScanState(cached)
      : {
          scannedThroughBlock: BigInt(config.startBlock) - 1n,
          feesAccrued: 0n,
          bondSlashed: 0n,
          reserveUsed: 0n,
          socialized: 0n,
          open: new Map(),
          rateTicks: [],
        };
    const fromBlock = state.scannedThroughBlock + 1n;
    if (head >= fromBlock) {
      const logs = (
        await scanLogs(rpc, poolAddress, fromBlock, head, config.scanChunkBlocks ?? DEFAULT_SCAN_CHUNK_BLOCKS)
      ).sort(compareLogs);
      const newOpenBlocks = new Map<string, string>();
      mergeLogs(state, logs, config, newOpenBlocks);
      await enrichOpenedAt(rpc, state, newOpenBlocks);
      state.scannedThroughBlock = head;
    }
    SCAN_CACHE.set(key, state);
    return state;
  })();

  SCAN_PENDING.set(key, run);
  try {
    return await run;
  } finally {
    SCAN_PENDING.delete(key);
  }
}

/** The org's rate curve: the base origin (5000, never emitted on chain) prepended to the
 *  org's RateChanged ticks in strict block order. A write-off LOWERS the rate, so this is
 *  not monotonic — never sort by value. */
function buildRateHistory(state: ScanState, config: FloatChainConfig, orgAddress: string | null): RateHistoryPoint[] {
  const base: RateHistoryPoint = { rateBps: BASE_RATE_BPS, blockNumber: config.startBlock, txHash: null };
  if (!orgAddress) return [base];
  const ticks = state.rateTicks
    .filter((t) => t.org === orgAddress)
    .map((t) => ({ rateBps: t.rateBps, blockNumber: t.blockNumber, txHash: t.txHash }));
  return [base, ...ticks];
}

export interface FloatHistory {
  feesAccruedUsdc: string;
  openAdvances: FloatOpenAdvance[];
  losses: FloatLossTotals;
  rateHistoryBps: RateHistoryPoint[];
  latestObservedOrg: string | null;
  /** The block this history was actually scanned through. Carried so a caller can tell whether
   *  the history it holds covers the head the view figures came from. */
  scannedThroughBlock: number;
}

function formatHistory(state: ScanState, config: FloatChainConfig, orgAddress: string | null): FloatHistory {
  return {
    feesAccruedUsdc: state.feesAccrued.toString(),
    openAdvances: [...state.open.values()],
    losses: {
      bondSlashedUsdc: state.bondSlashed.toString(),
      reserveUsedUsdc: state.reserveUsed.toString(),
      socializedUsdc: state.socialized.toString(),
    },
    rateHistoryBps: buildRateHistory(state, config, orgAddress ?? state.latestObservedOrg ?? null),
    latestObservedOrg: state.latestObservedOrg ?? null,
    scannedThroughBlock: toSafeNumber(state.scannedThroughBlock, 'scannedThroughBlock'),
  };
}

/** A synchronous peek at cached history, formatted for the payload; null if no scan has
 *  completed yet. Lets the route return complete history without awaiting a scan. */
export function peekFloatHistory(config: FloatChainConfig, orgAddress: string | null): FloatHistory | null {
  const poolAddress = normalizedAddress(config.poolAddress, 'FloatPool address');
  const state = SCAN_CACHE.get(scanKey(poolAddress, config.startBlock));
  return state ? formatHistory(state, config, orgAddress) : null;
}

/** Compose a wire snapshot from the immediate views and (optional) history. */
export function assembleSnapshot(
  views: FloatViews,
  history: FloatHistory | null,
  historyStatus: FloatSnapshot['historyStatus'],
): FloatSnapshot {
  return {
    chainId: views.chainId,
    blockNumber: views.blockNumber,
    poolAddress: views.poolAddress,
    explorerUrl: views.explorerUrl,
    totalAssetsUsdc: views.totalAssetsUsdc,
    totalOutstandingUsdc: views.totalOutstandingUsdc,
    availableLiquidityUsdc: views.availableLiquidityUsdc,
    utilizationBps: views.utilizationBps,
    feesAccruedUsdc: history?.feesAccruedUsdc ?? null,
    reserveUsdc: views.reserveUsdc,
    orgAddress: views.orgAddress ?? history?.latestObservedOrg ?? null,
    orgRateBps: views.orgRateBps,
    acceptedJobs: views.acceptedJobs,
    writtenOffJobs: views.writtenOffJobs,
    openAdvances: history?.openAdvances ?? null,
    losses: history?.losses ?? null,
    rateHistoryBps: history?.rateHistoryBps ?? null,
    historyScannedThroughBlock: history?.scannedThroughBlock ?? null,
    // A caller may ASK for 'complete', but it only holds if the history actually reaches the
    // block the views were read at. Downgrading here rather than trusting the caller means the
    // invariant lives in one place and cannot be forgotten at a call site.
    historyStatus:
      historyStatus === 'complete' && (history?.scannedThroughBlock ?? -1) < views.blockNumber
        ? 'pending'
        : historyStatus,
    observedAt: new Date().toISOString(),
  };
}

/** Reads the FloatPool views and, when requested, the full lifecycle history at one chain
 *  head. Kept as the single-call path for tests and non-progressive callers; the route
 *  composes loadFloatViews + scanFloatHistory directly for progressive delivery. */
export async function loadFloatSnapshot(
  config: FloatChainConfig,
  rpc: RPCTransport = createRPCTransport(config.rpcUrl),
  options: FloatLoadOptions = {},
): Promise<FloatSnapshot> {
  const includeHistory = options.includeHistory ?? true;
  const views = await loadFloatViews(config, rpc);
  if (!includeHistory) {
    return assembleSnapshot(views, null, 'unavailable');
  }
  const state = await scanFloatHistory(config, rpc, views.headBlock);
  return assembleSnapshot(views, formatHistory(state, config, views.orgAddress), 'complete');
}

export const floatChainInternals = { SELECTOR, TOPIC, word, BASE_RATE_BPS };
