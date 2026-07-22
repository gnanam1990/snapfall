/**
 * Scripted demo data for the Overview scaffold.
 *
 * This replays the PRD §15.1 demo spine as a live event stream so the dashboard shows the whole
 * Snapfall story on load: 0.00 treasury -> the snap (12.50 advance) -> safe spend -> a rejected
 * purchase -> the waterfall -> the rate flywheel. Numbers use the corrected 150.00 pool seed
 * (see docs: the 100.00 seed reverts the advance on the 10% exposure cap). Replaced wholesale by
 * the real H2 REST/SSE feed once that handshake lands.
 */

import type { OverviewSnapshot, PoolStats, OpenAdvance, FinancialEvent } from './types';

const EXPLORER = 'https://testnet.arcscan.app/tx';

const POOL_BASE: PoolStats = {
  tvlUsdc: '150000000', // 150.00 seeded by demo LPs
  utilizationBps: 0,
  feesAccruedUsdc: '0',
  reserveUsdc: '0',
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

export const snapshot: OverviewSnapshot = {
  treasuryUsdc: '0', // the $0 start · the first 10 seconds of the pitch
  pool: POOL_BASE,
  activeJobs: [
    {
      jobId: 'job_104',
      customer: 'Acme Labs',
      title: 'Competitor analysis · 3 AI coding products',
      state: 'Funded',
      priceUsdc: '25000000',
    },
  ],
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
      ts: new Date().toISOString(),
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
}

export const timeline: TimelineStep[] = [
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
    pool: { ...POOL_BASE, utilizationBps: 833 },
    openAdvances: [ADVANCE_OPEN],
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
    pool: { ...POOL_BASE, utilizationBps: 833 },
    openAdvances: [ADVANCE_OPEN],
  },
  {
    event: {
      category: 'Approval',
      type: 'approval.rejected',
      summary: 'Owner rejected premium dataset · 4.00 USDC, over threshold',
      jobId: 'job_104',
    },
    treasuryUsdc: '12460000',
    pool: { ...POOL_BASE, utilizationBps: 833 },
    openAdvances: [ADVANCE_OPEN],
  },
  {
    event: {
      category: 'Finance',
      type: 'payment.delivered',
      summary: 'x402 purchase: benchmark summary · 0.06 USDC (the cheaper alternative)',
      amountUsdc: '60000',
      jobId: 'job_104',
    },
    treasuryUsdc: '12400000',
    pool: { ...POOL_BASE, utilizationBps: 833 },
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
    pool: { tvlUsdc: '150200000', utilizationBps: 0, feesAccruedUsdc: '250000', reserveUsdc: '50000', orgRateBps: 5000 },
    openAdvances: [],
  },
  {
    event: {
      category: 'Float',
      type: 'rate.updated',
      summary: 'Advance rate 50% → 55% · the business earned cheaper capital',
      jobId: 'job_104',
    },
    treasuryUsdc: '24650000',
    pool: { tvlUsdc: '150200000', utilizationBps: 0, feesAccruedUsdc: '250000', reserveUsdc: '50000', orgRateBps: 5500 },
    openAdvances: [],
  },
  {
    event: {
      category: 'Intake',
      type: 'job.draft.created',
      summary: 'Next cycle · treasury reset to 0.00, rate now starts at 55%',
    },
    treasuryUsdc: '0',
    pool: { tvlUsdc: '150200000', utilizationBps: 0, feesAccruedUsdc: '250000', reserveUsdc: '50000', orgRateBps: 5500 },
    openAdvances: [],
  },
];
