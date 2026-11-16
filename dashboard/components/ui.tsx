'use client';

import Link from 'next/link';
import { useEffect, useState, type ReactNode } from 'react';
import type { ChainActivity, ChainJob, JobStage } from '@/lib/chainHistory';

// ── formatting ──────────────────────────────────────────────────────────────

/** Atomic USDC (6 dp) to a display amount, or a dash when absent. */
export function usdc(atomic: string | bigint | null | undefined, digits = 2): string {
  if (atomic === null || atomic === undefined || atomic === '') return '—';
  let v: bigint;
  try {
    v = typeof atomic === 'bigint' ? atomic : BigInt(atomic);
  } catch {
    return '—';
  }
  const neg = v < 0n;
  if (neg) v = -v;
  const whole = v / 1_000_000n;
  const frac = (v % 1_000_000n).toString().padStart(6, '0').slice(0, digits);
  return `${neg ? '-' : ''}${whole.toLocaleString('en-US')}${digits ? `.${frac}` : ''}`;
}

export const toNumber = (atomic: string | null | undefined): number => (atomic ? Number(BigInt(atomic)) / 1e6 : 0);

export function pct(bps: number | null | undefined, digits = 0): string {
  return bps === null || bps === undefined ? '—' : `${(bps / 100).toFixed(digits)}%`;
}

export function shortId(v: string, head = 6, tail = 4): string {
  return v.length > head + tail + 1 ? `${v.slice(0, head)}…${v.slice(-tail)}` : v;
}

export function timeAgo(unix: number | null, now: number = Date.now()): string {
  if (!unix) return '';
  const s = Math.max(0, Math.floor(now / 1000 - unix));
  if (s < 45) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d} day${d === 1 ? '' : 's'} ago`;
  return shortDate(unix);
}

export function shortDate(unix: number | null): string {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
}

export function clock(unix: number | null): string {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' });
}

export function dayLabel(unix: number | null): string {
  if (!unix) return 'Date unknown';
  const d = new Date(unix * 1000);
  const today = new Date();
  const y = new Date(today);
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return 'Today';
  if (d.toDateString() === y.toDateString()) return 'Yesterday';
  return d.toLocaleDateString('en-US', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' });
}

/** Re-render every `ms` so relative times stay true while the page is open. */
export function useNow(ms = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

// ── the band: every page's header ───────────────────────────────────────────

/** A one-line freshness statement: when the chain was last read, or why it was not. */
export function Freshness({ updatedAt, error, network }: { updatedAt: number | null; error: string | null; network: string }) {
  const now = useNow(10_000);
  if (updatedAt) {
    return error ? <span>{network} · last read, refresh failed</span> : <span>{network} · {timeAgo(Math.floor(updatedAt / 1000), now)}</span>;
  }
  return error ? <span className="is-bad">Could not read {network}</span> : <span>Reading {network}…</span>;
}

/**
 * The navy band that opens every page: the page name, one sentence, optional figures, and an
 * optional right-hand panel (the overview's waterfall). It is the one place the page is loud.
 */
export function Band({
  title, lede, long, sub, figs, aside, meta,
}: {
  title: string; lede: ReactNode; long?: boolean; sub?: ReactNode; figs?: { label: string; value: string; unit?: string }[];
  aside?: ReactNode; meta?: ReactNode;
}) {
  return (
    <section className="band">
      <div className={`wrap band-grid${aside ? '' : ' is-single'}`}>
        <div>
          <h1>{title}</h1>
          <p className={`band-lede${long ? ' is-long' : ''}`}>{lede}</p>
          {sub ? <p className="band-sub">{sub}</p> : null}
          {figs?.length ? (
            <dl className="band-figs">
              {figs.map((f) => (
                <div key={f.label}>
                  <dt>{f.label}</dt>
                  <dd>{f.value}{f.unit ? <small>{f.unit}</small> : null}</dd>
                </div>
              ))}
            </dl>
          ) : null}
          {meta ? <div className="band-meta" role="status">{meta}</div> : null}
        </div>
        {aside ? <div>{aside}</div> : null}
      </div>
    </section>
  );
}

/**
 * Waterfall of every settled job taken together: what customers paid, the part that fell back
 * to the pool first, and what the operator kept. The pool step hangs from the top of the paid
 * column, the way a waterfall chart shows a deduction; values are written on every bar.
 */
export function Waterfall({ paid, pool, operator }: { paid: number; pool: number; operator: number }) {
  const W = 420;
  const H = 210;
  const pad = { l: 8, r: 8, t: 26, b: 34 };
  const max = Math.max(paid, pool + operator, 0.000001);
  const y = (v: number) => pad.t + (1 - v / max) * (H - pad.t - pad.b);
  const colW = (W - pad.l - pad.r) / 3;
  const barW = colW * 0.56;
  const x = (i: number) => pad.l + colW * i + (colW - barW) / 2;
  const base = y(0);
  const cols = [
    { label: 'Customers paid', top: y(paid), bottom: base, value: paid, cls: 'wf-total', drop: false },
    { label: 'Pool repaid', top: y(paid), bottom: y(paid - pool), value: pool, cls: 'wf-pool', drop: true },
    { label: 'Operator kept', top: y(operator), bottom: base, value: operator, cls: 'wf-op', drop: false },
  ];
  // Truncated, not rounded, to match usdc() everywhere else on the page (23.9988 reads 23.99).
  const fmt = (v: number) => (Math.floor(v * 100 + 1e-9) / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return (
    <svg className="wf" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Customers paid ${fmt(paid)} USDC. ${fmt(pool)} went back to the pool first; the operator kept ${fmt(operator)}.`}>
      <line className="wf-grid" x1={pad.l} x2={W - pad.r} y1={base} y2={base} />
      <line className="wf-link" x1={x(0) + barW} x2={x(1)} y1={y(paid)} y2={y(paid)} />
      <line className="wf-link" x1={x(1) + barW} x2={x(2)} y1={y(paid - pool)} y2={y(paid - pool)} />
      {cols.map((c, i) => (
        <g key={c.label}>
          <title>{`${c.label}: ${fmt(c.value)} USDC`}</title>
          <rect className={`wf-bar ${c.cls}${c.drop ? ' is-drop' : ''}`} style={{ animationDelay: `${i * 140}ms` }} x={x(i)} y={c.top} width={barW} height={Math.max(2, c.bottom - c.top)} rx={4} />
          <text className="wf-val" x={x(i) + barW / 2} y={c.top - 8} textAnchor="middle">{fmt(c.value)}</text>
          <text className="wf-axis" x={x(i) + barW / 2} y={H - 12} textAnchor="middle">{c.label}</text>
        </g>
      ))}
    </svg>
  );
}

