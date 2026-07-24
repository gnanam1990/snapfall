/**
 * Scripted demo data for the Overview scaffold.
 *
 * This replays the PRD §15.1 demo spine as a live event stream so the dashboard shows the whole
 * Snapfall story on load: funding -> the snap (12.50 advance) -> safe spend -> a rejected
 * purchase -> the waterfall -> the rate flywheel -> an explicit loop reset. Numbers use the
 * corrected 150.00 pool seed (a 100.00 seed reverts the advance on the 10% exposure cap).
 *
 * Review notes applied (PR #8): each step now carries the job state it changed, so active
 * jobs update as the story advances; the loop ends with an EXPLICIT reset event that returns
 * treasury AND rate to the opening state, so the replay never shows a silent rate regression.
 * Replaced wholesale by the real H2 REST/SSE feed once that handshake lands.
 */

import type { OverviewSnapshot, PoolStats, OpenAdvance, FinancialEvent, JobSummary } from './types';

const EXPLORER = 'https://testnet.arcscan.app/tx';

const POOL_BASE: PoolStats = {
  tvlUsdc: '150000000', // 150.00 seeded by demo LPs
  utilizationBps: 0,
  feesAccruedUsdc: '0',
  reserveUsdc: '0',
  orgRateBps: 5000,
};

const POOL_DRAWN: PoolStats = { ...POOL_BASE, utilizationBps: 833 };

const POOL_SETTLED: PoolStats = {
  tvlUsdc: '150200000',
  utilizationBps: 0,
  feesAccruedUsdc: '250000',
  reserveUsdc: '50000',
  orgRateBps: 5000,
};

const ADVANCE_OPEN: OpenAdvance = {
  jobId: 'job_104',
  org: '0x0000000000000000000000000000000000000000',
  principalUsdc: '12500000',
  feeUsdc: '250000',
  rateBps: 5000,
  status: 'Issued',
};

const JOB = (state: JobSummary['state']): JobSummary[] => [
  {
    jobId: 'job_104',
    customer: 'Acme Labs',
    title: 'Competitor analysis · 3 AI coding products',
    state,
    priceUsdc: '25000000',
  },
];

export const snapshot: OverviewSnapshot = {
  treasuryUsdc: '0', // the $0 start · the first 10 seconds of the pitch
  pool: POOL_BASE,
  activeJobs: JOB('Funded'),
  pendingApprovals: 0,
  workforce: [
    { id: 'brain', role: 'Brain', status: 'idle' },
    { id: 'research', role: 'Research', status: 'idle' },
    { id: 'delivery', role: 'Delivery', status: 'idle' },
    { id: 'qa', role: 'QA', status: 'idle' },
    { id: 'funding', role: 'Funding', status: 'idle' },
  ],
  openAdvances: [],
  recentEvents: [
    {
      seq: 100,
      ts: new Date().toISOString(), // re-stamped per connection by the stream route
      category: 'Job',
      type: 'job.funded',
      summary: 'Acme Labs funded job_104 · 25.00 USDC escrowed',
      amountUsdc: '25000000',
      jobId: 'job_104',
      explorerUrl: `${EXPLORER}/0xfund`,
    },
  ],
};

/** One replayed step: the event plus the state it left behind. */
export interface TimelineStep {
  event: Omit<FinancialEvent, 'seq' | 'ts'>;
  treasuryUsdc: string;
  pool: PoolStats;
  openAdvances: OpenAdvance[];
  activeJobs?: JobSummary[];
  pendingApprovals?: number;
}

export const timeline: TimelineStep[] = [
  {
    event: {
      category: 'Job',
      type: 'job.funded',
      summary: 'Acme Labs funded job_104 · 25.00 USDC escrowed',
      amountUsdc: '25000000',
      jobId: 'job_104',
      explorerUrl: `${EXPLORER}/0xfund`,
    },
    treasuryUsdc: '0',
    pool: POOL_BASE,
    openAdvances: [],
    activeJobs: JOB('Funded'),
  },
  {
    event: {
      category: 'Float',
      type: 'advance.issued',
      summary: 'The snap · 12.50 USDC advanced to a 0-balance treasury at 50%',
      amountUsdc: '12500000',
      jobId: 'job_104',
      explorerUrl: `${EXPLORER}/0xadvance`,
    },
    treasuryUsdc: '12500000',
    pool: POOL_DRAWN,
    openAdvances: [ADVANCE_OPEN],
    activeJobs: JOB('InProgress'),
  },
  {
    event: {
      category: 'Finance',
      type: 'payment.delivered',
      summary: 'x402 purchase: company profile · 0.04 USDC (policy auto-approved)',
      amountUsdc: '40000',
      jobId: 'job_104',
    },
    treasuryUsdc: '12460000',
    pool: POOL_DRAWN,
    openAdvances: [ADVANCE_OPEN],
  },
  {
    event: {
      category: 'Approval',
      type: 'approval.requested',
      summary: 'Brain requested approval for premium market data · 4.00 USDC',
      amountUsdc: '4000000',
      jobId: 'job_104',
    },
    treasuryUsdc: '12460000',
    pool: POOL_DRAWN,
    openAdvances: [ADVANCE_OPEN],
    pendingApprovals: 1,
  },
  {
    event: {
      category: 'Approval',
      type: 'approval.request_alternative',
      summary: 'Owner asked the team to find a cheaper alternative',
      jobId: 'job_104',
    },
    treasuryUsdc: '12460000',
    pool: POOL_DRAWN,
    openAdvances: [ADVANCE_OPEN],
    pendingApprovals: 0,
  },
  {
    event: {
      category: 'Finance',
      type: 'approval.alternative_found',
      summary: 'Due Diligence found a cheaper benchmark source · 0.06 USDC',
      amountUsdc: '60000',
      jobId: 'job_104',
    },
    treasuryUsdc: '12400000',
    pool: POOL_DRAWN,
    openAdvances: [ADVANCE_OPEN],
  },
  {
    event: {
      category: 'Job',
      type: 'job.accepted',
      summary: 'Watch the Snapfall · pool repaid 12.75 first, operator 12.25',
      amountUsdc: '12750000',
      jobId: 'job_104',
      explorerUrl: `${EXPLORER}/0xwaterfall`,
    },
    treasuryUsdc: '24650000',
    pool: POOL_SETTLED,
    openAdvances: [],
    activeJobs: JOB('Accepted'),
  },
  {
    event: {
      category: 'Float',
      type: 'rate.updated',
      summary: 'Advance rate 50% → 55% · the business earned cheaper capital',
      jobId: 'job_104',
    },
    treasuryUsdc: '24650000',
    pool: { ...POOL_SETTLED, orgRateBps: 5500 },
    openAdvances: [],
  },
  {
    event: {
      category: 'Intake',
      type: 'job.draft.created',
      summary: 'Demo loop restarts · treasury, rate, and job reset to the opening state',
    },
    treasuryUsdc: '0',
    pool: POOL_BASE,
    openAdvances: [],
    activeJobs: JOB('Funded'),
    pendingApprovals: 0,
  },
];
