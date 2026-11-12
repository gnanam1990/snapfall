/**
 * The on-chain capital controls, read straight from FloatPool and JobVault.
 *
 * docs/MAINNET.md tells an operator to watch for CapsRaiseQueued and AdminTransferStarted, and
 * to know how much of the lifetime loss budget is spent. This is that view: the caps, the
 * budget (maxTotalExposure bounds totalOutstanding + totalWrittenOff), whether a cap raise or
 * an admin handoff is pending, and when each becomes executable. It is read from contract
 * state at one block, not from an indexer, so it cannot lag or be replayed wrong.
 *
 * The same goes for the operator allowlist (who may draw advances): a queued operator grant only
 * protects the LPs if someone sees it during OPERATOR_GRANT_DELAY, so every allowed operator and
 * every queued grant is shown with its deadline. The candidate addresses come from FloatPool's
 * operator events (a mapping cannot be listed); their state is read by view call at the same block.
 *
 * The contracts live on Arc testnet predate these controls (ADR-014 froze them before
 * capital controls existed), so their getters revert. That is reported as `unsupported`,
 * never as zeros: a zero cap would read as "nothing may be lent", which is a claim.
 */

import { DEFAULT_SCAN_CHUNK_BLOCKS, RPCError, isRangeError, type RPCTransport } from './floatChain';

/** Verified against `forge inspect ... methodIdentifiers` for FloatPool and JobVault. */
const SELECTOR = {
  maxTotalExposure: '0x14a10511',
  maxAdvance: '0xc668e935',
  totalOutstanding: '0x16078d04',
  totalWrittenOff: '0x4375511f',
  paused: '0x5c975abb',
  pendingMaxTotalExposure: '0x1c6b1c74',
  pendingMaxAdvance: '0x1949ee23',
  capRaiseExecutableAt: '0x45822f2b',
  admin: '0xf851a440',
  pendingAdmin: '0x26782247',
  adminTransferAcceptableAt: '0xaedd03a2',
  maxJobPayment: '0x61015acf',
} as const;

/**
 * The operator allowlist (FloatPool, added Nov 2026): who may draw advances. Verified against
 * `forge inspect FloatPool methodIdentifiers` and `cast keccak` of the event signatures.
 * Defined here rather than read from contracts/abi so an older ABI file cannot drift the read.
 */
const OPERATOR_SELECTOR = {
  OPERATOR_GRANT_DELAY: '0x536996f1',
  allowedOperator: '0xfc1be7d5', // allowedOperator(address)
  operatorGrantExecutableAt: '0xf1f4e038', // operatorGrantExecutableAt(address)
} as const;

const OPERATOR_TOPIC = {
  allowed: '0x5d9603027afffb9569796fb6d9fc189bbf68724bd0430924681595302758b4f0', // OperatorAllowed(address indexed,bool)
  queued: '0xff078ce151bed0a3c40fbcd1d5cdccda59b388dc9b7dcd23cdf1abc1e2572fb8', // OperatorGrantQueued(address indexed,uint64)
  cancelled: '0x9dd6995542eb1cfc773e44bfae3d1c28065d209fd2ffe2055268a99c64293da6', // OperatorGrantCancelled(address indexed)
} as const;

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const WORD_RE = /^0x[0-9a-fA-F]{64}$/;
const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';

export interface OperatorDiscoveryOptions {
  /** First block to scan for operator events: the deployment block. */
  startBlock: number;
  /** Max blocks per eth_getLogs request (the provider's range cap). */
  chunkBlocks?: number;
  /** Wall-clock budget for this call's share of the scan. Progress is cached, so the next call
   *  continues where this one stopped. */
  budgetMs?: number;
  /** Clock, for tests. */
  now?: () => number;
}

export interface GovernanceConfig {
  chainId: number;
  floatPoolAddress: string;
  jobVaultAddress: string;
  /** The operator this deployment expects to borrow (SNAPFALL_TREASURY_ADDRESS). Read by view
   *  call on every load, whatever the log scan manages. */
  operatorAddress?: string;
  /** Find every other operator the admin ever granted or queued, from FloatPool's logs. Omitted,
   *  only the configured operator is checked, and the snapshot says so. */
  operatorDiscovery?: OperatorDiscoveryOptions;
}

