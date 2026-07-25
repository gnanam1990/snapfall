'use client';

import { useEffect, useRef, useState } from 'react';
import type { ActivityMessage } from '@/lib/activity';
import type { PoolStats } from '@/lib/types';
import { formatUsdc } from '@/lib/format';
import ScoreRing from './ScoreRing';

/**
 * F1 Live Money Graph (V10, PRD §8) — the "watch the Snapfall" screen.
 *
 * A node-and-flow diagram driven by the SAME normalized activity stream the feed consumes, so
 * it works against the daemon vocabulary, the chain vocabulary, and the local fixture without
 * three copies of the mapping. Each beat sends animated droplets down the pipe the money
 * actually moved through:
 *
 *   fund      Customer  -> JobVault    (JobFunded / job.funded)
 *   snap      FloatPool -> Treasury    (AdvanceIssued / advance.issued)      THE SNAP
 *   spend     Treasury  -> x402 API    (payment.executed / ExpenseRecorded)
 *   fall      JobVault  -> FloatPool FIRST, then -> Operator (JobSettled)    THE WATERFALL
 *
 * The waterfall spawns the pool droplet BEFORE the operator droplet, because pool-first is the
 * whole claim of the protocol — it should be something a judge watches, not something a caption
 * asserts. Every figure is derived from event amounts; there are no demo constants here, so the
 * graph stays truthful for any job size on the real feed.
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
  { id: 'pool', name: 'FloatPool', cx: 100, cy: 350, accent: 'var(--accent)' },
  { id: 'treasury', name: 'Treasury', cx: 480, cy: 270, accent: 'var(--pos)' },
  { id: 'operator', name: 'Operator', cx: 860, cy: 350 },
];

/** Pipe paths, keyed by flow. Drawn edge-to-edge as gentle curves. */
const PIPES = {
  fund: 'M174,105 C 280,96 320,92 406,92',
  snap: 'M170,342 C 290,326 300,286 406,276',
  spend: 'M554,256 C 690,222 700,150 786,120',
  repay: 'M440,118 C 330,196 240,250 150,322',
  operator: 'M522,118 C 668,178 730,250 810,322',
} as const;

type Pipe = keyof typeof PIPES;

interface Drop {
  id: number;
  pipe: Pipe;
  kind: string;
  dur: number;
  begin: number;
}

type Beat = 'fund' | 'snap' | 'spend' | 'reject' | 'fall' | 'flywheel' | 'reset';

/**
 * Beat classification across all three vocabularies: the frozen chain ABI names, the daemon's
 * internal event kinds, and the local demo fixture. Anything unrecognized leaves the graph as
 * it is rather than guessing.
 */
function beatFor(kind: string): Beat | null {
  switch (kind) {
    case 'JobFunded':
    case 'job.funded':
      return 'fund';
    case 'AdvanceIssued':
    case 'advance.issued':
      return 'snap';
    case 'ExpenseRecorded':
    case 'payment.executed':
    case 'payment.delivered':
    case 'approval.alternative_found':
      return 'spend';
    case 'approval.reject':
    case 'approval.rejected':
    case 'approval.request_alternative':
      return 'reject';
    case 'JobSettled':
    case 'AdvanceRepaid':
    case 'job.accepted':
      return 'fall';
    case 'RateChanged':
    case 'rate.updated':
      return 'flywheel';
    case 'job.draft.created':
      return 'reset';
    default:
      return null;
  }
}

const CAPTION: Record<Beat, string> = {
  fund: 'The customer funded the JobVault',
  snap: 'The snap · capital in a snap',
  spend: 'Safe spend · policy authorized the purchase',
  reject: 'The owner said no · the workforce cannot embezzle itself',
  fall: 'Watch the Snapfall · the pool is repaid first',
  flywheel: 'The flywheel · cheaper capital, earned by delivering',
  reset: 'Waiting for the next job',
};

/** Atomic USDC parse that cannot throw on the render path: a bad frame counts as zero. */
const atomic = (s: string | null | undefined): bigint => (s && /^\d+$/.test(s) ? BigInt(s) : 0n);

