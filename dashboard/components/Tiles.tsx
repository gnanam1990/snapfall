'use client';

/**
 * One visual idea per overview tile, each drawn from what the tile measures. Every figure comes from
 * the caller (chain reads); these components only draw it, and each carries a text equivalent.
 */

/** The pool as a vessel: the liquid is the share that is free to lend, the empty part is lent out. */
export function PoolLevel({ freeShare }: { freeShare: number | null }) {
  const level = freeShare === null ? 0 : Math.max(0, Math.min(1, freeShare));
  const top = 100 - level * 100;
  return (
    <svg className="pool-level" viewBox="0 0 400 100" preserveAspectRatio="none" aria-hidden="true">
      <path
        className="pool-liquid"
        d={`M0 ${top + 3} C 60 ${top - 3}, 120 ${top + 6}, 200 ${top + 2} S 340 ${top - 4}, 400 ${top + 2} V 100 H 0 Z`}
      />
      <path className="pool-surface" d={`M0 ${top + 3} C 60 ${top - 3}, 120 ${top + 6}, 200 ${top + 2} S 340 ${top - 4}, 400 ${top + 2}`} />
    </svg>
  );
}

/** Semicircle gauge from the 30% floor to the 85% cap, a tick at the 50% start, needle at the rate. */
export function RateGauge({ rateBps }: { rateBps: number | null }) {
  const lo = 3000;
  const hi = 8500;
  const k = rateBps === null ? 0 : Math.max(0, Math.min(1, (rateBps - lo) / (hi - lo)));
  const cx = 100;
  const cy = 96;
  const r = 78;
  const pt = (t: number, rr = r) => {
    const a = Math.PI * (1 - t);
    return [cx + rr * Math.cos(a), cy - rr * Math.sin(a)];
  };
  const [ex, ey] = pt(k);
  const [nx, ny] = pt(k, r - 18);
  const [sx, sy] = pt((5000 - lo) / (hi - lo), r + 8);
  const [sx2, sy2] = pt((5000 - lo) / (hi - lo), r - 8);
  return (
    <svg className="gauge" viewBox="0 0 200 112" role="img" aria-label={`Advance rate ${rateBps === null ? 'unknown' : `${rateBps / 100}%`}, between a 30% floor and an 85% cap.`}>
      <path className="gauge-track" d={`M ${cx - r} ${cy} A ${r} ${r} 0 0 1 ${cx + r} ${cy}`} />
      {rateBps !== null ? <path className="gauge-fill" d={`M ${cx - r} ${cy} A ${r} ${r} 0 0 1 ${ex} ${ey}`} /> : null}
      <line className="gauge-start" x1={sx} y1={sy} x2={sx2} y2={sy2} />
      {rateBps !== null ? <line className="gauge-needle" x1={cx} y1={cy} x2={nx} y2={ny} /> : null}
      <circle className="gauge-hub" cx={cx} cy={cy} r={6} />
      <text className="gauge-tick" x={cx - r} y={cy + 14} textAnchor="middle">30%</text>
      <text className="gauge-tick" x={cx + r} y={cy + 14} textAnchor="middle">85%</text>
    </svg>
  );
}

/** One column per repaid advance's fee, oldest to newest. */
export function FeeBars({ fees }: { fees: number[] }) {
  if (!fees.length) return null;
  const max = Math.max(...fees);
  return (
    <div className="fee-bars" role="img" aria-label={`Fees from each repaid advance: ${fees.map((f) => f.toFixed(4)).join(', ')} USDC`}>
      {fees.map((f, i) => (
        <span key={i} style={{ height: `${Math.max(8, (f / max) * 100)}%`, ['--i' as string]: i }} title={`${f.toFixed(4)} USDC`} />
      ))}
    </div>
  );
}