export interface PendingAdmin {
  address: string;
  /** ISO time from which acceptAdmin() succeeds. */
  acceptableAt: string;
  acceptable: boolean;
}

export interface GovernanceControls {
  status: 'ok';
  chainId: number;
  blockNumber: number;
  /** Block timestamp the pending deadlines are judged against, ISO. */
  observedAt: string;
  pool: {
    paused: boolean;
    admin: string;
    maxTotalExposure: string; // USDC base units (6dp)
    maxAdvance: string;
    totalOutstanding: string;
    totalWrittenOff: string;
    pendingAdmin: PendingAdmin | null;
  };
  vault: {
    paused: boolean;
    admin: string;
    maxJobPayment: string;
    pendingAdmin: PendingAdmin | null;
  };
  /** maxTotalExposure split into what is lent now, what is lost for good, and what is left. */
  lossBudget: { ceiling: string; outstanding: string; writtenOff: string; remaining: string };
  pendingCapRaise: {
    maxTotalExposure: string;
    maxAdvance: string;
    executableAt: string;
    executable: boolean;
  } | null;
  /** Who may borrow LP capital, and any grant waiting out OPERATOR_GRANT_DELAY. */
  operatorAllowlist: OperatorAllowlist;
}

export interface OperatorGrantPending {
  address: string;
  /** ISO time from which executeOperatorGrant() succeeds. */
  executableAt: string;
  executable: boolean;
}

export interface OperatorEntry {
  address: string;
  /** allowedOperator(address) at the snapshot block: may this address draw advances now. */
  allowed: boolean;
  /** Whether this is the dashboard's configured operator (SNAPFALL_TREASURY_ADDRESS). */
  configured: boolean;
  pendingGrant: { executableAt: string; executable: boolean } | null;
}

/** How far the operator-event scan reached. Anything but `complete` means an allowed operator or a
 *  queued grant the dashboard has not seen may exist. */
export type OperatorDiscovery =
  | { status: 'complete'; fromBlock: number; throughBlock: number }
  | { status: 'partial'; fromBlock: number; throughBlock: number; reason: string }
  | { status: 'failed'; fromBlock: number; throughBlock: number; reason: string }
  | { status: 'off'; reason: string };

export type OperatorAllowlist =
  | { status: 'unsupported'; reason: string }
  | {
      status: 'ok';
      grantDelaySeconds: number;
      /** SNAPFALL_TREASURY_ADDRESS as configured, lowercased, or null when unset. */
      configuredOperator: string | null;
      /** Every address that is allowed, has a grant queued, or is the configured operator. */
      operators: OperatorEntry[];
      /** Addresses allowedOperator() is true for. */
      allowed: string[];
      /** Queued grants, soonest first. Alert on any you did not queue. */
      pendingGrants: OperatorGrantPending[];
      discovery: OperatorDiscovery;
    };

export interface GovernanceUnsupported {
  status: 'unsupported';
  chainId: number;
  reason: string;
}

export type GovernanceSnapshot = GovernanceControls | GovernanceUnsupported;

function requireAddress(value: string, label: string): string {
  if (!ADDRESS_RE.test(value)) throw new Error(`${label} is not an address: ${value}`);
  return value;
}

function word(raw: string, label: string): bigint {
  if (typeof raw !== 'string' || !WORD_RE.test(raw)) throw new Error(`${label} did not return one 32-byte word`);
  return BigInt(raw);
}

function addressOf(raw: string, label: string): string {
  return '0x' + word(raw, label).toString(16).padStart(40, '0');
}

/** A getter the contract does not have reverts with no data (code 3 on Arc's node). */
function isMissingFunction(error: unknown): boolean {
  if (!(error instanceof RPCError)) return false;
  return error.rpcCode === 3 || /execution reverted/i.test(error.rpcMessage);
}

const iso = (unixSeconds: bigint) => new Date(Number(unixSeconds) * 1000).toISOString();

function pendingAdminOf(address: string, acceptableAt: bigint, now: bigint): PendingAdmin | null {
  if (address === ZERO_ADDRESS) return null;
  return { address, acceptableAt: iso(acceptableAt), acceptable: now >= acceptableAt };
}