export default function MoneyGraph({
  latest,
  treasuryUsdc,
  pool,
  jobPriceUsdc,
  live = true,
}: {
  /** Newest normalized activity message; the graph reacts to its kind + amount. */
  latest: ActivityMessage | null;
  treasuryUsdc: string | null;
  pool: PoolStats | null;
  jobPriceUsdc?: string | null;
  live?: boolean;
}) {
  const [escrow, setEscrow] = useState('0');
  const [spent, setSpent] = useState('0');
  const [operatorNet, setOperatorNet] = useState('0');
  const [beat, setBeat] = useState<Beat | null>(null);
  const [drops, setDrops] = useState<Drop[]>([]);

  const lastId = useRef<string | null>(null);
  const dropId = useRef(0);
  const timers = useRef<Set<ReturnType<typeof setTimeout>>>(new Set());

  // Cancel pending droplet-cleanup timers on unmount (review finding on the original PR #9).
  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach(clearTimeout);
      pending.clear();
    };
  }, []);

  useEffect(() => {
    if (!latest || latest.id === lastId.current) return;
    lastId.current = latest.id;

    const next = beatFor(latest.kind);
    if (!next) return;

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

    const amount = atomic(latest.amountUsdc);
    setBeat(next);

    switch (next) {
      case 'fund':
        // A funding event starts a cycle: the funded amount IS the escrow, and the previous
        // job's spend/payout must not bleed into it. This also makes the looping demo replay
        // reset correctly without depending on a fixture-specific reset event.
        if (amount > 0n) setEscrow(amount.toString());
        setSpent('0');
        setOperatorNet('0');
        spawn([{ pipe: 'fund', kind: 'fund', dur: 1.1, begin: 0 }]);
        break;
      case 'snap':
        spawn([{ pipe: 'snap', kind: 'snap', dur: 0.75, begin: 0 }]);
        break;
      case 'spend':
        setSpent((s) => (atomic(s) + amount).toString());
        spawn([
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0 },
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0.18 },
        ]);
        break;
      case 'reject':
        // A refusal moves no money: caption only, deliberately no droplet.
        break;
      case 'fall': {
        // The chain reports both legs of the waterfall on JobSettled; use them when present.
        // Otherwise fall back to deriving the operator's share from the escrow it drains,
        // so any job size still renders correctly (never a hardcoded demo figure).
        const reported = latest.settlement?.operatorNetUsdc;
        if (reported && /^\d+$/.test(reported)) {
          setOperatorNet(reported);
          setEscrow('0');
        } else {
          setEscrow((esc) => {
            const held = atomic(esc);
            const net = held > amount ? held - amount : 0n;
            setOperatorNet(net.toString());
            return '0';
          });
        }
        spawn([
          { pipe: 'repay', kind: 'fall-pool', dur: 0.9, begin: 0 },
          { pipe: 'operator', kind: 'fall-op', dur: 0.9, begin: 0.7 },
        ]);
        break;
      }
      case 'flywheel':
        break;
      case 'reset':
        setEscrow('0');
        setSpent('0');
        setOperatorNet('0');
        break;
    }
  }, [latest]);

  const balances: Record<string, string | null> = {
    customer: null,
    escrow,
    merchant: spent,
    pool: pool?.tvlUsdc ?? null,
    treasury: treasuryUsdc,
    operator: operatorNet,
  };

  return (
    <section className="mg" aria-label="Live money graph">
      <div className="mg-head">
        <div className="mg-heading">
          <h2>Live money graph</h2>
          <span className={`mg-live${live ? '' : ' is-waiting'}`}>{live ? 'live' : 'reconnecting'}</span>
          <p className={`mg-beat${beat ? ' is-shown' : ''} mg-beat-${beat ?? 'idle'}`}>
            {beat ? CAPTION[beat] : 'watch the money move'}
          </p>
        </div>
        <ScoreRing rateBps={pool?.orgRateBps ?? null} jobPriceUsdc={jobPriceUsdc} />
      </div>

      <div className="mg-stage">
        <svg viewBox="0 0 960 440" className="mg-svg" role="img" aria-label="Money flow between the customer, JobVault, FloatPool, treasury, paid API and operator">
          <defs>
            <filter id="mg-glow" x="-60%" y="-60%" width="220%" height="220%">
              <feGaussianBlur stdDeviation="4" result="b" />
              <feMerge>
                <feMergeNode in="b" />
                <feMergeNode in="SourceGraphic" />
              </feMerge>
            </filter>
          </defs>

          {(Object.keys(PIPES) as Pipe[]).map((k) => (
            <path key={k} id={`mg-pipe-${k}`} d={PIPES[k]} className={`mg-pipe mg-pipe-${k}`} fill="none" />
          ))}

          {drops.map((d) => (
            <circle
              key={d.id}
              r={d.kind === 'snap' ? 9 : d.kind.startsWith('fall') ? 8 : 6}
              className={`mg-drop mg-drop-${d.kind}`}
              filter="url(#mg-glow)"
            >
              <animateMotion dur={`${d.dur}s`} begin={`${d.begin}s`} fill="freeze" rotate="auto">
                <mpath href={`#mg-pipe-${d.pipe}`} />
              </animateMotion>
            </circle>
          ))}

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
              <text x={n.cx} y={n.cy - 6} className="mg-name">
                {n.name}
              </text>
              <text x={n.cx} y={n.cy + 15} className="mg-bal">
                {n.id === 'customer'
                  ? 'external'
                  : balances[n.id] === null
                    ? '—'
                    : `${formatUsdc(balances[n.id] as string)} USDC`}
              </text>
            </g>
          ))}
        </svg>
      </div>
    </section>
  );
}
