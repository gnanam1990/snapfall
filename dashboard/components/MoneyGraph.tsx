'use client';

import { useEffect, useRef, useState } from 'react';
import type { FinancialEvent, PoolStats } from '@/lib/types';
import { formatUsdc } from '@/lib/format';
import Card, { CardHeader, Well } from './Card';
import ScoreRing from './ScoreRing';

/**
 * F1 Live Money Graph (V10) · the "watch the Snapfall" screen.
 *
 * A node-and-flow diagram driven by the same SSE events the Overview consumes. Each event
 * sends animated droplets down the pipe it moves money through:
 *   job.funded        Customer -> Escrow (25.00)
 *   advance.issued    Pool -> Treasury (12.50)          THE SNAP (bold, glowing)
 *   payment.delivered Treasury -> Merchant (0.04/0.06)  small droplets
 *   job.accepted      Escrow -> Pool FIRST, then Escrow -> Operator   THE WATERFALL
 *   rate.updated      handled by the Score Ring via the rate prop
 * The waterfall spawns the pool droplet before the operator droplet · pool-first is the
 * whole point, so it is visible, not just described.
 */

interface Node {
  id: string;
  name: string;
  cx: number;
  cy: number;
  accent?: string;
}

const W = 148;
const H = 58;

const NODES: Node[] = [
  { id: 'customer', name: 'Customer', cx: 100, cy: 110 },
  { id: 'escrow', name: 'JobVault', cx: 480, cy: 90, accent: 'var(--sky)' },
  { id: 'merchant', name: 'x402 API', cx: 860, cy: 110 },
  { id: 'pool', name: 'FloatPool', cx: 100, cy: 350, accent: 'var(--color-accent)' },
  { id: 'treasury', name: 'Treasury', cx: 480, cy: 270, accent: 'var(--pos)' },
  { id: 'operator', name: 'Operator', cx: 860, cy: 350 },
];

/** Pipe paths, keyed by flow. Drawn edge-to-edge as gentle curves. */
const PIPES: Record<string, string> = {
  fund: 'M174,105 C 280,96 320,92 406,92',
  snap: 'M170,342 C 290,326 300,286 406,276',
  spend: 'M554,256 C 690,222 700,150 786,120',
  repay: 'M440,118 C 330,196 240,250 150,322',
  operator: 'M522,118 C 668,178 730,250 810,322',
};

interface Drop {
  id: number;
  pipe: keyof typeof PIPES;
  kind: string;
  dur: number;
  begin: number;
}

const nodeById = (id: string) => NODES.find((n) => n.id === id)!;

/** Safe atomic parse: malformed stream amounts count as 0 instead of throwing mid-render. */
const atomic = (s: string | undefined): bigint => (s && /^\d+$/.test(s) ? BigInt(s) : 0n);

