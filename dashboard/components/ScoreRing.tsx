'use client';

import { useEffect, useRef, useState } from 'react';
import { formatBps, formatUsdc } from '@/lib/format';

/**
 * F2 Snapfall Score Ring (V11, PRD §8). The advance rate as a felt reward: the number eases
 * toward its new value on the flywheel beat and the ring bumps once. Only the percentage sits
 * inside the circle; the label and the unlock line are normal-flow captions below it, so
 * nothing can clip at any font size.
 *
 * Null-tolerant like the rest of the Overview: with no chain read yet, it renders an explicit
 * placeholder instead of implying a rate we do not have.
 */
export default function ScoreRing({
  rateBps,
  jobPriceUsdc,
}: {
  rateBps: number | null;
  /** Atomic USDC of the reference job, for the "next job unlocks" line. */
  jobPriceUsdc?: string | null;
}) {
  const [displayBps, setDisplayBps] = useState(rateBps ?? 0);
  const [bump, setBump] = useState(false);
  const prev = useRef(rateBps);
  // The animation must ease from what is CURRENTLY on screen, not from the previous target.
  // Reading state inside the effect would stale-close over it, so mirror it in a ref
  // (review: PR #40 — rapid updates used to jump).
  const shown = useRef(rateBps ?? 0);
  shown.current = displayBps;

  useEffect(() => {
    if (rateBps === null || prev.current === rateBps) {
      prev.current = rateBps;
      if (rateBps !== null) setDisplayBps(rateBps);
      return;
    }
    const to = rateBps;
    prev.current = rateBps;

    // Reduced motion: land the value immediately and never enter the bump state, so it
    // cannot be left stuck by a cancelled animation.
    if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
      setDisplayBps(to);
      setBump(false);
      return;
    }

    const from = shown.current;
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

    // Safety net: requestAnimationFrame is PAUSED for a hidden or backgrounded page, so on a
    // tab the owner is not looking at, the easing above never runs and the ring would stay
    // frozen on the old rate even after they come back. setTimeout is only throttled, never
    // paused, so it lands the final value regardless. Idempotent when the easing did run.
    const settle = setTimeout(() => setDisplayBps(to), dur + 60);

    // Clear the pending bump reset too: a rate change mid-cycle must not cancel the next
    // animation's bump early (review finding on the original PR #9).
    return () => {
      cancelAnimationFrame(raf);
      clearTimeout(settle);
      if (bumpTimer) clearTimeout(bumpTimer);
      // An interrupted cycle must not leave the bump class applied forever; the next cycle
      // sets it again on its own.
      setBump(false);
    };
  }, [rateBps]);

  // The frozen contract's clamp: rate(org) ∈ [30%, 85%].
  const FLOOR = 3000;
  const CAP = 8500;
  const R = 52;
  const CIRC = 2 * Math.PI * R;
  const known = rateBps !== null;
  const frac = known ? Math.max(0, Math.min(1, (displayBps - FLOOR) / (CAP - FLOOR))) : 0;
  const dash = CIRC * frac;

  const unlock =
    known && jobPriceUsdc && /^\d+$/.test(jobPriceUsdc)
      ? formatUsdc((BigInt(jobPriceUsdc) * BigInt(displayBps)) / 10_000n)
      : null;

  return (
    <div className="ring">
      <div className={`ring-figure${bump ? ' is-bumping' : ''}`}>
        <svg viewBox="0 0 120 120" className="ring-svg" aria-hidden="true">
          <circle cx="60" cy="60" r={R} className="ring-track" />
          {known ? (
            <circle
              cx="60"
              cy="60"
              r={R}
              className="ring-arc"
              strokeDasharray={`${dash} ${CIRC - dash}`}
              transform="rotate(-90 60 60)"
            />
          ) : null}
        </svg>
        <div className="ring-center">
          <span className="ring-pct">{known ? formatBps(displayBps) : '—'}</span>
        </div>
      </div>
      <div className="ring-cap">Advance rate</div>
      <div className="ring-hint">
        {known ? (
          unlock ? (
            <>
              next job unlocks <b>{unlock}</b>
            </>
          ) : (
            'earned from delivery history'
          )
        ) : (
          'awaiting chain indexer'
        )}
      </div>
    </div>
  );
}
