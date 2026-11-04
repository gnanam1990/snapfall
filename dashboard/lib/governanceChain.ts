/**
 * The on-chain capital controls, read straight from FloatPool and JobVault.
 *
 * docs/MAINNET.md tells an operator to watch for CapsRaiseQueued and AdminTransferStarted, and
 * to know how much of the lifetime loss budget is spent. This is that view: the caps, the
 * budget (maxTotalExposure bounds totalOutstanding + totalWrittenOff), whether a cap raise or
 * an admin handoff is pending, and when each becomes executable. It is read from contract
 * state at one block, not from an indexer, so it cannot lag or be replayed wrong.
 *
 * The contracts live on Arc testnet predate these controls (ADR-014 froze them before
 * capital controls existed), so their getters revert. That is reported as `unsupported`,
 * never as zeros: a zero cap would read as "nothing may be lent", which is a claim.
 */

import { RPCError, type RPCTransport } from './floatChain';

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

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const WORD_RE = /^0x[0-9a-fA-F]{64}$/;
const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';

export interface GovernanceConfig {
  chainId: number;
  floatPoolAddress: string;
  jobVaultAddress: string;
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
}

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
  };
}

export const governanceChainInternals = { SELECTOR };
