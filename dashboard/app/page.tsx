'use client';

import { useCallback, useState } from 'react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, StreamMessage } from '@/lib/types';
import type { ActivityMessage } from '@/lib/activity';
import { humanizeLegacyEvent, humanizeStreamEvent } from '@/lib/activity';
import { formatUsdc, formatBps } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import TreasuryHero from '@/components/TreasuryHero';
import StatCard from '@/components/StatCard';
import TeamActivityFeed from '@/components/TeamActivityFeed';
import WorkforceStrip from '@/components/WorkforceStrip';
import AdvancesTable from '@/components/AdvancesTable';
import ActiveJobs from '@/components/ActiveJobs';

const EMPTY_POOL: PoolStats = {
  tvlUsdc: '0',
  utilizationBps: 0,
  feesAccruedUsdc: '0',
  reserveUsdc: '0',
  orgRateBps: 0,
};

export default function OverviewPage() {
  const [snap, setSnap] = useState<OverviewSnapshot | null>(null);
  const [treasury, setTreasury] = useState('0');
  const [pool, setPool] = useState<PoolStats | null>(null);
  const [advances, setAdvances] = useState<OpenAdvance[]>([]);
  const [activity, setActivity] = useState<ActivityMessage[]>([]);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setSnap(msg.snapshot);
      setTreasury(msg.snapshot.treasuryUsdc ?? '0');
      // The real H2 daemon starts before chain projections, so null money aggregates
      // are a valid snapshot—not a reason to hide daemon activity behind a loader.
      setPool(msg.snapshot.pool ?? EMPTY_POOL);
      setAdvances(msg.snapshot.openAdvances ?? []);
      setActivity((msg.snapshot.recentEvents ?? []).map(humanizeLegacyEvent));
    } else {
      const next = humanizeStreamEvent(msg);
      setActivity((prev) => [next, ...prev.filter((item) => item.id !== next.id)].slice(0, 30));
      const aggregates = msg.aggregates;
      if (aggregates?.treasuryUsdc != null) setTreasury(aggregates.treasuryUsdc);
      if (aggregates?.pool) setPool(aggregates.pool);
      if (aggregates?.openAdvances) setAdvances(aggregates.openAdvances);
      if (aggregates && (aggregates.activeJobs || aggregates.pendingApprovals !== undefined)) {
        setSnap((s) =>
          s
            ? {
                ...s,
                activeJobs: aggregates.activeJobs ?? s.activeJobs,
                pendingApprovals: aggregates.pendingApprovals ?? s.pendingApprovals,
              }
            : s,
        );
      }
    }
  }, []);

  const status = useEventStream('/api/events/stream', onMessage);

  if (!snap || !pool) {
    return (
      <>
        <div className="topbar">
          <h1 className="page-title">Overview</h1>
        </div>
        <div className="loading">Connecting to the daemon event stream…</div>
      </>
    );
  }

  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Overview</h1>
          <p className="page-sub">One founder, a workforce that finances itself.</p>
        </div>
        {status === 'live' ? (
          <span className="badge-live">demo replay · updates in &lt;2s</span>
        ) : (
          <span className="badge-live badge-reconnecting">reconnecting…</span>
        )}
      </div>

      <TreasuryHero treasuryUsdc={treasury} orgRateBps={pool.orgRateBps} />

      <div className="grid cols-4 mt">
        <StatCard label="Pool TVL" value={<>{formatUsdc(pool.tvlUsdc)} <span className="u">USDC</span></>} sub="seeded by demo LPs" />
        <StatCard label="Utilization" value={formatBps(pool.utilizationBps)} sub="drawn / TVL · cap 80%" />
        <StatCard
          label="Fees accrued"
          value={<>{formatUsdc(pool.feesAccruedUsdc)} <span className="u">USDC</span></>}
          sub={`first-loss reserve ${formatUsdc(pool.reserveUsdc)}`}
        />
        <StatCard label="Pending approvals" value={String(snap.pendingApprovals)} sub={snap.pendingApprovals ? 'action needed' : 'all clear'} />
      </div>

      <div className="activity-layout mt">
        <TeamActivityFeed messages={activity} live={status === 'live'} />
        <div className="grid" style={{ gap: 16, alignContent: 'start' }}>
          <div className="card">
            <p className="card-title">Workforce</p>
            <WorkforceStrip agents={snap.workforce ?? []} />
          </div>
          <div className="card">
            <p className="card-title">Open advances</p>
            <AdvancesTable advances={advances} />
          </div>
          <div className="card">
            <p className="card-title">Active jobs</p>
            <ActiveJobs jobs={snap.activeJobs ?? []} />
          </div>
        </div>
      </div>
    </>
  );
}
