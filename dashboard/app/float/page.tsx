'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { formatBps, formatUsdcExact, isSafeExplorerUrl, relativeTime } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import type {
  FloatLossTotals,
  FloatOpenAdvance,
  FloatSnapshot,
  OpenAdvance,
  PoolStats,
  StreamMessage,
} from '@/lib/types';

const RATE = {
  base: 5_000,
  growth: 500,
  penalty: 1_500,
  floor: 3_000,
  cap: 8_500,
};

const FLOAT_EVENTS = new Set(['AdvanceIssued', 'AdvanceRepaid', 'AdvanceWrittenOff', 'RateChanged']);

function shortIdentifier(value: string, start = 8, end = 6): string {
  if (value.length <= start + end + 1) return value;
  return `${value.slice(0, start)}…${value.slice(-end)}`;
}

function sumAtomic(advances: OpenAdvance[]): bigint {
  return advances.reduce((total, advance) => total + BigInt(advance.principalUsdc), 0n);
}

function MetricModule({
  tone,
  symbol,
  label,
  value,
  note,
}: {
  tone: 'liquidity' | 'fees' | 'reserve';
  symbol: string;
  label: string;
  value: React.ReactNode;
  note: string;
}) {
  return (
    <article className={`float-metric ${tone}`}>
      <span className="float-metric-symbol" aria-hidden="true">{symbol}</span>
      <div>
        <p>{label}</p>
        <strong>{value}</strong>
        <small>{note}</small>
      </div>
    </article>
  );
}

function RateEngine({ snapshot, fallbackRateBps }: { snapshot: FloatSnapshot; fallbackRateBps: number | null }) {
  const rate = snapshot.orgRateBps ?? fallbackRateBps;
  const accepted = snapshot.acceptedJobs;
  const writtenOff = snapshot.writtenOffJobs;
  const ratePosition =
    rate === null ? 0 : Math.max(0, Math.min(100, ((rate - RATE.floor) / (RATE.cap - RATE.floor)) * 100));
  const delta = rate === null ? null : rate - RATE.base;

  return (
    <section className="rate-engine" aria-labelledby="rate-engine-title">
      <div className="float-section-head">
        <div>
          <p className="float-eyebrow">Credit mechanics</p>
          <h2 id="rate-engine-title">Rate engine</h2>
        </div>
        {snapshot.orgAddress ? (
          <a
            className="float-proof-link"
            href={`${snapshot.explorerUrl.replace(/\/address\/.+$/, '')}/address/${snapshot.orgAddress}`}
            target="_blank"
            rel="noreferrer"
          >
            {shortIdentifier(snapshot.orgAddress)} ↗
          </a>
        ) : (
          <span className="float-muted">Set SNAPFALL_TREASURY_ADDRESS to select an organization</span>
        )}
      </div>

      <div className="rate-engine-grid">
        <div className="rate-current">
          <span>Advance rate</span>
          <strong>{rate === null ? '—' : formatBps(rate)}</strong>
          <div className={`rate-delta${delta !== null && delta < 0 ? ' is-negative' : ''}`}>
            {delta === null ? (
              'Organization unavailable'
            ) : delta === 0 ? (
              'At the protocol base rate'
            ) : (
              <>{delta > 0 ? '↑' : '↓'} {Math.abs(delta) / 100} pts from delivery history</>
            )}
          </div>
        </div>

        <div className="rate-derivation">
          <div className="rate-equation" aria-label="Advance-rate derivation">
            <div className="rate-term base">
              <strong>50%</strong>
              <span>base</span>
            </div>
            <span className="rate-operator">+</span>
            <div className="rate-term growth">
              <strong>{accepted === null ? '—' : `${accepted * 5}%`}</strong>
              <span>5% × {accepted ?? '—'} accepted</span>
            </div>
            <span className="rate-operator">−</span>
            <div className="rate-term penalty">
              <strong>{writtenOff === null ? '—' : `${writtenOff * 15}%`}</strong>
              <span>15% × {writtenOff ?? '—'} write-offs</span>
            </div>
            <span className="rate-operator">=</span>
            <div className="rate-term result">
              <strong>{rate === null ? '—' : formatBps(rate)}</strong>
              <span>current rate</span>
            </div>
          </div>

          <div className="rate-scale">
            <div className="rate-scale-line" />
            {rate !== null ? (
              <div className="rate-marker" style={{ left: `${ratePosition}%` }}>
                <strong>{formatBps(rate)}</strong>
                <span />
              </div>
            ) : null}
            <div className="rate-scale-label floor"><strong>30%</strong><span>floor</span></div>
            <div className="rate-scale-label base"><strong>50%</strong><span>base</span></div>
            <div className="rate-scale-label cap"><strong>85%</strong><span>cap</span></div>
          </div>
        </div>
      </div>

      <p className="rate-proof-note">
        <span aria-hidden="true">✓</span>
        Rate is computed entirely on-chain. No oracle, no manual credit score.
      </p>
    </section>
  );
}

