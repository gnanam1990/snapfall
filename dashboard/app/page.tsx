'use client';

import Link from 'next/link';
import { useCallback, useEffect, useState } from 'react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, StreamMessage, FloatSnapshot } from '@/lib/types';
import type { ActivityMessage } from '@/lib/activity';
import { humanizeLegacyEvent, humanizeStreamEvent } from '@/lib/activity';
import { formatUsdc, formatUsdcExact, formatBps } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import PoolVessel from '@/components/PoolVessel';
import MoneyGraph from '@/components/MoneyGraph';
import StatCard from '@/components/StatCard';
import Card, { CardHeader, CardBody } from '@/components/Card';
import TeamActivityFeed from '@/components/TeamActivityFeed';
import WorkforceStrip from '@/components/WorkforceStrip';
import AdvancesTable from '@/components/AdvancesTable';
import ActiveJobs from '@/components/ActiveJobs';

export default function OverviewPage() {
  const [snap, setSnap] = useState<OverviewSnapshot | null>(null);
  const [pool, setPool] = useState<PoolStats | null>(null);
  const [advances, setAdvances] = useState<OpenAdvance[] | null>(null);
  const [activity, setActivity] = useState<ActivityMessage[]>([]);
  const [demo, setDemo] = useState(false);
  // Treasury and the pool stats are chain reads, served by /api/float — the same verifiable
  // source the Float page uses, independent of the SSE daemon stream. This is deliberate: a
  // visitor can re-run a balanceOf/pool read, but cannot check a daemon's assertion, and it
  // means the daemon-less public deploy shows real numbers instead of dashes. The SSE stream
  // still supplies the agent feed, workforce, advances, active jobs and approvals.
  const [float, setFloat] = useState<FloatSnapshot | null>(null);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setDemo(msg.demo === true);
      setSnap(msg.snapshot);
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

  // Poll the chain-read snapshot. Independent of the daemon stream, so it populates on the
  // public deploy too. A failed/unavailable read leaves the last value (or null → dashes),
  // never a fabricated number.
  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const res = await fetch('/api/float', { cache: 'no-store' });
        if (!res.ok) return;
        const data = (await res.json()) as FloatSnapshot;
        if (alive) setFloat(data);
      } catch {
        /* keep last known values */
      }
    };
    void load();
    const timer = setInterval(load, 15_000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  // Treasury + pool stats, sourced from the chain read (float), rendered identically whether or
  // not a daemon is connected. formatUsdc/formatBps dash any absent field, so a pending or
  // unavailable read shows a dash, never a wrong or fabricated value.
  const treasuryAndPool = (
    <>
      <PoolVessel tvlUsdc={float ? formatUsdc(float.totalAssetsUsdc) : null} utilizationBps={float?.utilizationBps ?? null} orgRateBps={float?.orgRateBps ?? null} />
      <div className="grid cols-4 mt">
        <StatCard
          label="Pool TVL"
          value={float ? <>{formatUsdc(float.totalAssetsUsdc)} <span className="u">USDC</span></> : '—'}
          sub="read live from Arc testnet"
        />
        <StatCard
          label="Utilization"
          value={float && float.utilizationBps != null ? formatBps(float.utilizationBps) : '—'}
          sub={float ? 'drawn / TVL · cap 80%' : 'reading Arc testnet'}
        />
        <StatCard
          label="Fees accrued"
          value={float ? <>{formatUsdc(float.feesAccruedUsdc)} <span className="u">USDC</span></> : '—'}
          sub={float ? `first-loss reserve ${formatUsdcExact(float.reserveUsdc)}` : '—'}
        />
        <StatCard
          label="Pending approvals"
          value={String(snap?.pendingApprovals ?? 0)}
          // Not "all clear": with no daemon there is nothing to be clear about, and asserting
          // it is the same false zero app/approvals/page.tsx refuses in its own comment.
          sub={
            snap?.pendingApprovals
              ? 'action needed'
              : snap?.daemonConnected === false
                ? 'no daemon connected'
                : 'none awaiting you'
          }
        />
      </div>
    </>
  );

  if (!snap) {
    return (
      <>
        <div className="topbar">
          <h1 className="page-title">Overview</h1>
        </div>
        {treasuryAndPool}
        <div className="loading mt">Connecting to the daemon event stream…</div>
      </>
    );
  }

  // No daemon is wired to this deployment (SNAPFALL_OWNER_API_URL unset, demo replay off).
  // Treasury and the pool stats above ARE shown — they are chain reads, verifiable from any
  // node. What needs the daemon is the agent feed, workforce and approvals; rather than
  // fabricate those, this says so and points at the rest of the verifiable evidence.
  if (snap.daemonConnected === false) {
    return (
      <>
        <div className="topbar">
          <div>
            <h1 className="page-title">Overview</h1>
            <p className="page-sub">One founder, a workforce that finances itself.</p>
          </div>
        </div>
        {treasuryAndPool}
        <Card className="mt">
          <CardHeader title="No daemon connected — showing on-chain data only" />
          <CardBody>
            <p>
              The treasury and pool figures above are read live from Arc testnet. The live agent
              feed, workforce and approvals come from the owner’s daemon, which does not run on this
              public site — so they are omitted rather than fabricated. Everything Snapfall shows is
              meant to be verifiable.
            </p>
            <p className="mt">More on-chain evidence:</p>
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
          <span className="badge-live">{demo ? 'demo replay' : 'live'} · updates in &lt;2s</span>
        ) : (
          <span className="badge-live badge-reconnecting">reconnecting…</span>
        )}
      </div>

      {treasuryAndPool}

      <div className="mt">
        <MoneyGraph
          latest={activity[0] ?? null}
          treasuryUsdc={float?.treasuryUsdc ?? null}
          pool={pool}
          jobPriceUsdc={snap.activeJobs?.[0]?.priceUsdc ?? null}
          live={status === 'live'}
        />
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
