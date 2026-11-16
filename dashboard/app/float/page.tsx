'use client';

import Link from 'next/link';
import { useState } from 'react';
import { useChainHistory, useFloat } from '@/lib/useChain';
import { networkLabel } from '@/lib/network';
import { AddrLink, Band, ErrorNotice, Freshness, TxLink, pct, shortDate, shortId, usdc } from '@/components/ui';

/**
 * Step chart of the advance rate across its RateChanged history. One series, so the heading names
 * it and there is no legend; each point answers hover and focus with its value and date.
 */
function RateChart({ points }: { points: { rateBps: number; at: number | null }[] }) {
  const series = [{ rateBps: 5000, at: null as number | null, label: 'Starting rate' }, ...points.map((p) => ({ ...p, label: shortDate(p.at) }))];
  const [hover, setHover] = useState<number | null>(null);
  const W = 560;
  const H = 200;
  const pad = { l: 44, r: 20, t: 18, b: 30 };
  const lo = 3000;
  const hi = 8500;
  const x = (i: number) => pad.l + (series.length === 1 ? 0 : (i / (series.length - 1)) * (W - pad.l - pad.r));
  const y = (bps: number) => pad.t + (1 - (bps - lo) / (hi - lo)) * (H - pad.t - pad.b);
  let d = `M ${x(0)} ${y(series[0].rateBps)}`;
  for (let i = 1; i < series.length; i += 1) d += ` H ${x(i)} V ${y(series[i].rateBps)}`;
  const tip = hover === null ? null : series[hover];
  return (
    <svg className="chart" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Advance rate over time: ${series.map((p) => `${p.label} ${pct(p.rateBps)}`).join(', ')}`}>
      {[3000, 5000, 7000, 8500].map((v) => (
        <g key={v}>
          <line className="chart-grid" x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} />
          <text className="chart-axis" x={pad.l - 10} y={y(v) + 4} textAnchor="end">{v / 100}%</text>
        </g>
      ))}
      <path className="chart-line" d={d} />
      {series.map((p, i) => (
        <g key={i} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} onFocus={() => setHover(i)} onBlur={() => setHover(null)} tabIndex={0} aria-label={`${p.label}: ${pct(p.rateBps)}`}>
          <circle className="chart-hit" cx={x(i)} cy={y(p.rateBps)} r={14} />
          <circle className="chart-dot" cx={x(i)} cy={y(p.rateBps)} r={hover === i ? 6 : 4} />
        </g>
      ))}
      {tip && hover !== null ? (
        <text className="chart-tip" x={Math.min(Math.max(x(hover), pad.l + 40), W - pad.r - 40)} y={y(tip.rateBps) - 14} textAnchor="middle">
          {pct(tip.rateBps)}, {tip.label}
        </text>
      ) : null}
      <text className="chart-axis" x={x(0)} y={H - 8}>Start</text>
      <text className="chart-axis" x={W - pad.r} y={H - 8} textAnchor="end">Now</text>
    </svg>
  );
}

/** The capital pool: current state by eth_call against FloatPool, history from its events. */
export default function CapitalPoolPage() {
  const float = useFloat();
  const history = useChainHistory();
  const f = float.data;
  const h = history.data;
  const t = h?.totals;
  const net = networkLabel();
  const lent = f?.utilizationBps ?? null;
  const rate = f?.orgRateBps ?? t?.latestRateBps ?? null;
  const advances = (h?.jobs ?? []).filter((j) => j.advance).sort((a, b) => (b.lastAt ?? 0) - (a.lastAt ?? 0));
  const lp = (h?.activity ?? []).filter((a) => a.kind === 'pool.deposit' || a.kind === 'pool.withdraw');

  return (
    <>
      <Band
        title="Capital pool"
        lede={f ? `The pool fronts ${pct(rate)} of a job the moment it starts.` : float.error ? 'The pool could not be read just now.' : 'Reading the pool…'}
        figs={f ? [
          { label: 'In the pool', value: usdc(f.totalAssetsUsdc), unit: 'USDC' },
          { label: 'Out with agents', value: usdc(f.totalOutstandingUsdc), unit: 'USDC' },
          { label: 'Lent out', value: pct(lent, 1) },
        ] : undefined}
        meta={<Freshness updatedAt={float.updatedAt} error={float.error} network={net} />}
      />

      <div className="wrap content">
      {float.error && !f ? <div className="section"><ErrorNotice message={`The pool could not be read: ${float.error}`} onRetry={float.refresh} /></div> : null}

      <div className="section split">
        <section aria-labelledby="lent-h">
          <div className="section-head">
            <div>
              <h2 id="lent-h">How much is lent out</h2>
            </div>
          </div>
          <div className="panel panel-pad panel-col">
          <div className="meter" role="img" aria-label={`${pct(lent, 1)} lent out. Limits: 10% to any one operator, 80% in total.`}>
            <div className="meter-fill" style={{ width: `${Math.min(100, (lent ?? 0) / 100)}%` }} />
            <span className="meter-cap" style={{ left: '10%' }} />
            <span className="meter-cap" style={{ left: '80%' }} />
          </div>
          <div className="meter-labels">
            <span style={{ left: '10%', transform: 'translateX(-50%)' }}>10% per operator</span>
            <span style={{ left: '80%', transform: 'translateX(-100%)' }}>80% in total</span>
          </div>
          <dl className="figs">
            <div><dt>Ready to lend</dt><dd>{f ? usdc(f.availableLiquidityUsdc) : '—'}<small>USDC</small></dd></div>
            <div><dt>Loss reserve</dt><dd>{f ? usdc(f.reserveUsdc, 4) : '—'}<small>USDC</small></dd></div>
            <div><dt>Fees earned</dt><dd>{t ? usdc(t.feesEarnedUsdc, 4) : '—'}<small>USDC</small></dd></div>
            <div><dt>Advances repaid</dt><dd>{t ? `${t.advancesRepaid} of ${t.advancesIssued}` : '—'}</dd></div>
          </dl>
          {f ? (
            <p className="cell-sub panel-foot">
              Read from FloatPool <a className="addr" href={f.explorerUrl} target="_blank" rel="noreferrer">{shortId(f.poolAddress)}</a> at block {f.blockNumber.toLocaleString('en-US')}.
            </p>
          ) : null}
          </div>
        </section>

        <section aria-labelledby="rate-h">
          <div className="section-head">
            <div>
              <h2 id="rate-h">Advance rate</h2>
            </div>
          </div>
          <div className="panel panel-pad panel-col">
            {h ? <RateChart points={h.rateHistory} /> : <div className="empty">Reading the chain…</div>}
            <p className="cell-sub panel-foot">50% start · +5 per settled job · −15 per write-off · 30–85%</p>
          </div>
        </section>
      </div>

      <section className="section" aria-labelledby="adv-h">
        <div className="section-head">
          <div>
            <h2 id="adv-h">Advances</h2>
          </div>
        </div>
        <div className="panel">
          {!h ? (
            <div className="empty">Reading the chain…</div>
          ) : advances.length === 0 ? (
            <div className="empty">No advance yet. The pool advances capital once a paid job starts.</div>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr><th>Job</th><th className="hide-md">Operator</th><th className="r">Advanced</th><th className="r hide-sm">Fee</th><th className="r">Rate</th><th>Status</th><th>Proof</th></tr>
                </thead>
                <tbody>
                  {advances.map((j) => (
                    <tr key={`${j.jobId}:${j.advance!.tx}`}>
                      <td><Link className="mono" href={`/jobs/${j.jobId}`}>{shortId(j.jobId, 8, 4)}</Link><span className="cell-sub">{shortDate(j.lastAt)}</span></td>
                      <td className="hide-md"><AddrLink explorer={h.explorerUrl} address={j.operator} /></td>
                      <td className="r"><span className="amount">{usdc(j.advance!.principalUsdc)}</span></td>
                      <td className="r hide-sm">{usdc(j.advance!.feeUsdc, 4)}</td>
                      <td className="r">{pct(j.advance!.rateBps)}</td>
                      <td><span className={`state ${j.advance!.status === 'Repaid' ? 'is-good' : j.advance!.status === 'Open' ? 'is-live' : 'is-bad'}`}>{j.advance!.status === 'Open' ? 'Out now' : j.advance!.status === 'Repaid' ? 'Repaid' : 'Written off'}</span></td>
                      <td><TxLink explorer={h.explorerUrl} tx={j.advance!.tx}>View</TxLink></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </section>

      <section className="section" aria-labelledby="lp-h">
        <div className="section-head">
          <div>
            <h2 id="lp-h">Deposits and withdrawals</h2>
          </div>
        </div>
        <div className="panel">
          {!h ? <div className="empty">Reading the chain…</div> : lp.length === 0 ? <div className="empty">No deposit yet.</div> : (
            <table className="table">
              <tbody>
                {lp.map((a) => (
                  <tr key={a.id}>
                    <td>{a.kind === 'pool.deposit' ? 'Deposit' : 'Withdrawal'}<span className="cell-sub">{shortDate(a.at)}</span></td>
                    <td className="hide-sm cell-sub" style={{ marginTop: 0 }}>{a.text}</td>
                    <td className="r"><span className="amount">{a.kind === 'pool.deposit' ? '+' : '−'}{usdc(a.amountUsdc)}<small>USDC</small></span></td>
                    <td><TxLink explorer={h.explorerUrl} tx={a.tx}>View</TxLink></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </section>
      </div>
    </>
  );
}
