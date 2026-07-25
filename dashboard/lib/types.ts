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
  historyStatus: 'complete' | 'unavailable';
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

/** Ratified H2 envelope: one stream, daemon and chain source vocabularies unchanged. */
export type StreamMessage =
  | { kind: 'snapshot'; snapshot: OverviewSnapshot }
  | {
      kind: 'event';
      source: 'daemon' | 'chain';
      seq: number | string;
      event: StreamEvent;
      aggregates?: OverviewAggregates;
    };
