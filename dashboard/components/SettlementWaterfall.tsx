/**
 * The settlement waterfall for one job, drawn.
 *
 * This is the product's own sentence -- "capital in a snap, settlement in a waterfall" -- and the
 * job detail page is the only surface where it resolves into real figures for a real job. It was
 * four rows of a table, which makes the reader do the arithmetic and hides the thing that matters:
 * the pool is made whole BEFORE the operator sees anything, in the same transaction.
 *
 * So the escrow is drawn as a column split in proportion to where it goes. Segment heights are the
 * actual ratio, which means a job whose advance ate most of the escrow looks like one, and no
 * caption is needed to say so.
 *
 * Order is carried by weight, not by colour, the same way PoolVessel draws the two outlets: the
 * pool leg is a full-weight .v-pipe and the operator leg is .v-pipe-dim. Colour would have to mean
 * something here, and "first" is not an alarm.
 *
 * The four states are genuinely different claims and are drawn differently:
 *   settled      the transfers happened; past tense.
 *   projection   what settlement would pay at the current chain state; conditional.
 *   terminal     Refunded/Cancelled -- the escrow already went back to the customer, so any
 *                operator payout is fiction. Nothing is drawn.
 *   unavailable  the pool read failed. The split is unknowable, so it is hatched, not zeroed.
 */

import { formatUsdcExact } from '@/lib/format';

interface Props {
  /** Escrowed customer payment, atomic USDC, or null when the vault read gave nothing. */
  escrowUsdc: string | null;
  /** Principal + fee owed to the pool, atomic USDC. */
  repaymentUsdc: string | null;
  /** What the operator is left with, atomic USDC, or null when it cannot be known. */
  operatorNetUsdc: string | null;
  /** True once the job is Accepted: these transfers already happened. */
  settled: boolean;
  /** True when the FloatPool read failed, so the split is unknown rather than zero. */
  unavailable: boolean;
  /** Refunded or Cancelled: the escrow's destination is already decided, and it is the customer. */
  terminal: string | null;
}

const W = 520;
const H = 190;
const COL_X = 232;
const COL_W = 74;
const COL_TOP = 24;
const COL_BOT = 158;
const COL_H = COL_BOT - COL_TOP;

