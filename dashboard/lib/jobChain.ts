/**
 * Authoritative read-only view of ONE job, straight from the frozen contracts (V7).
 *
 * The job detail page shows money, so it reads the chain rather than trusting a cached
 * projection: JobVault.jobs gives the lifecycle state, the quote, the operating budget, the
 * recorded expenses and the delivery hash in a single eth_call, and FloatPool.openAdvanceOf
 * plus advanceRate give the advance card. No eth_getLogs — the public endpoint rate-limits
 * history hard, so the timeline comes from the H2 event stream instead (see the page).
 *
 * Every amount stays an atomic-USDC decimal string end to end; nothing is parsed into a
 * JS number on the way to the UI.
 */

import { createRPCTransport, type RPCTransport } from './floatChain';

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const JOB_ID_RE = /^0x[0-9a-fA-F]{64}$/;

/** Verified against viem's toFunctionSelector; advanceRate/totalAssets agree with the
 *  already-proven table in floatChain.ts, which cross-checks the derivation. */
const SELECTOR = {
  jobs: '0x38ed7cfc', // jobs(bytes32) -> the whole Job struct, 9 static words
  openAdvanceOf: '0x37367c9f', // openAdvanceOf(bytes32) -> (principal, fee, open)
  advanceRate: '0x95790ee5', // advanceRate(address) -> uint16 bps
} as const;

/** JobVault.JobStatus, in the contract's own declaration order. */
export const JOB_STATUS = [
  'Created',
  'Funded',
  'InProgress',
  'Delivered',
  'Accepted',
  'Refunded',
  'Cancelled',
] as const;

export type ChainJobStatus = (typeof JOB_STATUS)[number];

export interface JobChainConfig {
  chainId: number;
  rpcUrl: string;
  jobVaultAddress: string;
  floatPoolAddress: string;
  explorerUrl: string;
}

/** One job as the chain reports it. Nulls mean "not knowable", never zero. */
export interface JobSnapshot {
  jobId: string;
  exists: boolean;
  status: ChainJobStatus | null;
  statusIndex: number | null;
  customer: string | null;
  operator: string | null;
  /** The chain-authoritative quote (PR #36 settled that the chain wins over any cache). */
  customerPaymentUsdc: string | null;
  maxOperatingBudgetUsdc: string | null
  onchainExpensesUsdc: string | null;
  budgetRemainingUsdc: string | null;
  /** Basis points of the operating budget already recorded on chain, for the budget bar. */
  budgetUsedBps: number | null;
  termsHash: string | null;
  /** Null until submitDelivery lands; SC-JV-004 requires it before acceptance. */
  deliveryHash: string | null;
  deadline: string | null;
  advance: {
    principalUsdc: string;
    feeUsdc: string;
    /** Total owed back to the pool: principal + fee, repaid FIRST by the waterfall. */
    repaymentUsdc: string;
    open: boolean;
    /** An advance that closed still happened; absence is a zero principal (see Oracle). */
    drawn: boolean;
  } | null;
  /**
   * Whether the pool read actually answered.
   *
   * `advance: null` had two meanings that render identically and mean opposite things: the pool
   * holds no advance for this job, or the eth_call failed and we know nothing. The second was
   * being drawn as the first, so one timeout produced "no advance drawn", "repaid to the pool
   * 0.00" and an operator payout of the entire escrow -- three affirmative figures, all false.
   * Nulls mean "not knowable", never zero, and this is what lets the surface tell them apart.
   */
  advanceRead: 'ok' | 'unavailable';
  /** The org's live rate, which is what the next advance would be priced at. */
  orgRateBps: number | null;
  /** What the operator receives on acceptance: payment - repayment. */
  operatorNetUsdc: string | null;
  explorer: {
    jobVault: string;
    floatPool: string;
  };
  observedAt: string;
}

