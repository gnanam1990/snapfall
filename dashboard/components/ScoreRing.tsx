'use client';

import { useEffect, useRef, useState } from 'react';
import { formatBps, formatUsdc, parseAtomicUsdc } from '@/lib/format';

/**
 * F2 Snapfall Score Ring (V11). The advance rate rendered as a felt reward: the number
 * eases 50% -> 55% on the flywheel event and the ring bumps. Only the percentage lives
 * inside the circle; labels sit below it, so nothing can clip.
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
    let bumpTimer: ReturnType<typeof setTimeout> | undefined;
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / dur);
      const eased = 1 - Math.pow(1 - p, 3);
      setDisplayBps(Math.round(from + (to - from) * eased));
      if (p < 1) raf = requestAnimationFrame(tick);
      else bumpTimer = setTimeout(() => setBump(false), 500);
    };
    raf = requestAnimationFrame(tick);
    // Clear the pending bump-reset too, so a rate change mid-cycle cannot cancel
    // the NEXT animation's bump early (review: PR #9).
    return () => {
      cancelAnimationFrame(raf);
      if (bumpTimer) clearTimeout(bumpTimer);
    };
  }, [rateBps]);

  const FLOOR = 3000;
  const CAP = 8500;
  const frac = Math.max(0, Math.min(1, (displayBps - FLOOR) / (CAP - FLOOR)));
  const R = 52;
  const CIRC = 2 * Math.PI * R;
  const dash = CIRC * frac;

  // The stream frame is JSON-validated but not shape-validated: a malformed or
  // decimal price must omit the hint, never throw during render (review: PR #9).
  const price = jobPriceUsdc !== undefined ? parseAtomicUsdc(jobPriceUsdc) : null;
  const unlock = price !== null ? formatUsdc((price * BigInt(displayBps)) / 10_000n) : null;

  return (
    <div className="ring-wrap">
      <div className={`ring-figure${bump ? ' bump' : ''}`}>
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
          <span className="ring-pct">{formatBps(displayBps)}</span>
        </div>
      </div>
      <div className="ring-cap">Advance rate</div>
      {unlock ? (
        <div className="ring-hint">
          next job unlocks <b>{unlock}</b>
        </div>
      ) : null}
    </div>
  );
}
