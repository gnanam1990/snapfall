/**
 * Dashboard data contract — the shapes the Overview (and later pages) render.
 *
 * This mirrors the H2 handshake (Gnanam -> Vasanth+Anandan: REST + SSE + approval POST).
 * H2 is not ratified yet, so this is the CONSUMER's proposed shape: it follows the PRD's
 * core entities (§8.1) and event taxonomy (§8.5). When H2 is agreed at the handshake, only
 * this file and lib/mockData.ts change; the components stay put. Amounts are atomic USDC
 * (6dp) decimal strings, matching the rest of the repo.
 */

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
  treasuryUsdc: string;
  pool: PoolStats;
  activeJobs: JobSummary[];
  pendingApprovals: number;
  workforce: AgentCard[];
  openAdvances: OpenAdvance[];
  recentEvents: FinancialEvent[];
}

/** SSE envelope on /api/events/stream. Each event carries the fields it moved, so the
 *  Overview stays live without re-fetching the whole snapshot. */
export type StreamMessage =
  | { kind: 'snapshot'; snapshot: OverviewSnapshot }
  | { kind: 'event'; event: FinancialEvent; treasuryUsdc: string; pool: PoolStats; openAdvances: OpenAdvance[] };