export default function MoneyGraph({
  event,
  treasuryUsdc,
  pool,
  jobPriceUsdc,
  streamStatus = 'live',
}: {
  event: FinancialEvent | null;
  treasuryUsdc: string;
  pool: PoolStats;
  jobPriceUsdc?: string;
  streamStatus?: 'connecting' | 'live' | 'reconnecting';
}) {
  // Balances the graph tracks locally from the event stream (treasury + pool come from props).
  // Every figure is DERIVED from event amounts - no demo constants (review: PR #9).
  const [escrow, setEscrow] = useState('0');
  const [spent, setSpent] = useState('0');
  const [operatorNet, setOperatorNet] = useState('0');
  const [beat, setBeat] = useState<{ label: string; kind: string } | null>(null);
  const [drops, setDrops] = useState<Drop[]>([]);

  const lastSeq = useRef<number>(-1);
  const dropId = useRef(0);
  const timers = useRef<Set<ReturnType<typeof setTimeout>>>(new Set());

  // Cancel any pending drop-cleanup timers on unmount (review: PR #9).
  useEffect(() => {
    const pending = timers.current;
    return () => pending.forEach(clearTimeout);
  }, []);

  useEffect(() => {
    if (!event || event.seq === lastSeq.current) return;
    lastSeq.current = event.seq;

    const spawn = (specs: Omit<Drop, 'id'>[]) => {
      const made = specs.map((s) => ({ ...s, id: ++dropId.current }));
      setDrops((cur) => [...cur, ...made]);
      const maxMs = Math.max(...specs.map((s) => (s.begin + s.dur) * 1000)) + 200;
      const ids = new Set(made.map((m) => m.id));
      const t = setTimeout(() => {
        timers.current.delete(t);
        setDrops((cur) => cur.filter((d) => !ids.has(d.id)));
      }, maxMs);
      timers.current.add(t);
    };

    switch (event.type) {
      case 'job.funded':
        // The funded amount IS the escrow; the stream emits this at the start of every cycle.
        setEscrow(atomic(event.amountUsdc).toString());
        setBeat({ label: 'Customer funds the JobVault', kind: 'fund' });
        spawn([{ pipe: 'fund', kind: 'fund', dur: 1.1, begin: 0 }]);
        break;
      case 'advance.issued':
        setBeat({ label: 'The snap · capital in a snap', kind: 'snap' });
        spawn([{ pipe: 'snap', kind: 'snap', dur: 0.75, begin: 0 }]);
        break;
      case 'payment.delivered':
        setSpent((s) => (atomic(s) + atomic(event.amountUsdc)).toString());
        setBeat({ label: 'Safe spend · x402 auto-approved', kind: 'spend' });
        spawn([
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0 },
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0.18 },
        ]);
        break;
      case 'approval.rejected':
        setBeat({ label: 'Owner rejects · the workforce cannot embezzle itself', kind: 'reject' });
        break;
      case 'job.accepted': {
        // Waterfall: the event amount is what the pool was repaid; the operator gets the
        // remainder of the escrow. Both derived, valid for any job size.
        const repaid = atomic(event.amountUsdc);
        setEscrow((esc) => {
          const e = atomic(esc);
          const net = e > repaid ? e - repaid : 0n;
          setOperatorNet(net.toString());
          return '0';
        });
        setBeat({ label: 'Watch the Snapfall · pool repaid first', kind: 'fall' });
        // Pool-first: the repay droplet leads, the operator droplet follows.
        spawn([
          { pipe: 'repay', kind: 'fall-pool', dur: 0.9, begin: 0 },
          { pipe: 'operator', kind: 'fall-op', dur: 0.9, begin: 0.7 },
        ]);
        break;
      }
      case 'rate.updated':
        setBeat({ label: 'The flywheel · cheaper capital, earned', kind: 'flywheel' });
        break;
      case 'job.draft.created':
        setEscrow('0');
        setSpent('0');
        setOperatorNet('0');
        setBeat(null);
        break;
      default:
        break;
    }
  }, [event]);

  const balances: Record<string, string | null> = {
    customer: null,
    escrow,
    merchant: spent,
    pool: pool.tvlUsdc,
    treasury: treasuryUsdc,
    operator: operatorNet,
  };

  return (
    <Card>
      <CardHeader
        title="Live money graph"
        meta={
          streamStatus === 'live' ? (
            <>
              <span className="dot-live" style={{ width: 6, height: 6 }} /> demo replay · updates in &lt;2s
            </>
          ) : (
            <>
              <span className="dot-live" style={{ width: 6, height: 6, background: 'var(--warn)', animation: 'none' }} /> reconnecting…
            </>
          )
        }
      />
      <div className="p-5">
        <div className="flex items-center justify-between gap-4">
          <div className={`mg-beat${beat ? ' show' : ''} beat-${beat?.kind ?? 'none'}`}>
            {beat?.label ?? 'watch the money move'}
          </div>
          <ScoreRing rateBps={pool.orgRateBps} jobPriceUsdc={jobPriceUsdc} />
        </div>

        <Well className="mt-3 px-2 py-1">
          <svg viewBox="0 0 960 440" className="mg-svg" role="img" aria-label="Snapfall money flow">
        <defs>
          <filter id="mg-glow" x="-60%" y="-60%" width="220%" height="220%">
            <feGaussianBlur stdDeviation="4" result="b" />
            <feMerge>
              <feMergeNode in="b" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
        </defs>

        {/* pipes */}
        {(Object.keys(PIPES) as (keyof typeof PIPES)[]).map((k) => (
          <path key={k} id={`pipe-${k}`} d={PIPES[k]} className={`mg-pipe pipe-${k}`} fill="none" />
        ))}

        {/* droplets */}
        {drops.map((d) => (
          <circle key={d.id} r={d.kind === 'snap' ? 9 : d.kind.startsWith('fall') ? 8 : 6} className={`mg-drop drop-${d.kind}`} filter="url(#mg-glow)">
            <animateMotion dur={`${d.dur}s`} begin={`${d.begin}s`} fill="freeze" rotate="auto">
              <mpath href={`#pipe-${d.pipe}`} />
            </animateMotion>
          </circle>
        ))}

          {/* nodes */}
          {NODES.map((n) => (
            <g key={n.id} className="mg-node">
              <rect
                x={n.cx - W / 2}
                y={n.cy - H / 2}
                width={W}
                height={H}
                rx={13}
                className="mg-box"
                style={n.accent ? ({ ['--n' as string]: n.accent }) : undefined}
              />
              <text x={n.cx} y={n.cy - 6} className="mg-name">{n.name}</text>
              <text x={n.cx} y={n.cy + 15} className="mg-bal">
                {balances[n.id] == null ? 'Acme Labs' : `${formatUsdc(balances[n.id] as string)} USDC`}
              </text>
            </g>
          ))}
        </svg>
        </Well>
      </div>
    </Card>
  );
}
