'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { formatBps, formatUsdcExact, isSafeExplorerUrl, relativeTime } from '@/lib/format';
import { rateProvenance, mayClaimOnChain } from '@/lib/rateProvenance';
import { useEventStream } from '@/lib/useEventStream';
import Badge from '@/components/Badge';
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

/* FloatPool.sol's pool-wide cap, marked on the meter rather than described. */
const UTILISATION_CAP = 0.8; // UTILIZATION_CAP_BPS 8000

const FLOAT_EVENTS = new Set(['AdvanceIssued', 'AdvanceRepaid', 'AdvanceWrittenOff', 'RateChanged']);

function shortIdentifier(value: string, start = 8, end = 6): string {
  if (value.length <= start + end + 1) return value;
  return `${value.slice(0, start)}…${value.slice(-end)}`;
}

function sumAtomic(advances: OpenAdvance[]): bigint {
  return advances.reduce((total, advance) => total + BigInt(advance.principalUsdc), 0n);
}

function MetricModule({
  label,
  value,
  note,
  absent,
}: {
  label: string;
  value: React.ReactNode;
  note: string;
  absent?: boolean;
}) {
  return (
    <article className="float-metric">
      <p>{label}</p>
      <strong className={absent ? 'is-absent' : undefined}>{value}</strong>
      <small>{note}</small>
    </article>
  );
}

