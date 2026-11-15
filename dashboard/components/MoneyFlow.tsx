'use client';

import { useEffect, useState } from 'react';

/**
 * The page's one moving picture: real money drawn as a flow.
 *
 * Customers' payments run into escrow, then fall out in the order the contract enforces — back to
 * the capital pool first, the rest to the operator — while the pool's own capital snaps forward to
 * the agents as advances. Stream width and the number of particles follow the real amounts, so a
 * thin stream is a small figure, not a style choice. With reduced motion the streams stay and the
 * particles are not drawn.
 */

function usePrefersReducedMotion(): boolean {
  const [reduce, setReduce] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    setReduce(mq.matches);
    const on = () => setReduce(mq.matches);
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  }, []);
  return reduce;
}

const fmt = (v: number) => (Math.floor(v * 100 + 1e-9) / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });

function Particles({ path, count, dur, cls }: { path: string; count: number; dur: number; cls: string }) {
  return (
    <>
      {Array.from({ length: count }, (_, i) => (
        <circle key={i} r={3.2} className={cls}>
          {/* A negative begin starts each particle already part-way along its path, so none waits at the origin. */}
          <animateMotion dur={`${dur}s`} repeatCount="indefinite" begin={`-${((i * dur) / count).toFixed(2)}s`} rotate="auto">
            <mpath href={`#${path}`} />
          </animateMotion>
        </circle>
      ))}
    </>
  );
}

export default function MoneyFlow({ paid, pool, operator, advanced }: { paid: number; pool: number; operator: number; advanced: number }) {
  const reduce = usePrefersReducedMotion();
  const total = Math.max(paid, pool + operator, 0.000001);
  const w = (v: number) => 2 + (14 * v) / total;
  const n = (v: number) => Math.max(2, Math.round((6 * v) / total));

  return (
    <svg className="flow" viewBox="0 0 480 270" role="img" aria-label={`Customers paid ${fmt(paid)} USDC into escrow. ${fmt(pool)} went back to the capital pool first and the operator kept ${fmt(operator)}. The pool advanced ${fmt(advanced)} USDC to the agents along the way.`}>
      <defs>
        <path id="flow-in" d="M 104 135 C 140 135 160 135 196 135" />
        <path id="flow-pool" d="M 284 135 C 330 135 336 62 372 62" />
        <path id="flow-op" d="M 284 135 C 330 135 336 208 372 208" />
        <path id="flow-snap" d="M 430 88 C 462 118 462 152 430 182" />
      </defs>

      {/* streams */}
      <use href="#flow-in" className="flow-stream flow-in" style={{ strokeWidth: w(paid) }} />
      <use href="#flow-pool" className="flow-stream flow-pool" style={{ strokeWidth: w(pool) }} />
      <use href="#flow-op" className="flow-stream flow-op" style={{ strokeWidth: w(operator) }} />
      <use href="#flow-snap" className="flow-snap" />

      {!reduce ? (
        <g aria-hidden="true">
          <Particles path="flow-in" count={n(paid)} dur={2.4} cls="flow-dot flow-dot-in" />
          <Particles path="flow-pool" count={n(pool)} dur={2.8} cls="flow-dot flow-dot-pool" />
          <Particles path="flow-op" count={n(operator)} dur={2.8} cls="flow-dot flow-dot-op" />
          <Particles path="flow-snap" count={2} dur={2} cls="flow-dot flow-dot-snap" />
        </g>
      ) : null}

      {/* nodes */}
      <g className="flow-node">
        <rect x={8} y={104} width={96} height={62} rx={12} />
        <text x={56} y={128} className="flow-label" textAnchor="middle">Customers</text>
        <text x={56} y={150} className="flow-value" textAnchor="middle">{fmt(paid)}</text>
      </g>
      <g className="flow-node flow-node-core">
        <rect x={196} y={98} width={88} height={74} rx={14} />
        <text x={240} y={126} className="flow-label" textAnchor="middle">Escrow</text>
        <text x={240} y={146} className="flow-sub" textAnchor="middle">splits on</text>
        <text x={240} y={160} className="flow-sub" textAnchor="middle">acceptance</text>
      </g>
      <g className="flow-node">
        <rect x={372} y={36} width={100} height={52} rx={12} />
        <text x={422} y={56} className="flow-label" textAnchor="middle">Pool, first</text>
        <text x={422} y={76} className="flow-value flow-value-pool" textAnchor="middle">{fmt(pool)}</text>
      </g>
      <g className="flow-node">
        <rect x={372} y={182} width={100} height={52} rx={12} />
        <text x={422} y={202} className="flow-label" textAnchor="middle">Operator</text>
        <text x={422} y={222} className="flow-value flow-value-op" textAnchor="middle">{fmt(operator)}</text>
      </g>
      <text x={444} y={139} className="flow-sub flow-snap-label" textAnchor="end">advanced {fmt(advanced)}</text>
      <text x={240} y={262} className="flow-sub" textAnchor="middle">All settled jobs, USDC</text>
    </svg>
  );
}