export function ErrorNotice({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="notice" role="alert">
      <p>{message}</p>
      <button className="btn" onClick={onRetry}>Try again</button>
    </div>
  );
}

// ── job stage ───────────────────────────────────────────────────────────────

export const STAGE_LABEL: Record<JobStage, string> = {
  Created: 'Waiting for payment',
  Funded: 'Paid, not started',
  InProgress: 'In progress',
  Delivered: 'Delivered',
  Settled: 'Settled',
  Refunded: 'Refunded',
  Cancelled: 'Cancelled',
};

const STAGE_TONE: Record<JobStage, string> = {
  Created: '',
  Funded: 'is-wait',
  InProgress: 'is-live',
  Delivered: 'is-live',
  Settled: 'is-good',
  Refunded: 'is-bad',
  Cancelled: '',
};

export function StageText({ stage }: { stage: JobStage }) {
  return <span className={`state ${STAGE_TONE[stage]}`}>{STAGE_LABEL[stage]}</span>;
}

/** Steps of the happy path a job has completed, 0–5. */
export function stepsDone(job: ChainJob): number {
  switch (job.stage) {
    case 'Created': return 0;
    case 'Funded': return job.advance ? 2 : 1;
    case 'InProgress': return 2;
    case 'Delivered': return 4;
    case 'Settled': return 5;
    default: return 1;
  }
}

const STEP_NAMES = ['Paid', 'Advanced', 'Working', 'Delivered', 'Settled'];

export function Steps({ job }: { job: ChainJob }) {
  const done = stepsDone(job);
  const stopped = job.stage === 'Refunded' || job.stage === 'Cancelled';
  return (
    <ol className="steps" aria-label={`${done} of 5 steps done`}>
      {STEP_NAMES.map((name, i) => {
        const state = i < done ? 'done' : i === done && !stopped ? 'now' : 'next';
        return (
          <li key={name} className={`step is-${state}`} aria-current={state === 'now' ? 'step' : undefined}>
            <span className="step-bar" />
            <span className="step-name">{name}</span>
          </li>
        );
      })}
    </ol>
  );
}

export function StepsMini({ job }: { job: ChainJob }) {
  const done = stepsDone(job);
  const stopped = job.stage === 'Refunded' || job.stage === 'Cancelled';
  return (
    <span className="steps-mini" role="img" aria-label={`${done} of 5 steps done`}>
      {[0, 1, 2, 3, 4].map((i) => <span key={i} className={i < done ? 'on' : i === done && !stopped ? 'now' : undefined} />)}
    </span>
  );
}

// ── links ───────────────────────────────────────────────────────────────────

export function TxLink({ explorer, tx, children = 'Proof' }: { explorer: string; tx: string; children?: ReactNode }) {
  return (
    <a href={`${explorer}/tx/${tx}`} target="_blank" rel="noreferrer" title={`Transaction ${tx}`}>
      {children}
    </a>
  );
}

export function AddrLink({ explorer, address }: { explorer: string; address: string }) {
  return (
    <a className="addr" href={`${explorer}/address/${address}`} target="_blank" rel="noreferrer" title={address}>
      {shortId(address)}
    </a>
  );
}

