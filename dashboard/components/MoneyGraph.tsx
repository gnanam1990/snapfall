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

type Beat = 'fund' | 'snap' | 'spend' | 'reject' | 'repay' | 'fall' | 'flywheel' | 'reset';

/**
 * Beat classification across all three vocabularies: the frozen chain ABI names, the daemon's
 * internal event kinds, and the local demo fixture. Anything unrecognized leaves the graph as
 * it is rather than guessing.
 *
 * `AdvanceRepaid` is deliberately its OWN beat, not part of the waterfall. The pool emits it
 * for the repayment leg alone; only `JobSettled` reports both legs, so only `JobSettled` is
 * allowed to drain the escrow and pay the operator. Conflating them animated a full waterfall
 * twice and let a leg-only event zero the operator's share (review: PR #40).
 */
/**
 * Maps a stream message to an animation beat.
 *
 * Takes the whole message, not just the kind, because the kind is not sufficient: the stream
 * normalizes a pending approval and an executed purchase to the same `approval.requested`.
 */
function beatFor(msg: ActivityMessage): Beat | null {
  switch (msg.kind) {
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
    // An approval request is only money leaving the treasury once it is APPROVED. The demo's
    // 0.06 alternative purchase arrives this way (the route rewrites
    // `approval.alternative_found` to `approval.requested` with state APPROVED), so without this
    // the graph's API total stopped at 0.04 and the replacement purchase animated nothing. The
    // pending 4.00 escalation normalizes to the SAME kind and must not be treated as a spend,
    // which is exactly why the state is the discriminator rather than the kind.
    case 'approval.requested':
      return msg.approvalState === 'approved' ? 'spend' : null;
    case 'approval.reject':
    case 'approval.rejected':
    case 'approval.request_alternative':
      return 'reject';
    case 'AdvanceRepaid':
      return 'repay';
    case 'JobSettled':
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

/**
 * Which side of the stream reported a spend.
 *
 * The spend kinds are disjoint by source, so the kind alone identifies it: `ExpenseRecorded` is
 * the chain's, everything else routed to a spend beat is the daemon's.
 */
function spendSource(kind: string): 'daemon' | 'chain' {
  return kind === 'ExpenseRecorded' ? 'chain' : 'daemon';
}

/** How many spends of one amount each source has reported this cycle. */
interface AmountTally {
  daemon: number;
  chain: number;
}




const CAPTION: Record<Beat, string> = {
  fund: 'The customer funded the JobVault',
  snap: 'The snap · capital in a snap',
  spend: 'Safe spend · policy authorized the purchase',
  reject: 'The owner said no · the workforce cannot embezzle itself',
  repay: 'The pool is repaid first',
  fall: 'Watch the Snapfall · the pool is repaid first',
  flywheel: 'The flywheel · cheaper capital, earned by delivering',
  reset: 'Waiting for the next job',
};

/** Atomic USDC parse that cannot throw on the render path: a bad frame counts as zero. */
const atomic = (s: string | null | undefined): bigint => (s && /^\d+$/.test(s) ? BigInt(s) : 0n);
const isAtomic = (s: string | null | undefined): s is string => !!s && /^\d+$/.test(s);

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
  // null means NOT YET KNOWN, not zero. The graph does not replay history, so on a mid-cycle
  // page load these amounts are genuinely unknown and must not be asserted as 0.00 — the same
  // discipline the rest of the Overview follows (review: PR #40).
  const [escrow, setEscrow] = useState<string | null>(null);
  const [spent, setSpent] = useState<string | null>(null);
  /**
   * Per-amount reconciliation of the two sources, so one purchase is counted exactly once.
   *
   * A single x402 purchase is reported twice: by the daemon (`payment.executed`) and by the chain
   * (`ExpenseRecorded`, once the expense is recorded against the vault). The H2 envelope carries
   * both vocabularies on one stream, so counting both doubles the API total, which is the graph
   * asserting money that never moved.
   *
   * A shared purchase id would make this trivial, and there is none: H1 ships `receiptHash` on
   * ExpenseRecorded while the daemon's payment.executed carries `request_id` and `intent_hash`,
   * and nothing here maps between them. So the reconciliation is per amount, and the rule is
   * simply that the true number of purchases of a given amount is the LARGER of what the two
   * sources claim. An event counts iff it raises its own source's tally above the other's:
   *
   *   daemon 0.04            d=1 > c=0  count   (a purchase)
   *   chain  0.04            c=1 > d=1  skip    (the echo of that purchase)
   *   daemon 0.04 again      d=2 > c=1  count   (a second, genuine, same-value purchase)
   *   chain  0.04            c=2 > d=2  skip    (its echo)
   *   chain  0.09 alone      c=1 > d=0  count   (an expense the daemon never reported)
   *
   * The comparison is symmetric, so it holds whichever source arrives first: if the chain event
   * leads, it counts and the daemon's later report is recognised as already-counted.
   *
   * Residual imprecision, inherent without a join key: a daemon purchase and an unrelated chain
   * expense of the exact same amount in one cycle are treated as one. It errs toward not
   * inventing money, and the only way to remove it is for the two events to share an id.
   */
  const spendTally = useRef<Map<string, AmountTally>>(new Map());
  const [operatorNet, setOperatorNet] = useState<string | null>(null);
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

    const next = beatFor(latest);
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

    // An amountless spend is not a spend event here AT ALL: it cannot be totalled, cannot be
    // reconciled against the other source, and must not set the spend caption either. The real
    // daemon's `payment.executed` carries no amount (approval/lifecycle.go), and the purchase it
    // confirms was already announced and counted by its amount-bearing policy-cleared
    // `approval.requested`, so letting it through only re-announced the same purchase.
    // Deliberately narrow: reject, flywheel and reset beats legitimately carry no amount.
    if (next === 'spend' && amount <= 0n) return;

    setBeat(next);

    switch (next) {
      case 'fund':
        // A funding event starts a cycle: the funded amount IS the escrow, and the previous
        // job's spend/payout must not bleed into it. This also makes the looping demo replay
        // reset correctly without depending on a fixture-specific reset event. Zero spend and
        // payout here are established facts (a new cycle), not assumptions.
        setEscrow(amount > 0n ? amount.toString() : null);
        setSpent('0');
        setOperatorNet('0');
        // A new cycle starts its own reconciliation; the looping demo repeats these amounts.
        spendTally.current.clear();
        spawn([{ pipe: 'fund', kind: 'fund', dur: 1.1, begin: 0 }]);
        break;
      case 'snap':
        spawn([{ pipe: 'snap', kind: 'snap', dur: 0.75, begin: 0 }]);
        break;
      case 'spend': {
        // Amountless events never reach here (guarded above), so every spend in the tally has a
        // real amount to reconcile on.
        //
        // One purchase, one count: this event only counts if it raises its own source's tally for
        // this amount above the other source's, i.e. if it represents a purchase the other side
        // has not already accounted for.
        const src = spendSource(latest.kind);
        const key = latest.amountUsdc ?? '-';
        const tally = spendTally.current.get(key) ?? { daemon: 0, chain: 0 };
        tally[src] += 1;
        spendTally.current.set(key, tally);
        const other = src === 'daemon' ? tally.chain : tally.daemon;
        if (tally[src] <= other) break; // already counted from the other source
        // Accumulate only from a known base; before any spend event the total is unknown.
        setSpent((s) => (s === null ? amount.toString() : (atomic(s) + amount).toString()));
        spawn([
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0 },
          { pipe: 'spend', kind: 'spend', dur: 1.0, begin: 0.18 },
        ]);
        break;
      }
      case 'reject':
        // A refusal moves no money: caption only, deliberately no droplet.
        break;
      case 'repay':
        // The repayment leg on its own: the pool is paid, but this event says nothing about
        // the operator's share or the escrow's final state, so neither is touched here.
        spawn([{ pipe: 'repay', kind: 'fall-pool', dur: 0.9, begin: 0 }]);
        break;
      case 'fall': {
        // JobSettled is the only event that reports BOTH legs, so it is the only one allowed
        // to drain the escrow and pay the operator. Prefer the chain's own figures; otherwise
        // derive the operator's share from the escrow it drains — but only when the escrow is
        // actually known, since deriving from an unknown base would invent a number.
        const reported = latest.settlement?.operatorNetUsdc;
        if (isAtomic(reported)) {
          setOperatorNet(reported);
          setEscrow('0');
        } else {
          setEscrow((esc) => {
            if (esc === null) return null; // unknown escrow: leave both unknown
            const held = atomic(esc);
            setOperatorNet((held > amount ? held - amount : 0n).toString());
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
        // An explicit reset DOES establish zero: the cycle is over by declaration.
        setEscrow('0');
        setSpent('0');
        setOperatorNet('0');
        spendTally.current.clear();
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
                {/* An em dash means not yet known from the stream — never a stand-in for 0.00. */}
                {n.id === 'customer'
                  ? 'external'
                  : isAtomic(balances[n.id])
                    ? `${formatUsdc(balances[n.id] as string)} USDC`
                    : '—'}
              </text>
            </g>
          ))}
        </svg>
      </div>
    </section>
  );
}
