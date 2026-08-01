/** Dashboard data contract. Amounts are atomic USDC (6dp) decimal strings. */

export type JobState =
  | 'Draft'
  | 'AwaitingFunding'
  | 'Funded'
  | 'InProgress'
  | 'DeliveryReady'
  | 'Delivered'
  | 'Accepted'
  | 'Refunded'
  | 'Cancelled'
  | 'Failed';

export type AgentStatus = 'idle' | 'working' | 'waiting' | 'approval-required' | 'failed' | 'frozen';

export type AdvanceStatus = 'Issued' | 'Repaid' | 'WrittenOff';

/** PRD §8.5 event categories. */
export type EventCategory = 'Intake' | 'Job' | 'Float' | 'Task' | 'Agent' | 'Action' | 'Finance' | 'Approval' | 'Audit';

export interface FinancialEvent {
  seq: number;
  ts: string; // ISO
  category: EventCategory;
  type: string; // e.g. "advance.issued", "payment.delivered"
  summary: string; // human-readable one-liner
  amountUsdc?: string; // atomic, when the event moves money
  jobId?: string;
  explorerUrl?: string;
}

export interface AgentCard {
  id: string;
  role: string; // Brain / Research / Delivery / QA / Funding
  status: AgentStatus;
  currentTask?: string;
}

export interface OpenAdvance {
  jobId: string;
  org: string;
  principalUsdc: string;
  feeUsdc: string;
  rateBps: number;
  status: AdvanceStatus;
}

export interface FloatOpenAdvance extends OpenAdvance {
  openedAt: string | null;
  txHash: string;
  explorerUrl: string;
}

export interface FloatLossTotals {
  bondSlashedUsdc: string;
  reserveUsedUsdc: string;
  socializedUsdc: string;
}

/** One point on the org's advance-rate curve. Ordered strictly by block (a write-off
 *  lowers the rate, so the series is not monotonic). The origin point (rate 5000, the
 *  base) has no tx — it is the pre-tick value, never emitted on chain. */
export interface RateHistoryPoint {
  rateBps: number;
  blockNumber: number;
  txHash: string | null;
}

/** Authoritative read-only FloatPool snapshot returned by /api/float. */
export interface FloatSnapshot {
  chainId: number;
  blockNumber: number;
  poolAddress: string;
  explorerUrl: string;
  totalAssetsUsdc: string;
  totalOutstandingUsdc: string;
  availableLiquidityUsdc: string;
  utilizationBps: number;
  feesAccruedUsdc: string | null;
  reserveUsdc: string;
  orgAddress: string | null;
  orgRateBps: number | null;
  acceptedJobs: number | null;
  writtenOffJobs: number | null;
  openAdvances: FloatOpenAdvance[] | null;
  losses: FloatLossTotals | null;
  /** The org's rate progression from chain (RateChanged ticks + the base origin).
   *  null while the history scan is pending or unavailable. */
  rateHistoryBps: RateHistoryPoint[] | null;
  // 'pending' = immediate views returned; the historical log scan is still running and
  // history fields are null for now (a later poll returns them 'complete').
  historyStatus: 'complete' | 'unavailable' | 'pending';
  observedAt: string;
}

export interface JobSummary {
  /** The daemon's business job id (e.g. job_demo_1) — NOT the chain entity. */
  jobId: string;
  /**
   * The bytes32 JobVault entity, when the job has an on-chain identity. The daemon models
   * these separately (jobs.vault_job_id), and a job has no vault id until it is created on
   * chain, so this is optional and its absence is a real state, not missing data. V7's
   * detail page can only read the chain when this is present.
   */
  vaultJobId?: string | null;
  customer: string;
  title: string;
  state: JobState;
  priceUsdc: string;
}

export interface PoolStats {
  tvlUsdc: string;
  utilizationBps: number;
  feesAccruedUsdc: string;
  reserveUsdc: string;
  orgRateBps: number;
}

export interface OverviewSnapshot {
  treasuryUsdc: string | null;
  pool: PoolStats | null;
  activeJobs: JobSummary[] | null;
  pendingApprovals: number;
  workforce?: AgentCard[] | null;
  openAdvances: OpenAdvance[] | null;
  /** Present in the local demo fixture; the real H2 snapshot may omit history. */
  recentEvents?: FinancialEvent[] | null;
  /**
   * False when this deployment has no daemon wired (SNAPFALL_OWNER_API_URL unset) and the
   * demo replay is off. The Overview renders an honest "no daemon connected" banner instead
   * of a hero + activity feed, so a public visitor never sees fabricated agent activity.
   * Absent/true on a real daemon snapshot and on the local demo replay.
   */
  daemonConnected?: boolean;
}

export interface StreamEvent {
  kind: string;
  jobId?: string;
  entityId?: string;
  actor?: string;
  at: string;
  payload?: unknown;
}

export interface OverviewAggregates {
  treasuryUsdc?: string | null;
  pool?: PoolStats | null;
  openAdvances?: OpenAdvance[] | null;
  activeJobs?: JobSummary[] | null;
  pendingApprovals?: number;
}

/** Ratified H2 envelope: one stream, daemon and chain source vocabularies unchanged.
 *  `demo: true` marks a message from the local scripted replay so consumers can flag its
 *  contents — above all its explorer links — as placeholders, never as real chain proof. */
export type StreamMessage =
  | { kind: 'snapshot'; snapshot: OverviewSnapshot; demo?: boolean }
  | {
      kind: 'event';
      source: 'daemon' | 'chain';
      seq: number | string;
      event: StreamEvent;
      aggregates?: OverviewAggregates;
      demo?: boolean;
    };
