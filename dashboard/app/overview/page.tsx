'use client';

import Link from 'next/link';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { OverviewSnapshot, PoolStats, OpenAdvance, StreamMessage, FloatSnapshot, RateHistoryPoint } from '@/lib/types';
import type { ActivityMessage } from '@/lib/activity';
import { humanizeLegacyEvent, humanizeStreamEvent } from '@/lib/activity';

/** Read a string field off an unknown chain-event payload without trusting its shape. */
function payloadString(payload: unknown, key: string): string | null {
  if (payload && typeof payload === 'object' && key in payload) {
    const v = (payload as Record<string, unknown>)[key];
    return typeof v === 'string' ? v : null;
  }
  return null;
}
import { formatUsdc, formatUsdcExact, formatBps } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import PoolCapacity from '@/components/PoolCapacity';
import MoneyGraph from '@/components/MoneyGraph';
import ScoreRing from '@/components/ScoreRing';
import Card, { CardHeader, CardBody } from '@/components/Card';
import TeamActivityFeed from '@/components/TeamActivityFeed';
import WorkforceStrip from '@/components/WorkforceStrip';
import AdvancesTable from '@/components/AdvancesTable';
import ActiveJobs from '@/components/ActiveJobs';

/**
 * Pending approvals, as the first viewport's one coloured region and its primary action.
 *
 * The contract asks for two things the stat tile could not do: that this be the single place
 * colour appears, and that the primary action sit with it. Colour is --warn, because a purchase
 * held for a decision is caution, not alarm -- and it appears ONLY when something is actually
 * waiting, so the colour means "you have work" rather than decorating a zero.
 *
 * The action is a link to /approvals rather than an inline approve/refuse pair, deliberately.
 * Deciding binds to the intentHash the owner was shown, needs the reason field the worker adapts
 * on, and has to handle STALE_VIEW, expiry and double-submit. That path exists once, on the
 * approvals surface, and is tested there. A second implementation of an irreversible money action
 * on a different surface is how the two drift apart, and the cost of getting it wrong is a
 * payment. So the primary action here is to go and decide, and the deciding stays in one place.
 */
function PendingApprovals({
  count,
  daemonConnected,
}: {
  count: number | null;
  daemonConnected: boolean;
}) {
  if (!daemonConnected) {
    return (
      <section className="pending is-quiet mt">
        <p className="pending-body">
          Purchases an agent may not make alone arrive here. No daemon is connected, so none can:
          this is not a quiet queue.
        </p>
      </section>
    );
  }
  if (!count) {
    return (
      <section className="pending is-quiet mt">
        <p className="pending-body">Nothing is waiting on you. Escalations appear here.</p>
      </section>
    );
  }
  return (
    <section className="pending is-waiting mt" aria-labelledby="pending-title">
      <span className="pending-count" aria-hidden="true">
        {count}
      </span>
      <div className="pending-text">
        <h2 className="pending-title" id="pending-title">
          {count === 1 ? 'A purchase is waiting on you' : `${count} purchases are waiting on you`}
        </h2>
        <p className="pending-body">
          An agent has reached a spend it may not make alone. The line is shut until you answer.
        </p>
      </div>
      <Link className="pending-action" href="/approvals">
        Review and decide <span aria-hidden="true">→</span>
      </Link>
    </section>
  );
}

