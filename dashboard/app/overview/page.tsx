'use client';

import Link from 'next/link';
import type { CSSProperties } from 'react';
import { useChainHistory, useFloat } from '@/lib/useChain';
import { networkLabel } from '@/lib/network';
import OwnerPanel from '@/components/OwnerPanel';
import Cascade from '@/components/Cascade';
import { FeeBars, PoolLevel, RateGauge } from '@/components/Tiles';
import {
  ActivityFeed, CountUp, ErrorNotice, Fall, FallLegend, Freshness, StageText, Steps, StepsMini, pct, shortId, timeAgo, toNumber, usdc, useNow,
} from '@/components/ui';

const at = (i: number): CSSProperties => ({ ['--i' as string]: i });

/**
 * The owner's first screen. It opens on the Snapfall: a live cascade of where the customers' money
 * fell, drawn from the settled jobs on chain, with the one sentence that matters beside it. Below
 * it the working tiles: the pool, the rate, the fees, the agents, every settlement, the open jobs,
 * and the chain's latest lines. Every figure is read from the chain.
 */
export default function OverviewPage() {
  const history = useChainHistory();
  const float = useFloat();
  const now = useNow();
  const h = history.data;
  const f = float.data;
  const t = h?.totals;
  const net = networkLabel();

  const settled = (h?.jobs ?? []).filter((j) => j.settlement);
  const opKept = settled.reduce((sum, j) => sum + toNumber(j.settlement!.operatorNetUsdc), 0);
  const rate = f?.orgRateBps ?? t?.latestRateBps ?? null;
  const lent = f?.utilizationBps ?? null;
  const fees = (h?.jobs ?? [])
    .filter((j) => j.advance?.status === 'Repaid')
    .sort((a, b) => (a.lastAt ?? 0) - (b.lastAt ?? 0))
    .map((j) => toNumber(j.advance!.feeUsdc));

  const open = (h?.jobs ?? []).filter((j) => j.stage === 'Funded' || j.stage === 'InProgress' || j.stage === 'Delivered');
  const focus = open[0];
  const reading = history.error ? 'Unavailable until the chain can be read.' : 'Reading the chain…';

  return (
    <div className="wrap">
      <section className="hero" aria-labelledby="hero-h">
        <div className="hero-copy">
          <div className="hero-top">
            <h1 id="hero-h">Overview</h1>
            <Freshness updatedAt={history.updatedAt} error={history.error} network={net} />
          </div>
          <p className="hero-lede">
            {t ? (
              t.settled ? (
                <>
                  Your agents kept <span className="num"><CountUp value={opKept} format={(v) => usdc(BigInt(Math.round(v * 1e6)))} /></span> USDC from {t.settled} settled job{t.settled === 1 ? '' : 's'}.
                </>
              ) : (
                'No job has settled yet.'
              )
            ) : history.error ? (
              'The chain could not be read just now.'
            ) : (
              'Reading the chain…'
            )}
          </p>
          <dl className="hero-figs">
            <div><dt>Open jobs</dt><dd>{t ? t.active : '—'}</dd></div>
            <div><dt>Jobs ever</dt><dd>{t ? t.jobs : '—'}</dd></div>
            <div><dt>Advances</dt><dd>{t ? t.advancesIssued : '—'}<small>{t ? `${t.advancesRepaid} repaid` : ''}</small></dd></div>
          </dl>
          <Link href="/jobs" className="btn-solid">See every job</Link>
        </div>
        {h ? (
          <Cascade jobs={h.jobs} now={now} />
        ) : (
          <div className="cascade cascade-wait"><p className="cascade-empty">{reading}</p></div>
        )}
      </section>

      {history.error && !h ? (
        <div style={{ margin: '16px 0 0' }}><ErrorNotice message={`The job history could not be read: ${history.error}`} onRetry={history.refresh} /></div>
      ) : null}

      <div className="bento">
        {/* Pool: a vessel filled to the share that is free to lend */}
        <Link href="/float" className="tile tile-pool" style={at(0)} aria-label={`Capital pool: ${f ? usdc(f.totalAssetsUsdc) : 'unknown'} USDC, ${pct(lent, 1)} lent out`}>
          <PoolLevel freeShare={lent === null ? null : 1 - lent / 10_000} />
          <div className="tile-head">
            <span className="tile-label">Capital pool</span>
          </div>
          <div className="tile-big">{f ? usdc(f.totalAssetsUsdc) : '—'}<small>USDC</small></div>
          <p className="tile-note">{f ? `${usdc(f.availableLiquidityUsdc)} free · ${pct(lent, 1)} lent` : float.error ?? 'Reading…'}</p>
        </Link>

        {/* Advance rate: a gauge between the floor and the cap */}
        <Link href="/float" className="tile tile-gauge" style={at(1)}>
          <div className="tile-head">
            <span className="tile-label">Advance rate</span>
            <span className="btn-icon" aria-hidden="true">
              <svg viewBox="0 0 20 20"><path d="M7 4l6 6-6 6" /></svg>
            </span>
          </div>
          <RateGauge rateBps={rate} />
          <div className="gauge-value">{pct(rate)}</div>
        </Link>

        {/* Fees: one column per repaid advance */}
        <div className="tile tile-fees" style={at(2)}>
          <span className="tile-label">Pool fees</span>
          <div className="tile-big">{t ? usdc(t.feesEarnedUsdc, 4) : '—'}<small>USDC</small></div>
          <FeeBars fees={fees} />
          <p className="tile-note">{t ? `${t.advancesRepaid} repaid · ${t.writtenOff} written off` : ' '}</p>
        </div>

        {/* Agents: owner data behind sign-in */}
        <OwnerPanel compact className="tile tile-agents" style={at(3)} />

        {/* Every settlement: split bars */}
        <section className="tile flush t-2x1 tile-fall" style={at(4)} aria-labelledby="fall-h">
          <div className="tile-head">
            <div>
              <h2 className="tile-title" id="fall-h">Settlements</h2>
            </div>
            <Link className="btn-underline" href="/jobs">All jobs</Link>
          </div>
          <div style={{ padding: '12px 24px 0' }}><FallLegend /></div>
          {h ? <Fall jobs={h.jobs} limit={6} /> : <p className="tile-note" style={{ padding: '0 24px' }}>{reading}</p>}
        </section>

        {/* Open jobs: the one in progress, then the rest */}
        <section className="tile flush t-2x1 tile-ticket" style={at(5)} aria-labelledby="open-h">
          <div className="tile-head">
            <div>
              <h2 className="tile-title" id="open-h">Open jobs</h2>
            </div>
            <span className="count-badge">{open.length}</span>
          </div>
          {focus ? (
            <div className="ticket">
              <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, alignItems: 'baseline' }}>
                <Link href={`/jobs/${focus.jobId}`} className="link mono">{shortId(focus.jobId, 10, 6)}</Link>
                <span className="amount">{usdc(focus.paymentUsdc)}<small>USDC</small></span>
              </div>
              <p className="cell-sub" style={{ margin: '2px 0 14px' }}>Last moved {timeAgo(focus.lastAt, now)}</p>
              <Steps job={focus} />
            </div>
          ) : (
            <p className="tile-note" style={{ padding: '0 24px 20px' }}>{h ? 'Nothing open.' : reading}</p>
          )}
          {open.length > 1 ? (
            <table className="table">
              <tbody>
                {open.slice(1, 4).map((j) => (
                  <tr key={`${j.jobId}:${j.createdTx}`}>
                    <td><Link className="mono" href={`/jobs/${j.jobId}`}>{shortId(j.jobId, 8, 4)}</Link></td>
                    <td><StepsMini job={j} /></td>
                    <td className="hide-md"><StageText stage={j.stage} /></td>
                    <td className="r"><span className="amount">{usdc(j.paymentUsdc)}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : null}
        </section>

        {/* Latest on chain: a timeline */}
        <section className="tile flush t-4x1 tile-timeline" style={at(6)} aria-labelledby="act-h">
          <div className="tile-head">
            <div>
              <h2 className="tile-title" id="act-h">Latest on chain</h2>
            </div>
            <Link className="btn-underline" href="/activity">All activity</Link>
          </div>
          <div style={{ marginTop: 12 }}>
            {h ? <ActivityFeed items={h.activity.slice(0, 6)} explorer={h.explorerUrl} now={now} /> : <p className="tile-note" style={{ padding: '0 24px 20px' }}>{reading}</p>}
          </div>
        </section>
      </div>
    </div>
  );
}
