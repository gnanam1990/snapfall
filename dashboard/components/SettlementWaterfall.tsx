/**
 * The settlement split for one job, drawn as one bar.
 *
 * This is the product's own sentence — "capital in a snap, settlement in a waterfall" — and the
 * job detail page is the only surface where it resolves into real figures for a real job. It was
 * four rows of a table, which makes the reader do the arithmetic and hides the thing that
 * matters: the pool is made whole BEFORE the operator sees anything, in the same transaction.
 *
 * So the escrow is drawn as a single bar split in proportion to where it goes. Segment widths
 * are the actual ratio, which means a job whose advance ate most of the escrow looks like one,
 * and no caption is needed to say so. The pool leg sits first and solid; the operator leg is
 * whatever survives it. Order is carried by position and weight, not hue — colour would have to
 * mean something here, and "first" is not an alarm. The figures underneath name what each leg
 * measures, numbered, because the order IS the evidence.
 *
 * The four states are genuinely different claims and are drawn differently:
 *   settled      the transfers happened; past tense.
 *   projection   what settlement would pay at the current chain state; conditional.
 *   terminal     Refunded/Cancelled — the escrow already went back to the customer, so any
 *                operator payout is fiction. Nothing is drawn.
 *   unavailable  the pool read failed. The split is unknowable, so the track is hatched and the
 *                legs read "unknown" — never zeroed, never an operator payout of the whole.
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
        there is no split to draw: the operator received nothing, and any advance was written off
        through the pool’s loss waterfall rather than repaid from this escrow.
      </p>
    );
  }

  const escrow = escrowUsdc === null ? null : BigInt(escrowUsdc);
  const repay = repaymentUsdc === null ? null : BigInt(repaymentUsdc);
  const net = operatorNetUsdc === null ? null : BigInt(operatorNetUsdc);

  /* A failed read and a real zero are different claims. The first is hatched and says "unknown";
     the second is an empty track and figures of 0, as checkable as any other number here. */
  const readsFailed = unavailable || escrow === null || repay === null || net === null;
  const known = !readsFailed && escrow! > 0n;

  /* Proportional split, clamped so a rounding artefact can never invert the two legs. */
  const poolPct = known
    ? Math.min(100, Math.max(0, Number((repay! * 10_000n) / escrow!) / 100))
    : 0;

  const verb = settled ? 'was' : 'would be';
  const leg = (value: string | null) =>
    readsFailed ? 'unknown' : `${formatUsdcExact(value)} USDC`;

  return (
    <figure className="wf">
      <div
        className={`wf-track${readsFailed ? ' is-unknown' : ''}`}
        role="img"
        aria-label={
          known
            ? `Of ${formatUsdcExact(escrowUsdc)} USDC held in escrow, ${formatUsdcExact(
                repaymentUsdc,
              )} ${verb} repaid to the capital pool first, and the operator ${
                settled ? 'received' : 'would receive'
              } the remaining ${formatUsdcExact(operatorNetUsdc)}.`
            : readsFailed
              ? 'The settlement split cannot be shown: a chain read did not answer, so how the escrow divides is unknown.'
              : 'Nothing is held in escrow, so there is nothing to split.'
        }
      >
        {known ? (
          <>
            <div className="wf-pool" style={{ width: `${poolPct}%` }} />
            <div className="wf-operator" style={{ width: `${100 - poolPct}%` }} />
          </>
        ) : null}
      </div>

      <dl className="wf-facts">
        <div>
          <dt>escrow held</dt>
          <dd className={escrow === null ? 'is-absent' : undefined}>
            {escrow === null ? 'not reported' : `${formatUsdcExact(escrowUsdc)} USDC`}
          </dd>
        </div>
        <div>
          <dt>1 · repaid to the pool, first</dt>
          <dd className={readsFailed ? 'is-absent' : undefined}>{leg(repaymentUsdc)}</dd>
        </div>
        <div>
          <dt>2 · to the operator, second</dt>
          <dd className={readsFailed ? 'is-absent' : undefined}>{leg(operatorNetUsdc)}</dd>
        </div>
      </dl>

      <figcaption className="wf-note">
        {unavailable
          ? 'The FloatPool read did not answer, so how this escrow divides is unknown. It is left undrawn rather than shown as an operator payout of the whole amount.'
          : settled
            ? 'Settled. Both transfers happened in one transaction, the pool made whole before the operator.'
            : known
              ? 'What settlement would pay out at the current chain state. The pool is repaid in full before the operator receives anything, in the same transaction as acceptance.'
              : 'Nothing is held in escrow yet. When the customer funds this job, this bar shows how the escrow divides.'}
      </figcaption>
    </figure>
  );
}