// ── The operator allowlist ───────────────────────────────────────────────────
//
// allowedOperator and operatorGrantExecutableAt are mappings, so the contract cannot list who is
// in them. The addresses come from FloatPool's own OperatorAllowed / OperatorGrantQueued /
// OperatorGrantCancelled logs; the STATE of each address is then read by view call at the
// snapshot block. Logs only nominate candidates, so a missed or duplicated log can hide an
// address but never misreport one that is shown.
//
// The scan is incremental and cached on globalThis, like the Float history: Arc is final on
// inclusion (deployments/README.md), so a scanned block never changes. The accumulator is a set
// of addresses, so merging the same range twice (two concurrent requests) is harmless, and each
// chunk commits its addresses and its cursor together with no await in between.

interface OperatorScanState {
  throughBlock: bigint;
  addresses: Set<string>;
}

const operatorGlobals = globalThis as unknown as { __snapfallOperatorScan?: Map<string, OperatorScanState> };
operatorGlobals.__snapfallOperatorScan ??= new Map();
const OPERATOR_SCAN = operatorGlobals.__snapfallOperatorScan;

/** Default share of one load spent scanning, so the caps view never waits on a cold scan. */
export const DEFAULT_OPERATOR_SCAN_BUDGET_MS = 2_500;

interface RPCLog {
  topics: string[];
}

const hexBlock = (n: bigint) => '0x' + n.toString(16);

function topicAddress(topic: string | undefined): string {
  if (typeof topic !== 'string' || !WORD_RE.test(topic)) throw new Error('operator event topic is malformed');
  return '0x' + topic.slice(-40).toLowerCase();
}

async function scanOperatorEvents(
  rpc: RPCTransport,
  pool: string,
  chainId: number,
  head: bigint,
  options: OperatorDiscoveryOptions,
): Promise<{ state: OperatorScanState; discovery: OperatorDiscovery }> {
  const startBlock = BigInt(options.startBlock);
  const key = `${chainId}:${pool.toLowerCase()}:${options.startBlock}`;
  let state = OPERATOR_SCAN.get(key);
  if (!state) {
    state = { throughBlock: startBlock - 1n, addresses: new Set() };
    OPERATOR_SCAN.set(key, state);
  }
  const now = options.now ?? Date.now;
  const deadline = now() + (options.budgetMs ?? DEFAULT_OPERATOR_SCAN_BUDGET_MS);
  const chunk = Number.isFinite(options.chunkBlocks) ? Math.floor(options.chunkBlocks!) : DEFAULT_SCAN_CHUNK_BLOCKS;
  let window = BigInt(Math.max(1, chunk));
  const fromBlock = options.startBlock;
  const through = () => Number(state!.throughBlock);

  while (state.throughBlock < head) {
    if (now() >= deadline) {
      return {
        state,
        discovery: {
          status: 'partial',
          fromBlock,
          throughBlock: through(),
          reason: `operator events scanned through block ${through()} of ${head}; the scan continues on the next load`,
        },
      };
    }
    const from = state.throughBlock + 1n;
    const end = from + window - 1n < head ? from + window - 1n : head;
    let logs: RPCLog[];
    try {
      logs = await rpc<RPCLog[]>('eth_getLogs', [
        {
          address: pool,
          fromBlock: hexBlock(from),
          toBlock: hexBlock(end),
          topics: [[OPERATOR_TOPIC.allowed, OPERATOR_TOPIC.queued, OPERATOR_TOPIC.cancelled]],
        },
      ]);
    } catch (error) {
      if (isRangeError(error) && window > 1n) {
        window = window / 2n > 0n ? window / 2n : 1n; // sticky: the provider's real cap, found once
        continue;
      }
      // The message is not passed on: a provider error can echo the RPC URL and its key.
      return {
        state,
        discovery: {
          status: 'failed',
          fromBlock,
          throughBlock: through(),
          reason: `the RPC failed the operator-event scan (eth_getLogs) after block ${through()}`,
        },
      };
    }
    if (!Array.isArray(logs)) throw new Error('eth_getLogs did not return a list');
    // Commit addresses and cursor together. Another request may have moved the cursor meanwhile;
    // the set makes the overlap harmless and the cursor only moves forward.
    for (const log of logs) state.addresses.add(topicAddress(log.topics?.[1]));
    if (end > state.throughBlock) state.throughBlock = end;
  }
  return { state, discovery: { status: 'complete', fromBlock, throughBlock: through() } };
}

const encodeAddressArg = (selector: string, address: string) => selector + address.slice(2).toLowerCase().padStart(64, '0');

