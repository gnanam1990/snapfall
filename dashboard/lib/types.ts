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

export interface JobSummary {
  jobId: string;
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