function hexToBigInt(value: string, label: string): bigint {
  if (typeof value !== 'string' || !/^0x[0-9a-fA-F]*$/.test(value)) {
    throw new Error(`${label} did not return hex`);
  }
  return value === '0x' ? 0n : BigInt(value);
}

/** Split an eth_call return into 32-byte words, refusing a short response outright. */
function wordsOf(data: string, minimum: number, label: string): string[] {
  if (typeof data !== 'string' || !data.startsWith('0x')) throw new Error(`${label} did not return hex`);
  const body = data.slice(2);
  if (body.length < minimum * 64) {
    throw new Error(`${label} returned ${body.length / 2} bytes, want at least ${minimum * 32}`);
  }
  const out: string[] = [];
  for (let i = 0; i < body.length; i += 64) out.push(body.slice(i, i + 64));
  return out;
}

const uintOf = (w: string): bigint => (w ? BigInt('0x' + w) : 0n);
const addressOf = (w: string): string => '0x' + w.slice(24);
const bytes32Of = (w: string): string => '0x' + w;
const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';
const ZERO_BYTES32 = '0x' + '0'.repeat(64);

function encodeWordCall(selector: string, word32: string): string {
  return `${selector}${word32.replace(/^0x/, '').toLowerCase()}`;
}

function encodeAddressCall(selector: string, address: string): string {
  return `${selector}${address.replace(/^0x/, '').toLowerCase().padStart(64, '0')}`;
}

function requireAddress(value: string, label: string): string {
  if (!ADDRESS_RE.test(value)) throw new Error(`${label} is not an address: ${value}`);
  return value;
}

/**
 * Load one job. Throws only on configuration or transport failure; a job the vault has
 * never seen comes back as `exists: false` rather than an error, because "no such job" is a
 * legitimate answer the page must render.
 */
