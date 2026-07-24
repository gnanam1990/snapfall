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

export default function OverviewPage() {
  const [snap, setSnap] = useState<OverviewSnapshot | null>(null);
  const [treasury, setTreasury] = useState<string | null>(null);
  const [pool, setPool] = useState<PoolStats | null>(null);
  const [advances, setAdvances] = useState<OpenAdvance[] | null>(null);
  const [activity, setActivity] = useState<ActivityMessage[]>([]);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setSnap(msg.snapshot);
      setTreasury(msg.snapshot.treasuryUsdc);
      setPool(msg.snapshot.pool);
      setAdvances(msg.snapshot.openAdvances);
      const recent = (msg.snapshot.recentEvents ?? []).map(humanizeLegacyEvent);
      setActivity((previous) => {
        const ids = new Set(recent.map((item) => item.id));
        return [...recent, ...previous.filter((item) => !ids.has(item.id))].slice(0, 30);
      });
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

  if (!snap) {
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

      <TreasuryHero treasuryUsdc={treasury} orgRateBps={pool?.orgRateBps ?? null} />

      <div className="grid cols-4 mt">
        <StatCard
          label="Pool TVL"
          value={pool ? <>{formatUsdc(pool.tvlUsdc)} <span className="u">USDC</span></> : '—'}
          sub={pool ? 'seeded by demo LPs' : 'awaiting chain indexer'}
        />
        <StatCard
          label="Utilization"
          value={pool ? formatBps(pool.utilizationBps) : '—'}
          sub={pool ? 'drawn / TVL · cap 80%' : 'awaiting chain indexer'}
        />
        <StatCard
          label="Fees accrued"
          value={pool ? <>{formatUsdc(pool.feesAccruedUsdc)} <span className="u">USDC</span></> : '—'}
          sub={pool ? `first-loss reserve ${formatUsdc(pool.reserveUsdc)}` : 'awaiting chain indexer'}
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
            {advances === null ? <div className="empty">Awaiting chain indexer.</div> : <AdvancesTable advances={advances} />}
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