function OpenAdvances({
  advances,
  losses,
}: {
  advances: OpenAdvance[] | null;
  losses: FloatLossTotals | null;
}) {
  const outstanding = useMemo(() => (advances ? sumAtomic(advances) : null), [advances]);
  const totalLosses = losses
    ? BigInt(losses.bondSlashedUsdc) + BigInt(losses.reserveUsedUsdc) + BigInt(losses.socializedUsdc)
    : null;

  return (
    <>
      <section className="float-advances" aria-labelledby="open-advances-title">
        <div className="float-section-head">
          <div>
            <p className="float-eyebrow">Active exposure</p>
            <h2 id="open-advances-title">Open advances</h2>
          </div>
          <p className="float-advance-summary">
            {advances === null ? 'History unavailable' : (
              <>
                {advances.length} open <span>·</span>{' '}
                <strong>{formatUsdcExact(outstanding!)} USDC</strong> outstanding
              </>
            )}
          </p>
        </div>

        {advances === null ? (
          <div className="float-empty is-unavailable">
            <strong>Open-advance history is awaiting H2 or a private RPC.</strong>
            <span>Current pool totals above remain direct FloatPool reads.</span>
          </div>
        ) : advances.length === 0 ? (
          <div className="float-empty">
            <strong>No capital currently deployed.</strong>
            <span>The table will update when FloatPool emits AdvanceIssued.</span>
          </div>
        ) : (
          <div className="float-table-wrap">
            <table className="float-table">
              <thead>
                <tr>
                  <th>Job</th>
                  <th>Organization</th>
                  <th>Principal</th>
                  <th>Fee</th>
                  <th>Rate</th>
                  <th>Opened</th>
                  <th>Status</th>
                  <th>Proof</th>
                </tr>
              </thead>
              <tbody>
                {advances.map((advance) => {
                  const chainDetails = advance as Partial<FloatOpenAdvance>;
                  const openedAt = chainDetails.openedAt ?? null;
                  const explorerUrl = chainDetails.explorerUrl;
                  return (
                  <tr key={advance.jobId}>
                    <td className="mono" title={advance.jobId}>{shortIdentifier(advance.jobId)}</td>
                    <td className="mono" title={advance.org}>{shortIdentifier(advance.org, 8, 4)}</td>
                    <td><strong>{formatUsdcExact(advance.principalUsdc)}</strong> <span className="u">USDC</span></td>
                    <td>{formatUsdcExact(advance.feeUsdc)} <span className="u">USDC</span></td>
                    <td>{formatBps(advance.rateBps)}</td>
                    <td>{openedAt ? relativeTime(openedAt) : '—'}</td>
                    <td><span className="float-issued"><i />{advance.status}</span></td>
                    <td>
                      {explorerUrl && isSafeExplorerUrl(explorerUrl) ? (
                        <a href={explorerUrl} target="_blank" rel="noreferrer">View ↗</a>
                      ) : (
                        '—'
                      )}
                    </td>
                  </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="loss-waterfall" aria-labelledby="loss-waterfall-title">
        <div className="loss-copy">
          <p className="float-eyebrow">Default protection</p>
          <h2 id="loss-waterfall-title">Loss waterfall</h2>
          <p>Absorbs defaults in that order.</p>
          <strong className={totalLosses === 0n ? 'is-clear' : totalLosses === null ? '' : 'has-loss'}>
            {totalLosses === null
              ? 'History unavailable'
              : totalLosses === 0n
                ? '✓ No losses recorded'
                : `${formatUsdcExact(totalLosses)} USDC absorbed`}
          </strong>
        </div>
        <ol className="loss-stages">
          <li>
            <span>1</span>
            <div><strong>Operator bond</strong><small>{losses ? `${formatUsdcExact(losses.bondSlashedUsdc)} USDC` : '—'}</small></div>
          </li>
          <li>
            <span>2</span>
            <div><strong>First-loss reserve</strong><small>{losses ? `${formatUsdcExact(losses.reserveUsedUsdc)} USDC` : '—'}</small></div>
          </li>
          <li>
            <span>3</span>
            <div><strong>LP capital</strong><small>{losses ? `${formatUsdcExact(losses.socializedUsdc)} USDC` : '—'}</small></div>
          </li>
        </ol>
      </section>
    </>
  );
}

export default function FloatPage() {
  const [snapshot, setSnapshot] = useState<FloatSnapshot | null>(null);
  const [h2Pool, setH2Pool] = useState<PoolStats | null>(null);
  const [h2Advances, setH2Advances] = useState<OpenAdvance[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      const response = await fetch('/api/float', { cache: 'no-store' });
      const body = (await response.json()) as FloatSnapshot | { message?: string };
      if (!response.ok) throw new Error('message' in body ? body.message : 'FloatPool snapshot unavailable');
      setSnapshot(body as FloatSnapshot);
      setError(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'FloatPool snapshot unavailable');
    } finally {
      setRefreshing(false);
    }
  }, []);

  const onStreamMessage = useCallback(
    (message: StreamMessage) => {
      if (message.kind === 'snapshot') {
        setH2Pool(message.snapshot.pool);
        setH2Advances(message.snapshot.openAdvances);
        return;
      }
      if (message.aggregates?.pool) setH2Pool(message.aggregates.pool);
      if (message.aggregates?.openAdvances) setH2Advances(message.aggregates.openAdvances);
      if (
        message.source === 'chain' &&
        FLOAT_EVENTS.has(message.event.kind)
      ) {
        void refresh();
      }
    },
    [refresh],
  );
  const streamStatus = useEventStream('/api/events/stream', onStreamMessage);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 15_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  if (!snapshot) {
    return (
      <>
        <div className="topbar">
          <div>
            <h1 className="page-title">Float</h1>
            <p className="page-sub">Working capital, priced by delivery history.</p>
          </div>
        </div>
        {error ? (
          <div className="float-error" role="alert">
            <strong>FloatPool data is unavailable.</strong>
            <span>{error}</span>
            <button type="button" onClick={() => void refresh()} disabled={refreshing}>
              {refreshing ? 'Retrying…' : 'Retry chain read'}
            </button>
          </div>
        ) : (
          <div className="loading">Reading FloatPool at the latest Arc block…</div>
        )}
      </>
    );
  }

  const utilizationWithinCap = Math.max(0, Math.min(100, (snapshot.utilizationBps / 8_000) * 100));
  const feesAccrued = snapshot.feesAccruedUsdc ?? h2Pool?.feesAccruedUsdc ?? null;
  const displayedAdvances = snapshot.openAdvances ?? h2Advances;
  const displayedRate = snapshot.orgRateBps ?? h2Pool?.orgRateBps ?? null;

  return (
    <div className="float-page">
      <div className="topbar float-topbar">
        <div>
          <h1 className="page-title">Float</h1>
          <p className="page-sub">Working capital, priced by delivery history.</p>
        </div>
        <div className="float-live-meta">
          <span className={`float-live${streamStatus !== 'live' ? ' is-waiting' : ''}`}>
            Arc testnet · {streamStatus === 'live' ? 'live' : 'chain polling'}
          </span>
          <small>block {snapshot.blockNumber.toLocaleString('en-US')}</small>
        </div>
      </div>

      {error ? <div className="float-stale" role="status">Latest refresh failed · showing block {snapshot.blockNumber.toLocaleString('en-US')}</div> : null}

      <section className="float-capital" aria-labelledby="pool-capital-title">
        <div className="float-capital-top">
          <div>
            <p className="float-eyebrow">Pool capital</p>
            <h2 id="pool-capital-title">
              {formatUsdcExact(snapshot.totalAssetsUsdc)} <span>USDC</span>
            </h2>
            <p>{formatUsdcExact(snapshot.totalOutstandingUsdc)} USDC deployed</p>
          </div>
          <a className="float-proof-link" href={snapshot.explorerUrl} target="_blank" rel="noreferrer">
            Verify pool ↗
          </a>
        </div>
        <div className="utilization-labels">
          <strong>{formatBps(snapshot.utilizationBps)} <span>utilized</span></strong>
          <span>80% protocol cap</span>
        </div>
        <div
          className="utilization-track"
          role="progressbar"
          aria-label="Pool utilization against the protocol cap"
          aria-valuemin={0}
          aria-valuemax={8_000}
          aria-valuenow={snapshot.utilizationBps}
        >
          <span style={{ width: `${utilizationWithinCap}%` }} />
          <i /><i /><i /><i /><i /><i /><i />
        </div>
      </section>

      <div className="float-metrics">
        <MetricModule
          tone="liquidity"
          symbol="≈"
          label="Available liquidity"
          value={<>{formatUsdcExact(snapshot.availableLiquidityUsdc)} <span className="u">USDC</span></>}
          note="LP capital ready to deploy"
        />
        <MetricModule
          tone="fees"
          symbol="↗"
          label="Fees earned"
          value={feesAccrued === null ? '—' : <>{formatUsdcExact(feesAccrued)} <span className="u">USDC</span></>}
          note={feesAccrued === null ? 'Awaiting H2 or private RPC history' : 'Cumulative AdvanceRepaid fees'}
        />
        <MetricModule
          tone="reserve"
          symbol="◇"
          label="First-loss reserve"
          value={<>{formatUsdcExact(snapshot.reserveUsdc)} <span className="u">USDC</span></>}
          note="20% of fees retained"
        />
      </div>

      <p className="float-accounting-note">
        <span aria-hidden="true">i</span>
        LP-owned capital excludes the reserve. Values observed at Arc block {snapshot.blockNumber.toLocaleString('en-US')}.
      </p>

      <RateEngine snapshot={snapshot} fallbackRateBps={displayedRate} />
      <OpenAdvances advances={displayedAdvances} losses={snapshot.losses} />
    </div>
  );
}
