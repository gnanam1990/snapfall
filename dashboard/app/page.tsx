'use client';

import { useEffect, useState } from 'react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, FinancialEvent, StreamMessage } from '@/lib/types';
import { formatUsdc, formatBps } from '@/lib/format';
import MoneyGraph from '@/components/MoneyGraph';
import StatCard from '@/components/StatCard';
import EventFeed from '@/components/EventFeed';
import WorkforceStrip from '@/components/WorkforceStrip';
import AdvancesTable from '@/components/AdvancesTable';
import ActiveJobs from '@/components/ActiveJobs';

export default function OverviewPage() {
  const [snap, setSnap] = useState<OverviewSnapshot | null>(null);
  const [treasury, setTreasury] = useState('0');
  const [pool, setPool] = useState<PoolStats | null>(null);
  const [advances, setAdvances] = useState<OpenAdvance[]>([]);
  const [events, setEvents] = useState<FinancialEvent[]>([]);

  useEffect(() => {
    const es = new EventSource('/api/events/stream');
    es.onmessage = (m) => {
      const msg = JSON.parse(m.data) as StreamMessage;
      if (msg.kind === 'snapshot') {
        setSnap(msg.snapshot);
        setTreasury(msg.snapshot.treasuryUsdc);
        setPool(msg.snapshot.pool);
        setAdvances(msg.snapshot.openAdvances);
        setEvents(msg.snapshot.recentEvents);
      } else {
        setTreasury(msg.treasuryUsdc);
        setPool(msg.pool);
        setAdvances(msg.openAdvances);
        setEvents((prev) => [msg.event, ...prev].slice(0, 14));
      }
    };
    es.onerror = () => es.close();
    return () => es.close();
  }, []);

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
        <span className="badge-live">live · updates in &lt;2s</span>
      </div>

      <MoneyGraph
        event={events[0] ?? null}
        treasuryUsdc={treasury}
        pool={pool}
        jobPriceUsdc={snap.activeJobs[0]?.priceUsdc ?? '25000000'}
      />

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

      <div className="grid cols-2 mt">
        <div className="card">
          <p className="card-title">Recent financial events</p>
          <EventFeed events={events} />
        </div>
        <div className="grid" style={{ gap: 16, alignContent: 'start' }}>
          <div className="card">
            <p className="card-title">Workforce</p>
            <WorkforceStrip agents={snap.workforce} />
          </div>
          <div className="card">
            <p className="card-title">Open advances</p>
            <AdvancesTable advances={advances} />
          </div>
          <div className="card">
            <p className="card-title">Active jobs</p>
            <ActiveJobs jobs={snap.activeJobs} />
          </div>
        </div>
      </div>
    </>
  );
}