function RateEngine({ snapshot, fallbackRateBps }: { snapshot: FloatSnapshot; fallbackRateBps: number | null }) {
  // Provenance, not just value. snapshot.orgRateBps is an eth_call against FloatPool.advanceRate;
  // fallbackRateBps is the daemon's own assertion off the H2 stream (page.tsx:349), which under
  // SNAPFALL_DEMO_STREAM is the hardcoded 5000 in lib/mockData.ts. Both land in `rate`, so the
  // proof note below has to know which one it is before claiming the figure came from the chain.
  // The decision is a tested function rather than a ternary here: see lib/rateProvenance.test.ts.
  const provenance = rateProvenance(snapshot.orgRateBps, fallbackRateBps);
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
          <h2 id="rate-engine-title">Rate engine</h2>
          <p className="float-section-sub">The advance rate, derived from delivery history.</p>
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
          <strong className={rate === null ? 'is-absent' : undefined}>
            {rate === null ? 'not reported' : formatBps(rate)}
          </strong>
          {/* The delta is a state claim, so it is one hue and no box: pos when history lifted the
              rate, neg when it cut it, muted when there is no rate to speak of. */}
          <p
            className={`rate-delta${
              rate === null ? ' is-absent' : delta !== null && delta < 0 ? ' is-negative' : ''
            }`}
          >
            {/* delta is null exactly when rate is; branching on it lets the compiler narrow. */}
            {delta === null ? (
              'No organisation configured'
            ) : delta === 0 ? (
              'At the protocol base rate'
            ) : (
              <>{delta > 0 ? '↑' : '↓'} {Math.abs(delta) / 100} pts from delivery history</>
            )}
          </p>
        </div>

        <div className="rate-derivation">
          {/* The derivation as one line of arithmetic. A term the history scan has not reported
              yet says so, at the muted floor — a missing term is not a zero. */}
          <div className="rate-equation" aria-label="Advance-rate derivation">
            <div className="rate-term">
              <strong>50%</strong>
              <span>base</span>
            </div>
            <span className="rate-operator">+</span>
            <div className="rate-term">
              <strong className={accepted === null ? 'is-absent' : undefined}>
                {accepted === null ? 'unknown' : `${accepted * 5}%`}
              </strong>
              <span>5% × {accepted === null ? 'unknown' : accepted} accepted</span>
            </div>
            <span className="rate-operator">−</span>
            <div className="rate-term">
              <strong className={writtenOff === null ? 'is-absent' : undefined}>
                {writtenOff === null ? 'unknown' : `${writtenOff * 15}%`}
              </strong>
              <span>15% × {writtenOff === null ? 'unknown' : writtenOff} write-offs</span>
            </div>
            <span className="rate-operator">=</span>
            <div className="rate-term result">
              <strong className={rate === null ? 'is-absent' : undefined}>
                {rate === null ? 'unknown' : formatBps(rate)}
              </strong>
              <span>current rate</span>
            </div>
          </div>

          {/* The rate plotted where it sits between the protocol floor and cap. The track's ends
              ARE the bounds, so the only standing tick is the base rate; the marker is the
              current figure. */}
          <div className="rate-scale">
            <div className="rate-track">
              <span className="rate-tick" style={{ left: '36.36%' }} />
              {rate !== null ? (
                <span className="rate-marker" style={{ left: `${ratePosition}%` }}>
                  <span className="rate-marker-fig">{formatBps(rate)}</span>
                </span>
              ) : null}
            </div>
            <div className="rate-scale-labels">
              <span style={{ left: 0 }}>30% floor</span>
              <span style={{ left: '36.36%', transform: 'translateX(-50%)' }}>50% base</span>
              <span style={{ left: '100%', transform: 'translateX(-100%)' }}>85% cap</span>
            </div>
          </div>
          {/* The scale and its bounds render unconditionally, so without this the absent case is
              a fully drawn scale with no marker and nothing saying why. */}
          {rate === null ? (
            <p className="rate-scale-note">No rate to plot until an organisation is resolved.</p>
          ) : null}
        </div>
      </div>

      {/* The check mark is a provenance claim, so it may only appear over a figure actually read
          from the chain. Rendering it unconditionally meant it also vouched for the daemon's
          asserted rate -- and, under the demo stream, for a hardcoded constant. */}
      {rate === null ? (
        <p className="rate-proof-note is-absent">
          No organisation is configured, so no rate has been read for one. The advance rate is per
          organisation, resolved against FloatPool.
        </p>
      ) : mayClaimOnChain(provenance) ? (
        <p className="rate-proof-note">
          <span aria-hidden="true">✓</span> Rate is computed entirely on-chain. No oracle, no
          manual credit score.
        </p>
      ) : (
        <p className="rate-proof-note is-absent">
          This figure is the local daemon&apos;s, not a chain read. Configure an organisation to
          resolve the rate against FloatPool and verify it independently.
        </p>
      )}
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
            <h2 id="open-advances-title">Open advances</h2>
            <p className="float-section-sub">Capital currently deployed against jobs.</p>
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
                    {/* A cell the history scan did not fill says so, at the muted floor with
                        tabular figures off — a blank here is not a zero. */}
                    <td>{openedAt ? relativeTime(openedAt) : <span className="is-absent">not reported</span>}</td>
                    {/* Was a hand-rolled dot-plus-label span, .float-issued, which painted every
                        row in the positive tint. advance.status is AdvanceStatus, so it can also be
                        Repaid or WrittenOff, and a written-off advance rendering green is a lie the
                        markup could tell. Badge keys the tint off the state name, so it cannot. */}
                    <td><Badge kind={advance.status} /></td>
                    <td>
                      {explorerUrl && isSafeExplorerUrl(explorerUrl) ? (
                        <a href={explorerUrl} target="_blank" rel="noreferrer">View ↗</a>
                      ) : (
                        <span className="is-absent">not reported</span>
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

      {/* The loss waterfall as a schedule. The order is the evidence, so it is numbered, and the
          amounts are tabular. No connecting line: the numerals carry the sequence. */}
      <section className="loss-waterfall" aria-labelledby="loss-waterfall-title">
        <div className="float-section-head">
          <div>
            <h2 id="loss-waterfall-title">Loss waterfall</h2>
            <p className="float-section-sub">Absorbs defaults in that order.</p>
          </div>
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
            <strong>Operator bond</strong>
            <small>{losses ? `${formatUsdcExact(losses.bondSlashedUsdc)} USDC` : 'not reported'}</small>
          </li>
          <li>
            <span>2</span>
            <strong>First-loss reserve</strong>
            <small>{losses ? `${formatUsdcExact(losses.reserveUsedUsdc)} USDC` : 'not reported'}</small>
          </li>
          <li>
            <span>3</span>
            <strong>LP capital</strong>
            <small>{losses ? `${formatUsdcExact(losses.socializedUsdc)} USDC` : 'not reported'}</small>
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

  const header = (
    <div className="page-header">
      <div className="page-header-text">
        <h1>Float</h1>
        <p className="page-header-sub">Working capital, priced by delivery history.</p>
      </div>
      {snapshot ? (
        <span className="page-header-aside">
          <span className="float-live-meta">
            <span className={`badge-live${streamStatus !== 'live' ? ' badge-reconnecting' : ''}`}>
              Arc testnet · {streamStatus === 'live' ? 'live' : 'chain polling'}
            </span>
            <small>block {snapshot.blockNumber.toLocaleString('en-US')}</small>
          </span>
        </span>
      ) : null}
    </div>
  );

  if (!snapshot) {
    return (
      <>
        {header}
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

  const used = snapshot.utilizationBps / 10_000;
  /* A cap is a threshold, not an alarm: its tick colours only when the level actually approaches
     it — the same rule the Overview's pool meter follows. */
  const capState = used >= UTILISATION_CAP ? 'at' : used >= UTILISATION_CAP - 0.1 ? 'near' : '';
  // Chain scan only: the H2 stream reports 0 fees (it doesn't compute chain fees), and a
  // non-null 0 masked the honest "Awaiting…" placeholder — an unknown rendered as a
  // measured 0.00. Unknown must read as unknown. (review: chain-authoritative fee path)
  const feesAccrued = snapshot.feesAccruedUsdc ?? null;
  const displayedAdvances = snapshot.openAdvances ?? h2Advances;
  const displayedRate = snapshot.orgRateBps ?? h2Pool?.orgRateBps ?? null;

  return (
    <div className="float-page">
      {header}

      {error ? (
        <p className="float-stale" role="status">
          Latest refresh failed · showing block {snapshot.blockNumber.toLocaleString('en-US')}
        </p>
      ) : null}

      {/* The capital panel is the same meter the Overview draws, at the same semantics: fill is
          the share of the pool lent out, the tick is the contract's 80% cap. */}
      <section className="pool" aria-labelledby="pool-capital-title">
        <div className="pool-head">
          <div>
            <h2 className="pool-title" id="pool-capital-title">Pool capital</h2>
            <p className="pool-sub">Read live from FloatPool on Arc testnet</p>
          </div>
          <div className="pool-figure">
            <span className="pool-tvl">{formatUsdcExact(snapshot.totalAssetsUsdc)}</span>
            <span className="pool-unit">USDC</span>
          </div>
        </div>
        <div
          className="pool-meter"
          role="img"
          aria-label={
            `Pool holding ${formatUsdcExact(snapshot.totalAssetsUsdc)} USDC, of which ` +
            `${(used * 100).toFixed(1)} percent is lent out. Lending is capped at 80 percent ` +
            'of the pool.'
          }
        >
          <div className="pool-track">
            {used > 0 ? (
              <div className="pool-fill" style={{ width: `${Math.min(100, used * 100)}%` }} />
            ) : null}
            <span className={`pool-tick${capState ? ` is-${capState}` : ''}`} style={{ left: '80%' }} />
          </div>
          <div className="pool-scale">
            <span
              style={{ left: '80%', transform: 'translateX(-100%)' }}
              className={capState ? 'is-at-cap' : undefined}
            >
              80% utilisation cap
            </span>
          </div>
          <p className="pool-meter-note">
            {formatUsdcExact(snapshot.totalOutstandingUsdc)} USDC deployed ·{' '}
            {formatBps(snapshot.utilizationBps)} of the pool ·{' '}
            <a className="float-proof-link" href={snapshot.explorerUrl} target="_blank" rel="noreferrer">
              Verify on the explorer ↗
            </a>
          </p>
        </div>
      </section>

      <div className="float-metrics">
        <MetricModule
          label="Available liquidity"
          value={<>{formatUsdcExact(snapshot.availableLiquidityUsdc)} <span className="u">USDC</span></>}
          note="LP capital ready to deploy"
        />
        <MetricModule
          label="Fees earned"
          absent={feesAccrued === null}
          value={feesAccrued === null ? 'not reported' : <>{formatUsdcExact(feesAccrued)} <span className="u">USDC</span></>}
          note={feesAccrued === null ? 'Awaiting H2 or private RPC history' : 'Cumulative AdvanceRepaid fees'}
        />
        <MetricModule
          label="First-loss reserve"
          value={<>{formatUsdcExact(snapshot.reserveUsdc)} <span className="u">USDC</span></>}
          note="20% of fees retained"
        />
      </div>

      <p className="float-accounting-note">
        LP-owned capital excludes the reserve. Values observed at Arc block {snapshot.blockNumber.toLocaleString('en-US')}.
        {/* One caption cannot honestly cover both when they are read at different blocks. The
            view figures are always at blockNumber; history comes from a cache whose scan may
            lag, and the two are the same quantity in places (totalOutstanding is the sum of open
            principals, FloatPool.sol), so a silent lag reads as a contradiction on screen. */}
        {snapshot.historyStatus === 'pending' && snapshot.historyScannedThroughBlock !== null ? (
          <> History below is scanned through block{' '}
            {snapshot.historyScannedThroughBlock.toLocaleString('en-US')}
            {snapshot.historyScannedThroughBlock < snapshot.blockNumber
              ? ' and is still catching up.'
              : /* The cache can also be scanned PAST this response's head, because a concurrent
                   request extended it after these views were read. Both directions are mixed
                   observations and both read pending, but only one of them is the history lagging,
                   so the copy must not claim it is. */
                ', so the figures above are the older of the two.'}</>
        ) : null}
      </p>

      <RateEngine snapshot={snapshot} fallbackRateBps={displayedRate} />
      <OpenAdvances advances={displayedAdvances} losses={snapshot.losses} />
    </div>
  );
}
