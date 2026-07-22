'use client';

import { useEffect, useState } from 'react';
import { motion } from 'framer-motion';
import { Lightning, Waves, Bank, Gauge, Coins, BellRinging } from '@phosphor-icons/react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, FinancialEvent, StreamMessage } from '@/lib/types';
import { formatUsdc, formatBps } from '@/lib/format';
import { fadeUp } from '@/lib/motion';
import MoneyGraph from '@/components/MoneyGraph';
import StatCard from '@/components/StatCard';
import Card, { CardTitle } from '@/components/Card';
import EventFeed from '@/components/EventFeed';
import WorkforceStrip from '@/components/WorkforceStrip';
import AdvancesTable from '@/components/AdvancesTable';
import ActiveJobs from '@/components/ActiveJobs';

const inlineIcon: React.CSSProperties = {
  display: 'inline',
  verticalAlign: 'middle',
  position: 'relative',
  top: -2,
  margin: '0 4px',
};

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
        setEvents((prev) => [msg.event, ...prev].slice(0, 12));
      }
    };
    es.onerror = () => es.close();
    return () => es.close();
  }, []);

  return (
    <>
      {/* hero: the brand line in the template's inline-icon heading treatment */}
      <div className="pb-7 pt-4 text-left sm:pt-7">
        <motion.h1
          variants={fadeUp}
          custom={0}
          initial="hidden"
          animate="visible"
          className="m-0"
          style={{ fontSize: 'clamp(1.65rem, 4.2vw, 2.7rem)', lineHeight: 1.05, letterSpacing: '-0.01em' }}
        >
          <span className="whitespace-nowrap">
            Capital in a snap
            <span className="inline-icon" style={inlineIcon}>
              <Lightning size={26} weight="regular" color="var(--color-text)" />
            </span>
          </span>
          <br />
          settlement in a waterfall
          <span className="inline-icon" style={{ ...inlineIcon, marginLeft: 6 }}>
            <Waves size={26} weight="regular" color="var(--color-text)" />
          </span>
        </motion.h1>
        <motion.p
          variants={fadeUp}
          custom={1}
          initial="hidden"
          animate="visible"
          className="mb-0 mt-3 max-w-[560px]"
          style={{ color: 'var(--color-muted)', lineHeight: 1.65, fontSize: 'clamp(0.9rem, 2.2vw, 1.05rem)' }}
        >
          One founder. A workforce that can&apos;t embezzle itself. A business that gets cheaper to run
          every time it delivers.
        </motion.p>
      </div>

      {!snap || !pool ? (
        <div className="py-20 text-center text-sm" style={{ color: 'var(--color-muted)' }}>
          Connecting to the daemon event stream…
        </div>
      ) : (
        <>
          <motion.div variants={fadeUp} custom={2} initial="hidden" animate="visible">
            <MoneyGraph
              event={events[0] ?? null}
              treasuryUsdc={treasury}
              pool={pool}
              jobPriceUsdc={snap.activeJobs[0]?.priceUsdc ?? '25000000'}
            />
          </motion.div>

          <div className="mt-4 grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard index={3} label="Pool TVL" icon={Bank}
              value={formatUsdc(pool.tvlUsdc)} sub="USDC · seeded by demo LPs" />
            <StatCard index={4} label="Utilization" icon={Gauge}
              value={formatBps(pool.utilizationBps)} sub="drawn / TVL · cap 80%" />
            <StatCard index={5} label="Fees accrued" icon={Coins}
              value={formatUsdc(pool.feesAccruedUsdc)} sub={`USDC · reserve ${formatUsdc(pool.reserveUsdc)}`} />
            <StatCard index={6} label="Pending approvals" icon={BellRinging}
              value={String(snap.pendingApprovals)} sub={snap.pendingApprovals ? 'action needed' : 'all clear'} />
          </div>

          <div className="mt-4 grid gap-4 lg:grid-cols-[1.4fr_1fr] [&>*]:min-w-0">
            <motion.div variants={fadeUp} custom={7} initial="hidden" animate="visible">
              <Card>
                <CardTitle>Recent financial events</CardTitle>
                <EventFeed events={events} />
              </Card>
            </motion.div>
            <div className="grid content-start gap-4 [&>*]:min-w-0">
              <motion.div variants={fadeUp} custom={8} initial="hidden" animate="visible">
                <Card>
                  <CardTitle>Workforce</CardTitle>
                  <WorkforceStrip agents={snap.workforce} />
                </Card>
              </motion.div>
              <motion.div variants={fadeUp} custom={9} initial="hidden" animate="visible">
                <Card>
                  <CardTitle>Open advances</CardTitle>
                  <AdvancesTable advances={advances} />
                </Card>
              </motion.div>
              <motion.div variants={fadeUp} custom={10} initial="hidden" animate="visible">
                <Card>
                  <CardTitle>Active jobs</CardTitle>
                  <ActiveJobs jobs={snap.activeJobs} />
                </Card>
              </motion.div>
            </div>
          </div>
        </>
      )}
    </>
  );
}
