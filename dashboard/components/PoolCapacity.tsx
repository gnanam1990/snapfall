/**
 * The capital pool, measured.
 *
 * This replaces the drawn vessel with the information the vessel carried, in ledger form.
 * FloatPool genuinely is a tank with capacity limits — an advance may not exceed 10% of TVL for
 * one org, total lending may not exceed 80% of TVL pool-wide, and a request that crosses either
 * reverts with CapExceeded. Those caps are marked as ticks where they actually sit on the
 * utilisation meter, so the reader sees headroom without doing arithmetic — the one job the
 * drawing did that a table cannot.
 *
 * Utilisation is a chain read, not an indexer figure: FloatPool exposes totalOutstanding and
 * totalAssets, so 0% means nothing is drawn right now and is as verifiable as TVL. The only
 * genuine unknown is the window before the fetch returns, which reads as "reading chain" — a
 * different statement from "nothing is drawn", and the meter keeps them apart: an empty track
 * asserts nothing-is-lent, so an unread track is hatched instead.
 */

interface Props {
  /** Pool TVL as a 6dp decimal string, read live from FloatPool.totalAssets. */
  tvlUsdc: string | null;
  /**
   * Drawn principal in basis points of TVL, computed from FloatPool.totalOutstanding over
   * totalAssets (floatChain.ts:356). Null only while the fetch is in flight — no indexer is
   * involved, so a returned 0 means nothing is drawn and is as verifiable as TVL itself.
   */
  utilizationBps: number | null;
  /**
   * The org's current advance rate in bps, from FloatPool.advanceRate.
   *
   * Null does NOT mean the read failed. loadFloatViews only issues the advanceRate call when an
   * organisation address is configured (floatChain.ts:312 and :335, fed by
   * SNAPFALL_TREASURY_ADDRESS in app/api/float/route.ts:24). With that unset the call is never
   * made, so the honest statement is that nothing was asked for — not that the chain was asked
   * and could not answer.
   */
  orgRateBps: number | null;
  /** Fees accrued to the pool, 6dp decimal string, from FloatPool.feesAccrued. */
  feesAccruedUsdc: string | null;
  /** The first-loss reserve, 6dp decimal string: 20% of every fee. */
  reserveUsdc: string | null;
}

/* FloatPool.sol constants, marked on the meter rather than described. */
const UTILISATION_CAP = 0.8; // UTILIZATION_CAP_BPS 8000, pool-wide
const ORG_CAP = 0.1; // ORG_EXPOSURE_CAP_BPS 1000, per org

export default function PoolCapacity({
  tvlUsdc,
  utilizationBps,
  orgRateBps,
  feesAccruedUsdc,
  reserveUsdc,
}: Props) {
  const loaded = utilizationBps !== null;
  const used = loaded ? utilizationBps / 10_000 : 0;
  /* A cap is a threshold, not an alarm: its tick colours only when the level actually approaches
     it. Permanent red at 0% utilisation would make the colour decorative, which is the one thing
     the semantic-colour rule forbids. */
  const capState = used >= UTILISATION_CAP ? 'at' : used >= UTILISATION_CAP - 0.1 ? 'near' : '';

  return (
    <section className="pool" aria-label="Capital pool">
      <div className="pool-head">
        <div>
          <h2 className="pool-title">Capital pool</h2>
          <p className="pool-sub">Read live from FloatPool on Arc testnet</p>
        </div>
        <div className="pool-figure">
          <span className="pool-tvl">{tvlUsdc ?? '—'}</span>
          <span className="pool-unit">USDC</span>
        </div>
      </div>

      <div
        className="pool-meter"
        role="img"
        aria-label={
          `Capital pool holding ${tvlUsdc ?? 'an unread amount of'} USDC. ` +
          (loaded
            ? `${(used * 100).toFixed(1)} percent is currently lent out. `
            : 'The current lending figure is still being read from the chain. ') +
          'Lending is capped at 80 percent of the pool overall, and 10 percent for any one operator.'
        }
      >
        <div className={`pool-track${loaded ? '' : ' is-unknown'}`}>
          {loaded && used > 0 ? (
            <div className="pool-fill" style={{ width: `${Math.min(100, used * 100)}%` }} />
          ) : null}
          <span className={`pool-tick${capState ? ` is-${capState}` : ''}`} style={{ left: '80%' }} />
          <span className="pool-tick" style={{ left: '10%' }} />
        </div>
        <div className="pool-scale">
          <span style={{ left: '10%' }}>10% per-operator cap</span>
          <span
            style={{ left: '80%', transform: 'translateX(-100%)' }}
            className={capState ? 'is-at-cap' : undefined}
          >
            80% utilisation cap
          </span>
        </div>
        <p className="pool-meter-note">
          {loaded
            ? used === 0
              ? 'Nothing is drawn right now — a real, checkable zero, readable as totalOutstanding on the contract.'
              : `${(used * 100).toFixed(1)}% of the pool is lent out.`
            : 'Reading the chain…'}
        </p>
      </div>

      {/* Every figure names its source. An absent one says which absence it is, at the muted
          floor with tabular figures off, so it can never be mistaken for a value at a glance. */}
      <dl className="pool-facts">
        <div className="pool-fact">
          <dt>Lent out</dt>
          <dd className={loaded ? undefined : 'is-absent'}>
            {loaded ? `${(used * 100).toFixed(1)}%` : 'reading chain'}
            <span className="pool-src">totalOutstanding ÷ totalAssets · cap 80%</span>
          </dd>
        </div>
        <div className="pool-fact">
          <dt>Fees accrued</dt>
          <dd className={feesAccruedUsdc === null ? 'is-absent' : undefined}>
            {feesAccruedUsdc === null ? 'not reported' : `${feesAccruedUsdc} USDC`}
            <span className="pool-src">FloatPool.feesAccrued · 2% of each principal</span>
          </dd>
        </div>
        <div className="pool-fact">
          <dt>First-loss reserve</dt>
          <dd className={reserveUsdc === null ? 'is-absent' : undefined}>
            {reserveUsdc === null ? 'not reported' : `${reserveUsdc} USDC`}
            <span className="pool-src">20% of every fee, held against write-offs</span>
          </dd>
        </div>
        <div className="pool-fact">
          <dt>Advance rate</dt>
          <dd className={orgRateBps === null ? 'is-absent' : undefined}>
            {orgRateBps === null ? 'no organisation set' : `${(orgRateBps / 100).toFixed(0)}%`}
            <span className="pool-src">FloatPool.advanceRate, per organisation</span>
          </dd>
        </div>
      </dl>
      {orgRateBps === null ? (
        <p className="pool-note">
          The advance rate is per organisation. Set the treasury address to read this org's rate
          from FloatPool.
        </p>
      ) : null}
    </section>
  );
}