async function loadOperatorAllowlist(
  config: GovernanceConfig,
  rpc: RPCTransport,
  pool: string,
  head: string,
  now: bigint,
): Promise<OperatorAllowlist> {
  const at = (data: string, label: string) =>
    rpc<string>('eth_call', [{ to: pool, data }, head]).then((raw) => word(raw, label));

  // The newest getter decides support, as for the capital controls as a whole.
  let grantDelay: bigint;
  try {
    grantDelay = await at(OPERATOR_SELECTOR.OPERATOR_GRANT_DELAY, 'OPERATOR_GRANT_DELAY');
  } catch (error) {
    if (!isMissingFunction(error)) throw error;
    return {
      status: 'unsupported',
      reason:
        'This FloatPool predates the operator allowlist: it has no OPERATOR_GRANT_DELAY(), so it does not ' +
        'restrict who may draw advances and there are no operator grants to watch. Only a deployment of the ' +
        'current contracts has them.',
    };
  }

  const configured = config.operatorAddress?.trim()
    ? requireAddress(config.operatorAddress.trim(), 'operatorAddress').toLowerCase()
    : null;

  let discovery: OperatorDiscovery;
  const candidates = new Set<string>(configured ? [configured] : []);
  if (config.operatorDiscovery) {
    const scanned = await scanOperatorEvents(rpc, pool, config.chainId, BigInt(head), config.operatorDiscovery);
    for (const a of scanned.state.addresses) candidates.add(a);
    discovery = scanned.discovery;
  } else {
    discovery = { status: 'off', reason: 'operator-event discovery is off; only the configured operator was checked' };
  }

  const operators = await Promise.all(
    [...candidates].sort().map(async (address): Promise<OperatorEntry> => {
      const [allowed, executableAt] = await Promise.all([
        at(encodeAddressArg(OPERATOR_SELECTOR.allowedOperator, address), 'allowedOperator'),
        at(encodeAddressArg(OPERATOR_SELECTOR.operatorGrantExecutableAt, address), 'operatorGrantExecutableAt'),
      ]);
      return {
        address,
        allowed: allowed !== 0n,
        configured: address === configured,
        pendingGrant: executableAt === 0n ? null : { executableAt: iso(executableAt), executable: now >= executableAt },
      };
    }),
  );
  const shown = operators.filter((o) => o.allowed || o.pendingGrant || o.configured);
  const pendingGrants = shown
    .filter((o) => o.pendingGrant)
    .map((o) => ({ address: o.address, ...o.pendingGrant! }))
    .sort((a, b) => a.executableAt.localeCompare(b.executableAt));

  return {
    status: 'ok',
    grantDelaySeconds: Number(grantDelay),
    configuredOperator: configured,
    operators: shown,
    allowed: shown.filter((o) => o.allowed).map((o) => o.address),
    pendingGrants,
    discovery,
  };
}