export async function loadJobSnapshot(
  config: JobChainConfig,
  jobId: string,
  transport?: RPCTransport,
): Promise<JobSnapshot> {
  if (!JOB_ID_RE.test(jobId)) {
    throw new Error(`job id must be 0x-prefixed bytes32 hex, got ${jobId}`);
  }
  const jobVault = requireAddress(config.jobVaultAddress, 'jobVaultAddress');
  const floatPool = requireAddress(config.floatPoolAddress, 'floatPoolAddress');
  const rpc = transport ?? createRPCTransport(config.rpcUrl);
  const explorerBase = config.explorerUrl.replace(/\/$/, '');
  const explorer = {
    jobVault: `${explorerBase}/address/${jobVault}`,
    floatPool: `${explorerBase}/address/${floatPool}`,
  };
  const observedAt = new Date().toISOString();

  const raw = await rpc<string>('eth_call', [
    { to: jobVault, data: encodeWordCall(SELECTOR.jobs, jobId) },
    'latest',
  ]);
  const w = wordsOf(raw, 9, 'jobs(bytes32)');

  const customer = addressOf(w[0]!);
  // JobVault treats a zero customer as "unknown job" (every guard starts there), so that is
  // the honest existence test rather than inferring from a zero payment.
  if (customer.toLowerCase() === ZERO_ADDRESS) {
    return {
      jobId,
      exists: false,
      status: null,
      statusIndex: null,
      customer: null,
      operator: null,
      customerPaymentUsdc: null,
      maxOperatingBudgetUsdc: null,
      onchainExpensesUsdc: null,
      budgetRemainingUsdc: null,
      budgetUsedBps: null,
      termsHash: null,
      deliveryHash: null,
      deadline: null,
      advance: null,
      advanceRead: 'ok',
      orgRateBps: null,
      operatorNetUsdc: null,
      explorer,
      observedAt,
    };
  }

  const operator = addressOf(w[1]!);
  const payment = uintOf(w[2]!);
  const budget = uintOf(w[3]!);
  const expenses = uintOf(w[4]!);
  const termsHash = bytes32Of(w[5]!);
  const deliveryHashRaw = bytes32Of(w[6]!);
  const deadlineSeconds = uintOf(w[7]!);
  const statusIndex = Number(uintOf(w[8]!));

  // The advance and the rate are independent reads; a failure on either must not blank the
  // whole page, so they degrade to null while the vault facts stay visible.
  let advance: JobSnapshot['advance'] = null;
  let advanceReadFailed = false;
  try {
    const advRaw = await rpc<string>('eth_call', [
      { to: floatPool, data: encodeWordCall(SELECTOR.openAdvanceOf, jobId) },
      'latest',
    ]);
    const aw = wordsOf(advRaw, 3, 'openAdvanceOf(bytes32)');
    const principal = uintOf(aw[0]!);
    const fee = uintOf(aw[1]!);
    advance = {
      principalUsdc: principal.toString(),
      feeUsdc: fee.toString(),
      repaymentUsdc: (principal + fee).toString(),
      open: uintOf(aw[2]!) !== 0n,
      drawn: principal !== 0n,
    };
  } catch {
    advance = null;
    advanceReadFailed = true;
  }

  let orgRateBps: number | null = null;
  try {
    const rateRaw = await rpc<string>('eth_call', [
      { to: floatPool, data: encodeAddressCall(SELECTOR.advanceRate, operator) },
      'latest',
    ]);
    const rate = hexToBigInt(rateRaw, 'advanceRate(address)');
    if (rate <= 10_000n) orgRateBps = Number(rate);
  } catch {
    orgRateBps = null;
  }

  const remaining = budget > expenses ? budget - expenses : 0n;
  const usedBps = budget > 0n ? Number((expenses * 10_000n) / budget) : 0;
  // `drawn`, NOT `open`, and deliberately so. JobVault.sol:216 gates on `open` because it is
  // deciding what to transfer right now; this figure is a reconstruction of what the waterfall
  // did or would do, and for an Accepted job the advance has already been repaid, so `open` is
  // false while the operator's payout was still payment - repayment. Gating on `open` here would
  // report the operator receiving the entire escrow for every settled job -- the demo's final
  // beat. jobChain.test.ts pins exactly that case.
  //
  // The case this does get wrong is a WRITTEN-OFF advance, where open is false, the principal is
  // still recorded, and the pool rather than the operator absorbed the loss. openAdvanceOf cannot
  // distinguish repaid from written off (FloatPool.writeOff only flips the status), so the page
  // suppresses this projection for the terminal states where that is possible instead of
  // pretending to know. Reading FloatPool.advances(bytes32) for the real three-state status is
  // the proper fix and needs a fourth eth_call.
  const repayment = advance?.drawn ? BigInt(advance.repaymentUsdc) : 0n;
  // Unknowable, not zero: if the pool read failed there is no basis for a payout figure.
  const operatorNet = advanceReadFailed ? null : payment > repayment ? payment - repayment : 0n;

  return {
    jobId,
    exists: true,
    status: JOB_STATUS[statusIndex] ?? null,
    statusIndex: Number.isFinite(statusIndex) ? statusIndex : null,
    customer,
    operator,
    customerPaymentUsdc: payment.toString(),
    maxOperatingBudgetUsdc: budget.toString(),
    onchainExpensesUsdc: expenses.toString(),
    budgetRemainingUsdc: remaining.toString(),
    budgetUsedBps: usedBps,
    termsHash,
    deliveryHash: deliveryHashRaw === ZERO_BYTES32 ? null : deliveryHashRaw,
    deadline: deadlineSeconds === 0n ? null : new Date(Number(deadlineSeconds) * 1000).toISOString(),
    advance,
    orgRateBps,
    advanceRead: advanceReadFailed ? 'unavailable' : 'ok',
    operatorNetUsdc: operatorNet === null ? null : operatorNet.toString(),
    explorer,
    observedAt,
  };
}

export const jobChainInternals = { SELECTOR, wordsOf, encodeWordCall, encodeAddressCall };