export default function SettlementWaterfall({
  escrowUsdc,
  repaymentUsdc,
  operatorNetUsdc,
  settled,
  unavailable,
  terminal,
}: Props) {
  if (terminal) {
    return (
      <p className="wf-terminal">
        This job ended as <b>{terminal}</b>. The escrow was returned to the customer in full, so
        there is no waterfall to draw: the operator received nothing, and any advance was written
        off through the pool’s loss waterfall rather than repaid from this escrow.
      </p>
    );
  }

  const escrow = escrowUsdc === null ? null : BigInt(escrowUsdc);
  const repay = repaymentUsdc === null ? null : BigInt(repaymentUsdc);
  const net = operatorNetUsdc === null ? null : BigInt(operatorNetUsdc);
  const known = !unavailable && escrow !== null && repay !== null && net !== null && escrow > 0n;

  // Proportional split. Clamped so a rounding artefact can never invert the two legs.
  const poolFrac = known
    ? Math.min(1, Math.max(0, Number((repay! * 10_000n) / escrow!) / 10_000))
    : 0;
  const poolH = Math.round(COL_H * poolFrac);
  const splitY = COL_TOP + poolH;
  const poolLegY = COL_TOP + Math.max(14, poolH / 2);
  const opLegY = splitY + Math.max(14, (COL_BOT - splitY) / 2);

  const verb = settled ? 'was' : 'would be';

  return (
    <figure className="wf">
      <svg
        className="wf-svg"
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={
          known
            ? `Of ${formatUsdcExact(escrowUsdc)} USDC held in escrow, ${formatUsdcExact(
                repaymentUsdc,
              )} ${verb} repaid to the capital pool first, and the operator ${
                settled ? 'received' : 'would receive'
              } the remaining ${formatUsdcExact(operatorNetUsdc)}.`
            : 'The settlement split cannot be shown: the capital pool read did not answer, so how the escrow divides is unknown.'
        }
      >
        {/* Escrow enters from above. */}
        <g className="v-pipe">
          <path d={`M ${COL_X + COL_W / 2} 4 L ${COL_X + COL_W / 2} ${COL_TOP}`} />
          <text x={COL_X + COL_W / 2 + 8} y="10">
            escrow
          </text>
        </g>

        <rect x={COL_X} y={COL_TOP} width={COL_W} height={COL_H} className="v-wall" />

        {known ? (
          <>
            {/* The pool's share, drawn at the top because it is taken first. */}
            <rect
              x={COL_X + 1}
              y={COL_TOP + 1}
              width={COL_W - 2}
              height={Math.max(0, poolH - 1)}
              className="wf-pool"
            />
            <line x1={COL_X} y1={splitY} x2={COL_X + COL_W} y2={splitY} className="wf-split" />
          </>
        ) : (
          <g className="v-unknown">
            {[0.25, 0.5, 0.75].map((f) => (
              <line
                key={f}
                x1={COL_X + 1}
                y1={COL_TOP + COL_H * f}
                x2={COL_X + COL_W - 1}
                y2={COL_TOP + COL_H * f}
              />
            ))}
            <text x={COL_X + COL_W / 2} y={COL_TOP + COL_H / 2 - 8} textAnchor="middle">
              split unknown
            </text>
          </g>
        )}

        {/* Leg one: the pool. Full weight, because it is satisfied first. */}
        <g className="v-pipe">
          <path d={`M ${COL_X + COL_W} ${poolLegY} L ${COL_X + COL_W + 34} ${poolLegY}`} />
          <text x={COL_X + COL_W + 40} y={poolLegY + 3.5}>
            1 · pool repaid first
          </text>
        </g>

        {/* Leg two: the operator. Dimmed, because it is whatever survives leg one. */}
        <g className="v-pipe v-pipe-dim">
          <path d={`M ${COL_X + COL_W} ${opLegY} L ${COL_X + COL_W + 34} ${opLegY}`} />
          <text x={COL_X + COL_W + 40} y={opLegY + 3.5}>
            2 · operator, second
          </text>
        </g>

        {/* Figures hang off callout leaders on the left, so each number names what it measures. */}
        <g className="wf-callout">
          <line x1={COL_X - 8} y1={COL_TOP} x2={COL_X} y2={COL_TOP} />
          <text x={COL_X - 14} y={COL_TOP + 4} textAnchor="end" className="wf-fig">
            {escrowUsdc === null ? '—' : formatUsdcExact(escrowUsdc)}
          </text>
          <text x={COL_X - 14} y={COL_TOP + 18} textAnchor="end" className="wf-lab">
            escrow held
          </text>

          <line x1={COL_X - 8} y1={splitY} x2={COL_X} y2={splitY} />
          <text x={COL_X - 14} y={splitY + 4} textAnchor="end" className="wf-fig">
            {known ? formatUsdcExact(repaymentUsdc) : '—'}
          </text>
          <text x={COL_X - 14} y={splitY + 18} textAnchor="end" className="wf-lab">
            to the pool
          </text>

          <line x1={COL_X - 8} y1={COL_BOT} x2={COL_X} y2={COL_BOT} />
          <text x={COL_X - 14} y={COL_BOT + 4} textAnchor="end" className="wf-fig">
            {known ? formatUsdcExact(operatorNetUsdc) : '—'}
          </text>
          <text x={COL_X - 14} y={COL_BOT + 18} textAnchor="end" className="wf-lab">
            to the operator
          </text>
        </g>
      </svg>

      <figcaption className="wf-note">
        {unavailable
          ? 'The FloatPool read did not answer, so how this escrow divides is unknown. It is left undrawn rather than shown as an operator payout of the whole amount.'
          : settled
            ? 'Settled. Both transfers happened in one transaction, the pool made whole before the operator.'
            : 'What settlement would pay out at the current chain state. The pool is repaid in full before the operator receives anything, in the same transaction as acceptance.'}
      </figcaption>
    </figure>
  );
}