export async function loadGovernance(config: GovernanceConfig, rpc: RPCTransport): Promise<GovernanceSnapshot> {
  const pool = requireAddress(config.floatPoolAddress, 'floatPoolAddress');
  const vault = requireAddress(config.jobVaultAddress, 'jobVaultAddress');

  // Same posture as floatChain/jobChain: refuse an RPC serving a different chain.
  const servedChainId = Number(BigInt(await rpc<string>('eth_chainId', [])));
  if (servedChainId !== config.chainId) {
    throw new Error(`Arc RPC chain ID ${servedChainId} does not match configured chain ID ${config.chainId}`);
  }

  // One block for every read, so the budget and the pending deadlines are mutually consistent.
  const block = await rpc<{ number: string; timestamp: string } | null>('eth_getBlockByNumber', ['latest', false]);
  if (!block) throw new Error('latest block unavailable');
  const head = block.number;
  const now = BigInt(block.timestamp);

  const call = (to: string, selector: string, label: string) =>
    rpc<string>('eth_call', [{ to, data: selector }, head]).then((raw) => word(raw, label));

  // The newest getter decides support. A deployment without it predates the controls.
  let adminTransferAcceptableAt: bigint;
  try {
    adminTransferAcceptableAt = await call(pool, SELECTOR.adminTransferAcceptableAt, 'adminTransferAcceptableAt');
  } catch (error) {
    if (!isMissingFunction(error)) throw error;
    return {
      status: 'unsupported',
      chainId: config.chainId,
      reason:
        'These contracts predate on-chain capital controls: FloatPool has no adminTransferAcceptableAt(). ' +
        'The caps, loss budget and timelocks exist only on a deployment of the current contracts.',
    };
  }

  const [maxTotalExposure, maxAdvance, totalOutstanding, totalWrittenOff, poolPaused] = await Promise.all([
    call(pool, SELECTOR.maxTotalExposure, 'maxTotalExposure'),
    call(pool, SELECTOR.maxAdvance, 'maxAdvance'),
    call(pool, SELECTOR.totalOutstanding, 'totalOutstanding'),
    call(pool, SELECTOR.totalWrittenOff, 'totalWrittenOff'),
    call(pool, SELECTOR.paused, 'paused'),
  ]);
  const [pendingMaxTotalExposure, pendingMaxAdvance, capRaiseExecutableAt] = await Promise.all([
    call(pool, SELECTOR.pendingMaxTotalExposure, 'pendingMaxTotalExposure'),
    call(pool, SELECTOR.pendingMaxAdvance, 'pendingMaxAdvance'),
    call(pool, SELECTOR.capRaiseExecutableAt, 'capRaiseExecutableAt'),
  ]);
  const [poolAdmin, poolPendingAdmin] = await Promise.all([
    rpc<string>('eth_call', [{ to: pool, data: SELECTOR.admin }, head]).then((r) => addressOf(r, 'admin')),
    rpc<string>('eth_call', [{ to: pool, data: SELECTOR.pendingAdmin }, head]).then((r) => addressOf(r, 'pendingAdmin')),
  ]);
  const [vaultPaused, maxJobPayment, vaultAdmin, vaultPendingAdmin, vaultAcceptableAt] = await Promise.all([
    call(vault, SELECTOR.paused, 'vault paused'),
    call(vault, SELECTOR.maxJobPayment, 'maxJobPayment'),
    rpc<string>('eth_call', [{ to: vault, data: SELECTOR.admin }, head]).then((r) => addressOf(r, 'vault admin')),
    rpc<string>('eth_call', [{ to: vault, data: SELECTOR.pendingAdmin }, head]).then((r) => addressOf(r, 'vault pendingAdmin')),
    call(vault, SELECTOR.adminTransferAcceptableAt, 'vault adminTransferAcceptableAt'),
  ]);

  const operatorAllowlist = await loadOperatorAllowlist(config, rpc, pool, head, now);

  const used = totalOutstanding + totalWrittenOff;
  const remaining = maxTotalExposure > used ? maxTotalExposure - used : 0n;

  return {
    status: 'ok',
    chainId: config.chainId,
    blockNumber: Number(BigInt(head)),
    observedAt: iso(now),
    pool: {
      paused: poolPaused !== 0n,
      admin: poolAdmin,
      maxTotalExposure: maxTotalExposure.toString(),
      maxAdvance: maxAdvance.toString(),
      totalOutstanding: totalOutstanding.toString(),
      totalWrittenOff: totalWrittenOff.toString(),
      pendingAdmin: pendingAdminOf(poolPendingAdmin, adminTransferAcceptableAt, now),
    },
    vault: {
      paused: vaultPaused !== 0n,
      admin: vaultAdmin,
      maxJobPayment: maxJobPayment.toString(),
      pendingAdmin: pendingAdminOf(vaultPendingAdmin, vaultAcceptableAt, now),
    },
    lossBudget: {
      ceiling: maxTotalExposure.toString(),
      outstanding: totalOutstanding.toString(),
      writtenOff: totalWrittenOff.toString(),
      remaining: remaining.toString(),
    },
    pendingCapRaise:
      capRaiseExecutableAt === 0n
        ? null
        : {
            maxTotalExposure: pendingMaxTotalExposure.toString(),
            maxAdvance: pendingMaxAdvance.toString(),
            executableAt: iso(capRaiseExecutableAt),
            executable: now >= capRaiseExecutableAt,
          },
    operatorAllowlist,
  };
}

export const governanceChainInternals = {
  SELECTOR,
  OPERATOR_SELECTOR,
  OPERATOR_TOPIC,
  /** Forget cached operator scans, so a test starts cold. */
  resetOperatorScan: () => OPERATOR_SCAN.clear(),
};