// ── the fall ────────────────────────────────────────────────────────────────

/**
 * Settled jobs, each drawn as one bar split into what the pool was repaid and what the operator
 * kept. Bar length is the job's price on a shared scale, so a 19.50 job is visibly larger than a
 * 1.00 job. Amounts sit beside every bar, so the split never depends on colour alone.
 */
export function Fall({ jobs, limit }: { jobs: ChainJob[]; limit?: number }) {
  const settled = jobs.filter((j) => j.settlement).slice(0, limit);
  if (!settled.length) return <div className="empty">No job has settled yet. The first settlement will appear here, split between the pool and the operator.</div>;
  const max = Math.max(...settled.map((j) => toNumber(j.paymentUsdc)));
  return (
    <ol className="fall">
      {settled.map((j, i) => {
        const pool = toNumber(j.settlement!.advanceRepaidUsdc);
        const op = toNumber(j.settlement!.operatorNetUsdc);
        const total = pool + op || toNumber(j.paymentUsdc);
        const width = max ? Math.max(1.5, (total / max) * 100) : 0;
        const label = `Job ${shortId(j.jobId, 8, 4)}: ${usdc(j.paymentUsdc)} USDC. Pool repaid ${usdc(j.settlement!.advanceRepaidUsdc)}, operator kept ${usdc(j.settlement!.operatorNetUsdc)}.`;
        return (
          <li key={`${j.jobId}:${j.settlement!.tx}`} className="fall-row">
            <span className="fall-when">{shortDate(j.lastAt)}</span>
            <Link href={`/jobs/${j.jobId}`} className="fall-track" aria-label={label} title={label}>
              <span className="fall-bar" style={{ width: `${width}%`, ['--i' as string]: i }}>
                {pool > 0 ? <span className="fall-pool" style={{ flex: pool }} /> : null}
                {op > 0 ? <span className="fall-op" style={{ flex: op }} /> : null}
              </span>
            </Link>
            <span className="fall-amt">
              <strong>{usdc(j.paymentUsdc)}</strong> USDC
              <small>{pool > 0 ? `pool ${usdc(j.settlement!.advanceRepaidUsdc)}, operator ${usdc(j.settlement!.operatorNetUsdc)}` : `operator ${usdc(j.settlement!.operatorNetUsdc)}`}</small>
            </span>
          </li>
        );
      })}
    </ol>
  );
}

export function FallLegend({ band = false }: { band?: boolean }) {
  return (
    <div className="legend" aria-hidden="true">
      <span><i style={{ background: band ? 'var(--band-pool)' : 'var(--pool)' }} />Pool, repaid first</span>
      <span><i style={{ background: band ? 'var(--band-op)' : 'var(--operator)' }} />Operator</span>
    </div>
  );
}

// ── activity ────────────────────────────────────────────────────────────────

export function ActivityFeed({ items, explorer, groupByDay = false, now }: { items: ChainActivity[]; explorer: string; groupByDay?: boolean; now: number }) {
  if (!items.length) return <div className="empty">Nothing has happened on chain yet. Events appear here as soon as a job is created.</div>;
  const rows: ReactNode[] = [];
  let lastDay = '';
  for (const a of items) {
    if (groupByDay) {
      const d = dayLabel(a.at);
      if (d !== lastDay) {
        rows.push(<li key={`day-${a.id}`} className="feed-day">{d}</li>);
        lastDay = d;
      }
    }
    rows.push(
      <li key={a.id} className={`feed-item${a.kind === 'job.settled' ? ' is-good' : ''}`}>
        <span className="feed-time">{groupByDay ? clock(a.at) : timeAgo(a.at, now)}</span>
        <span>
          <span className="feed-text">{a.text}</span>
          <span className="feed-meta">
            <span>{a.actor}</span>
            {a.jobId ? <Link href={`/jobs/${a.jobId}`}>Job {shortId(a.jobId, 8, 4)}</Link> : null}
            <TxLink explorer={explorer} tx={a.tx} />
          </span>
        </span>
        <span className="amount">{a.amountUsdc ? <>{usdc(a.amountUsdc)}<small>USDC</small></> : null}</span>
      </li>,
    );
  }
  return <ol className="feed">{rows}</ol>;
}

// ── count-up ────────────────────────────────────────────────────────────────

/**
 * A figure that counts up from zero the first time it appears, then follows its value. The final
 * text is always the exact formatted value; only the first second is animated, and not at all
 * under reduced motion.
 */
export function CountUp({ value, format }: { value: number; format: (v: number) => string }) {
  const [shown, setShown] = useState<number | null>(null);
  useEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      setShown(value);
      return;
    }
    let raf = 0;
    const start = performance.now();
    const from = 0;
    const tick = (t: number) => {
      const k = Math.min(1, (t - start) / 1100);
      const eased = 1 - Math.pow(1 - k, 3);
      setShown(from + (value - from) * eased);
      if (k < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [value]);
  return <span className="num">{format(shown ?? 0)}</span>;
}
