'use client';

import { useEffect, useRef, useState } from 'react';
import { formatBps, formatUsdc } from '@/lib/format';

/**
 * F2 Snapfall Score Ring (V11). The advance rate rendered as a felt reward: the number
 * eases 50% -> 55% on the flywheel event, the ring bumps, and a "next job unlocks N" line
 * makes the abstract on-chain rate concrete. Value is read from chain via the SSE stream.
 */
export default function ScoreRing({
  rateBps,
  jobPriceUsdc,
}: {
  rateBps: number;
  /** Atomic USDC of the reference job, for the "next job unlocks" line. */
  jobPriceUsdc?: string;
}) {
  const [displayBps, setDisplayBps] = useState(rateBps);
  const [bump, setBump] = useState(false);
  const prev = useRef(rateBps);

  useEffect(() => {
    if (prev.current === rateBps) return;
    const from = prev.current;
    const to = rateBps;
    prev.current = rateBps;
    setBump(true);
    const start = performance.now();
    const dur = 900;
    let raf = 0;
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / dur);
      const eased = 1 - Math.pow(1 - p, 3);
      setDisplayBps(Math.round(from + (to - from) * eased));
      if (p < 1) raf = requestAnimationFrame(tick);
      else setTimeout(() => setBump(false), 500);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [rateBps]);

  const FLOOR = 3000;
  const CAP = 8500;
  const frac = Math.max(0, Math.min(1, (displayBps - FLOOR) / (CAP - FLOOR)));
  const R = 46;
  const CIRC = 2 * Math.PI * R;
  const dash = CIRC * frac;

  const unlock = jobPriceUsdc ? formatUsdc((BigInt(jobPriceUsdc) * BigInt(displayBps)) / 10_000n) : null;

  return (
    <div className={`ring-wrap${bump ? ' bump' : ''}`}>
      <svg viewBox="0 0 120 120" className="ring-svg" aria-hidden="true">
        <circle cx="60" cy="60" r={R} className="ring-track" />
        <circle
          cx="60"
          cy="60"
          r={R}
          className="ring-arc"
          strokeDasharray={`${dash} ${CIRC - dash}`}
          strokeDashoffset={0}
          transform="rotate(-90 60 60)"
        />
      </svg>
      <div className="ring-center">
        <div className="ring-pct">{formatBps(displayBps)}</div>
        <div className="ring-lbl">advance&nbsp;rate</div>
      </div>
      {unlock ? <div className="ring-hint">next job unlocks <b>{unlock}</b></div> : null}
    </div>
  );
}
