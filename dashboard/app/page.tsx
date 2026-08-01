'use client';

import Link from 'next/link';
import { useCallback, useState } from 'react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, StreamMessage } from '@/lib/types';
import type { ActivityMessage } from '@/lib/activity';
import { humanizeLegacyEvent, humanizeStreamEvent } from '@/lib/activity';
import { formatUsdc, formatBps } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import TreasuryHero from '@/components/TreasuryHero';
import MoneyGraph from '@/components/MoneyGraph';
import StatCard from '@/components/StatCard';
import Card, { CardHeader, CardBody } from '@/components/Card';
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
      const recent = (msg.snapshot.recentEvents ?? []).map((e) => humanizeLegacyEvent(e, msg.demo === true));
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

  // No daemon is wired to this deployment (SNAPFALL_OWNER_API_URL unset, demo replay off).
  // Rather than fabricate a feed, say so plainly and send the visitor to the real evidence:
  // the live on-chain Float page, and the chain-verified settlement proof in docs/addresses.md.
  if (snap.daemonConnected === false) {
    return (
      <>
        <div className="topbar">
          <div>
            <h1 className="page-title">Overview</h1>
            <p className="page-sub">One founder, a workforce that finances itself.</p>
          </div>
        </div>
        <Card>
          <CardHeader title="No daemon connected to this deployment" />
          <CardBody>
            <p>
              The live agent feed, treasury and workforce come from the owner’s daemon, which does
              not run on this public site. Rather than show fabricated activity, this page shows
              nothing here — everything Snapfall displays is meant to be verifiable.
            </p>
            <p className="mt">
              The real, on-chain evidence is still one click away:
            </p>
            <ul className="disconnected-links mt">
              <li>
                <Link href="/float">Float</Link> — live pool, advance rate and history, read
                directly from Arc testnet.
              </li>
              <li>
                <a
                  href="https://github.com/gnanam1990/snapfall/blob/main/docs/addresses.md"
                  target="_blank"
                  rel="noreferrer"
                >
                  docs/addresses.md
                </a>{' '}
                — the deployed contract addresses and the actual settlement transactions, each
                verifiable on the explorer.
              </li>
            </ul>
          </CardBody>
        </Card>
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

      <div className="mt">
        <MoneyGraph
          latest={activity[0] ?? null}
          treasuryUsdc={treasury}
          pool={pool}
          jobPriceUsdc={snap.activeJobs?.[0]?.priceUsdc ?? null}
          live={status === 'live'}
        />
      </div>

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
        {/* The right rail is three plain surfaces with a kicker, which is exactly what the Card
            primitives express. Adopting them turns each kicker from a styled <p> into a real <h3>
            inside a header row, so the rail becomes three headings a screen reader can jump between
            instead of three anonymous divs. Titles and bodies are unchanged. */}
        <div className="grid" style={{ gap: 16, alignContent: 'start' }}>
          <Card>
            <CardHeader title="Workforce" />
            <CardBody>
              <WorkforceStrip agents={snap.workforce ?? []} />
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="Open advances" />
            <CardBody>
              {advances === null ? <div className="empty">Awaiting chain indexer.</div> : <AdvancesTable advances={advances} />}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="Active jobs" />
            <CardBody>
              <ActiveJobs jobs={snap.activeJobs ?? []} />
            </CardBody>
          </Card>
        </div>
      </div>
    </>
  );
}
