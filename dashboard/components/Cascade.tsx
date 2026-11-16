'use client';

import Link from 'next/link';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ChainJob } from '@/lib/chainHistory';
import MoneyFlow from '@/components/MoneyFlow';
import { shortId, timeAgo, toNumber, usdc } from '@/components/ui';
import type { Anchors, CascadeData, CascadeHandle, PartName, StageMode } from '@/components/cascade/scene';

/**
 * The overview's one moving picture: the Snapfall itself.
 *
 * Every word and figure on it is HTML over the canvas, so it is readable, selectable and announced
 * whether or not WebGL draws. The canvas is decoration that follows the data; the legend buttons
 * select the same parts a click on the scene selects, so the keyboard has the same path.
 *
 * Dragging the stage turns the fountain; the reset button (or a double-click) brings it home.
 *
 * With reduced motion the scene renders one still frame and the replay waits for Next. When WebGL
 * cannot start, or its context is lost, the 2D money flow takes its place.
 */

function useReducedMotion(): boolean {
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

const PARTS: { key: PartName; name: string }[] = [
  { key: 'escrow', name: 'Paid' },
  { key: 'pool', name: 'Pool, first' },
  { key: 'operator', name: 'Operator' },
];

const LABEL: Record<PartName, string> = { escrow: 'Customers paid', pool: 'Pool repaid', operator: 'Operator kept' };

/** The stage follows the page theme: daylight on the light theme, night on the dark one. */
function stageMode(): StageMode {
  return document.documentElement.dataset.theme === 'dark' ? 'night' : 'day';
}

export default function Cascade({ jobs, now }: { jobs: ChainJob[]; now: number }) {
  const settled = useMemo(
    () =>
      jobs
        .filter((j) => j.settlement)
        .sort((a, b) => (a.lastAt ?? 0) - (b.lastAt ?? 0)),
    [jobs],
  );
  const data: CascadeData = useMemo(() => {
    const js = settled.map((j) => ({
      id: j.jobId,
      paid: toNumber(j.paymentUsdc),
      pool: toNumber(j.settlement!.advanceRepaidUsdc),
      op: toNumber(j.settlement!.operatorNetUsdc),
    }));
    return {
      jobs: js,
      paid: js.reduce((s, j) => s + j.paid, 0),
      pool: js.reduce((s, j) => s + j.pool, 0),
      op: js.reduce((s, j) => s + j.op, 0),
    };
  }, [settled]);
  const totals = useMemo(() => {
    let paid = 0n, pool = 0n, op = 0n;
    for (const j of settled) {
      paid += BigInt(j.paymentUsdc);
      pool += BigInt(j.settlement!.advanceRepaidUsdc);
      op += BigInt(j.settlement!.operatorNetUsdc);
    }
    return { paid, pool, op };
  }, [settled]);

  const reduced = useReducedMotion();
  const stageRef = useRef<HTMLDivElement>(null);
  const handleRef = useRef<CascadeHandle | null>(null);
  const legendRef = useRef<HTMLDivElement>(null);
  const labelRefs = useRef<Record<PartName, HTMLDivElement | null>>({ escrow: null, pool: null, operator: null });
  const [failed, setFailed] = useState(false);
  const [ready, setReady] = useState(false);
  const [paused, setPaused] = useState(false);
  const [jobIdx, setJobIdx] = useState(settled.length ? settled.length - 1 : 0);
  const [selected, setSelected] = useState<PartName | null>(null);
  const [turned, setTurned] = useState(false);
  const dataRef = useRef(data);
  dataRef.current = data;

  // Mount once; three.js loads here, after first paint, and never on the server.
  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    let cancelled = false;
    import('@/components/cascade/scene')
      .then(({ mountCascade }) => {
        if (cancelled) return;
        handleRef.current = mountCascade(el, {
          data: dataRef.current,
          reduced: window.matchMedia('(prefers-reduced-motion: reduce)').matches,
          mode: stageMode(),
          modelUrl: '/cascade/fountain.glb',
          onFrame: (a: Anchors) => {
            const width = el.clientWidth;
            const placed: { node: HTMLDivElement; x: number; y: number; half: number; top: number; h: number; on: boolean }[] = [];
            for (const k of Object.keys(a) as PartName[]) {
              const node = labelRefs.current[k];
              if (!node) continue;
              // Keep the whole label inside the stage: on a 320px phone the escrow shelf sits at
              // the very edge and its label would otherwise be cut in half.
              const half = node.offsetWidth / 2 + 8;
              const x = Math.min(Math.max(a[k].x, half), Math.max(half, width - half));
              // A turned stage can carry a label up under the legend; keep it just below instead.
              let y = a[k].y;
              const legend = legendRef.current;
              if (legend) {
                const lx = legend.offsetLeft, lw = legend.offsetWidth;
                const overlapsX = x + half - 8 > lx && x - half + 8 < lx + lw;
                const floor = legend.offsetTop + legend.offsetHeight + 6 - node.offsetTop;
                if (overlapsX && y < floor) y = floor;
              }
              placed.push({ node, x, y, half: half - 8, top: node.offsetTop, h: node.offsetHeight, on: a[k].visible });
            }
            // Seen from the front the three bowls line up, and so would their labels: top to
            // bottom, push each shown label clear of any above it that it would cover.
            const shown = placed.filter((p) => p.on).sort((p, q) => p.y - q.y);
            for (let i = 1; i < shown.length; i++)
              for (let j = 0; j < i; j++) {
                const p = shown[j]!, q = shown[i]!;
                if (Math.abs(p.x - q.x) >= p.half + q.half + 4) continue;
                const pBottom = p.y + p.top + p.h + 4;
                if (q.y + q.top < pBottom) q.y = pBottom - q.top;
              }
            for (const p of placed) {
              p.node.style.transform = `translate(${Math.round(p.x)}px, ${Math.round(p.y)}px)`;
              p.node.style.opacity = p.on ? '1' : '0';
            }
          },
          onJob: setJobIdx,
          onPick: setSelected,
          onFail: () => setFailed(true),
          onTurn: setTurned,
        });
        setReady(true);
      })
      .catch(() => setFailed(true));
    // The theme toggle rewrites data-theme on <html>; the scene re-lights to match.
    const mo = new MutationObserver(() => handleRef.current?.setMode(stageMode()));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => {
      cancelled = true;
      mo.disconnect();
      handleRef.current?.dispose();
      handleRef.current = null;
    };
  }, []);

  useEffect(() => handleRef.current?.setData(data), [data, ready]);
  useEffect(() => handleRef.current?.setReduced(reduced), [reduced, ready]);
  useEffect(() => handleRef.current?.setPaused(paused), [paused, ready]);
  useEffect(() => handleRef.current?.setSelected(selected), [selected, ready]);
  // Start the replay on the newest settlement once the scene is up.
  useEffect(() => {
    if (ready && settled.length) handleRef.current?.showJob(settled.length - 1);
  }, [ready, settled.length]);

  const current = settled[Math.min(jobIdx, Math.max(0, settled.length - 1))];
  const figure = { escrow: totals.paid, pool: totals.pool, operator: totals.op } as const;

  if (failed) {
    return (
      <div className="cascade cascade-fallback">
        {data.paid > 0 ? (
          <MoneyFlow paid={data.paid} pool={data.pool} operator={data.op} advanced={data.pool} />
        ) : (
          <p className="cascade-empty">No job has settled yet.</p>
        )}
      </div>
    );
  }

  const has = settled.length > 0;
  const step = (d: number) => {
    if (!settled.length) return;
    const next = (jobIdx + d + settled.length) % settled.length;
    handleRef.current?.showJob(next);
    setJobIdx(next);
  };

  const resetBtn = turned ? (
    <button type="button" className="icon-btn cascade-reset" onClick={() => handleRef.current?.resetView()} aria-label="Reset the view">
      <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M4.5 10a5.5 5.5 0 1 0 1.8-4.1M4.5 3.5v3h3" /></svg>
    </button>
  ) : null;

  return (
    <div className={`cascade${ready ? ' is-ready' : ''}${selected ? ` sel-${selected}` : ''}`}>
      <div ref={stageRef} className="cascade-stage" />

      {has
        ? PARTS.map((p) => (
            <div
              key={p.key}
              ref={(n) => {
                labelRefs.current[p.key] = n;
              }}
              className={`cascade-label is-${p.key}${selected === p.key ? ' is-on' : ''}`}
            >
              <span className="cascade-label-name">{LABEL[p.key]}</span>
              <span className="cascade-label-fig">{usdc(figure[p.key])}</span>
            </div>
          ))
        : null}

      {/* No caption when nothing has settled: the hero sentence beside the stage already says so. */}

      <div ref={legendRef} className="cascade-legend" role="group" aria-label="Show one part of the cascade">
        {PARTS.map((p) => (
          <button
            key={p.key}
            type="button"
            className={`cascade-key is-${p.key}`}
            aria-pressed={selected === p.key}
            onClick={() => setSelected(selected === p.key ? null : p.key)}
            disabled={!has}
          >
            <i aria-hidden="true" />
            {p.name}
          </button>
        ))}
      </div>

      <div className="cascade-bar">

        {has && current ? (
          <div className="cascade-replay" aria-live="polite">
            <p key={current.jobId} className="replay-line">
              <b>{Math.min(jobIdx, settled.length - 1) + 1}/{settled.length}</b>
              <Link href={`/jobs/${current.jobId}`} className="mono">{shortId(current.jobId, 6, 4)}</Link>
              <span>paid <b>{usdc(current.paymentUsdc)}</b></span>
              <span>pool <span className="is-pool">{usdc(current.settlement!.advanceRepaidUsdc, 3)}</span></span>
              <span>operator <span className="is-operator">{usdc(current.settlement!.operatorNetUsdc, 3)}</span></span>
              <span>{timeAgo(current.lastAt, now)}</span>
            </p>
            <div className="cascade-controls">
              {resetBtn}
              <button type="button" className="icon-btn" onClick={() => step(-1)} aria-label="Previous settlement">
                <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M12 5l-5 5 5 5" /></svg>
              </button>
              <button type="button" className="icon-btn" onClick={() => setPaused((p) => !p)} aria-label={paused || reduced ? 'Play the cascade' : 'Pause the cascade'} disabled={reduced}>
                {paused || reduced ? (
                  <svg key="play" viewBox="0 0 20 20" aria-hidden="true"><path d="M7 5l8 5-8 5z" /></svg>
                ) : (
                  <svg key="pause" viewBox="0 0 20 20" aria-hidden="true"><path d="M7 5v10M13 5v10" /></svg>
                )}
              </button>
              <button type="button" className="icon-btn" onClick={() => step(1)} aria-label="Next settlement">
                <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M8 5l5 5-5 5" /></svg>
              </button>
            </div>
          </div>
        ) : resetBtn ? (
          <div className="cascade-controls cascade-controls-solo">{resetBtn}</div>
        ) : null}
      </div>
    </div>
  );
}