// The advance-rate progression: the sequence of on-chain rate changes that makes "earned, not set"
// concrete, shown beside the ring. Reads float.rateHistoryBps (the historical scan) and renders
// nothing when that history is unavailable — e.g. a public RPC that cannot complete the log scan —
// rather than inventing a curve.
function RateProgression({ points }: { points: RateHistoryPoint[] | null }) {
  if (!points || points.length === 0) return null;
  return (
    <div className="rate-progression">
      <p className="rate-progression-cap">Earned, not set</p>
      <ol className="rate-progression-seq" aria-label="Advance rate history">
        {points.map((p, i) => (
          <li key={`${p.rateBps}-${i}`} className={i === points.length - 1 ? 'is-current' : undefined}>
            {formatBps(p.rateBps)}
          </li>
        ))}
      </ol>
      <p className="rate-progression-note">Each accepted job raises the rate; the chain is the record.</p>
    </div>
  );
}

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
  // The org the ScoreRing represents (the operator), read from /api/float. Held in a ref so the
  // stable onMessage callback always sees the current value without re-subscribing the stream —
  // and so a RateChanged for a DIFFERENT org is rejected. The ring shows the operator's rate; it
  // must not move on another org's rate. Single-org demo or not, the check is not optional.
  const orgRef = useRef<string | null>(null);

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

      // A live RateChanged chain frame folds its new rate into the pool aggregate the ScoreRing
      // reads (MoneyGraph passes pool.orgRateBps to the ring) — one source of truth (`pool`), and
      // ScoreRing stays a scalar renderer that never learns this event kind exists. Chain frames
      // carry no aggregates, so without this the ring only moved on reconnect. Two guards, both
      // load-bearing: the rate is applied ONLY when the frame's org matches the operator org the
      // ring shows (another org's RateChanged is ignored), and rateBps is a base-10 STRING on the
      // wire (indexer decode.go) so a non-numeric value is dropped rather than turning the ring
      // into NaN.
      if (msg.source === 'chain' && msg.event.kind === 'RateChanged') {
        const org = payloadString(msg.event.payload, 'org');
        const rateStr = payloadString(msg.event.payload, 'rateBps');
        const rate = rateStr === null ? NaN : Number.parseInt(rateStr, 10);
        const ringOrg = orgRef.current;
        if (org && ringOrg && org.toLowerCase() === ringOrg.toLowerCase() && Number.isFinite(rate)) {
          setPool((p) => (p ? { ...p, orgRateBps: rate } : p));
        }
      }

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
        if (alive) {
          setFloat(data);
          // Keep the ring's org current for the RateChanged guard in onMessage.
          orgRef.current = data.orgAddress ?? null;
        }
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
      <PoolCapacity
        tvlUsdc={float ? formatUsdc(float.totalAssetsUsdc) : null}
        utilizationBps={float?.utilizationBps ?? null}
        orgRateBps={float?.orgRateBps ?? null}
        // formatUsdcExact returns the dash glyph for null, not null, so passing it straight
        // through would render "— USDC" as if it were a value and lose the absent styling.
        // feesAccruedUsdc IS null on the live endpoint today, so this is the real case.
        feesAccruedUsdc={float?.feesAccruedUsdc ? formatUsdcExact(float.feesAccruedUsdc) : null}
        reserveUsdc={float?.reserveUsdc ? formatUsdcExact(float.reserveUsdc) : null}
      />
      {/* Pending approvals are the first viewport's single coloured region, and they carry its
          primary action: go and decide. The deciding itself stays on /approvals, because a
          second implementation of an irreversible money action is how two implementations
          drift. */}
      <PendingApprovals
        count={snap?.pendingApprovals ?? null}
        daemonConnected={snap?.daemonConnected !== false}
      />
    </>
  );

  // The advance rate is the claim the whole project rests on, so it LEADS the page and the vessel
  // follows — a visitor's first impression should be the rate, not a pool balance (the order was
  // inverted). Rate source: the live pool aggregate when a daemon is streaming, else the /api/float
  // chain read, so a daemon-less public visitor still sees it. Kept within the direction contract:
  // the ScoreRing is this world's own device, not the hero-metric tile the contract refuses.
  const rateHero = (
    <section className="rate-hero" aria-label="Advance rate, earned from delivery history">
      <ScoreRing
        rateBps={pool?.orgRateBps ?? float?.orgRateBps ?? null}
        jobPriceUsdc={snap?.activeJobs?.[0]?.priceUsdc ?? null}
      />
      <RateProgression points={float?.rateHistoryBps ?? null} />
    </section>
  );

  if (!snap) {
    return (
      <>
        <div className="page-header">
          <div className="page-header-text">
            <h1>Overview</h1>
          </div>
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
        <div className="page-header">
          <div className="page-header-text">
            <h1>Overview</h1>
            <p className="page-header-sub">One founder, a workforce that finances itself.</p>
          </div>
        </div>
        {rateHero}
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
      <div className="page-header">
        <div className="page-header-text">
          <h1>Overview</h1>
          <p className="page-header-sub">One founder, a workforce that finances itself.</p>
        </div>
        <span className="page-header-aside">
          {status === 'live' ? (
            <span className="badge-live">{demo ? 'demo replay' : 'live'} · updates in &lt;2s</span>
          ) : (
            <span className="badge-live badge-reconnecting">reconnecting…</span>
          )}
        </span>
      </div>

      {rateHero}

      {treasuryAndPool}

      <div className="mt">
        <MoneyGraph
          latest={activity[0] ?? null}
          treasuryUsdc={float?.treasuryUsdc ?? null}
          pool={pool}
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
