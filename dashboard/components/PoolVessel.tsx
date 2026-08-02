/**
 * The pool, drawn as a vessel.
 *
 * This is the first viewport the direction contract describes, and the reason the instrument
 * diagram was the right world for this product: FloatPool genuinely is a tank with capacity
 * limits. An advance may not exceed 10% of TVL for one org, total lending may not exceed 80% of
 * TVL pool-wide. Those are not decorative numbers — they are the thresholds a request is checked
 * against, and one that crosses them reverts with CapExceeded.
 *
 * So they are drawn as threshold lines on the vessel rather than printed as labels in a card. A
 * reader sees the headroom before the cap without doing arithmetic, which is the whole reason
 * engineers draw tanks instead of tabulating them.
 *
 * Utilisation is a chain read, not an indexer figure: FloatPool exposes totalOutstanding and
 * totalAssets, so 0% means nothing is drawn right now and is as verifiable as TVL. The only
 * genuine unknown is the window before the fetch returns, which reads as "reading chain" --
 * a different statement from "nothing is drawn", and the vessel keeps them apart.
 */

interface Props {
  /** Pool TVL as a 6dp decimal string, read live from FloatPool.totalAssets. */
  tvlUsdc: string | null;
  /**
   * Drawn principal in basis points of TVL, computed from FloatPool.totalOutstanding over
   * totalAssets (floatChain.ts:356). Null only while the fetch is in flight -- no indexer is
   * involved, so a returned 0 means nothing is drawn and is as verifiable as TVL itself.
   */
  utilizationBps: number | null;
  /**
   * The org's current advance rate in bps, from FloatPool.advanceRate.
   *
   * Null does NOT mean the read failed. loadFloatViews only issues the advanceRate call when an
   * organisation address is configured (floatChain.ts:312 and :335, fed by
   * SNAPFALL_TREASURY_ADDRESS in app/api/float/route.ts:24). With that unset the call is never
   * made, so the honest statement is that nothing was asked for -- not that the chain was asked
   * and could not answer.
   */
  orgRateBps: number | null;
  /** Fees accrued to the pool, 6dp decimal string, from FloatPool.feesAccrued. */
  feesAccruedUsdc: string | null;
  /** The first-loss reserve, 6dp decimal string: 20% of every fee. */
  reserveUsdc: string | null;
}

/* FloatPool.sol constants, drawn rather than described. */
const UTILISATION_CAP = 0.8; // UTILIZATION_CAP_BPS 8000, pool-wide
const ORG_CAP = 0.1; // ORG_EXPOSURE_CAP_BPS 1000, per org

const W = 560;
const H = 250;
const TANK_X = 200;
const TANK_W = 148;
const TANK_TOP = 30;
const TANK_BOT = 206;
const TANK_H = TANK_BOT - TANK_TOP;

/** y for a fraction of capacity, measured up from the base of the vessel. */
const levelY = (frac: number) => TANK_BOT - Math.max(0, Math.min(1, frac)) * TANK_H;

/* Threshold lettering is right-aligned into this column, with the leader running from LEAD_X to
   the tank wall. Both are left of TANK_X so cap annotations and the outlet labels can never
   collide the way they did when everything hung off the right edge. */
const LABEL_R = 162;
const LEAD_X = 168;

export default function PoolVessel({
  tvlUsdc,
  utilizationBps,
  orgRateBps,
  feesAccruedUsdc,
  reserveUsdc,
}: Props) {
  const loaded = utilizationBps !== null;
  const used = loaded ? utilizationBps / 10_000 : 0;
  const capY = levelY(UTILISATION_CAP);
  const orgY = levelY(ORG_CAP);
  const usedY = levelY(used);
  const capState = used >= UTILISATION_CAP ? 'at' : used >= UTILISATION_CAP - 0.1 ? 'near' : '';

  return (
    <figure className="vessel">
      <svg
        className="vessel-svg"
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={
          `Capital pool holding ${tvlUsdc ?? 'an unread amount of'} USDC. ` +
          (loaded
            ? `${(used * 100).toFixed(1)} percent is currently lent out. `
            : 'The current lending figure is still being read from the chain. ') +
          'Lending is capped at 80 percent of the pool overall, and 10 percent for any one operator. ' +
          (orgRateBps === null
            ? 'No organisation is configured, so no advance rate has been read for one.'
            : `The advance rate for this organisation is ${(orgRateBps / 100).toFixed(0)} percent.`)
        }
      >
        <rect x={TANK_X} y={TANK_TOP} width={TANK_W} height={TANK_H} className="v-wall" />

        {/* Held capital. A drawn level of zero is a real, checkable statement, so it is labelled
            rather than left as an empty box that reads like a failed render. The hatched state
            below is the narrower case: the chain read has not come back yet. */}
        {loaded ? (
          <rect
            x={TANK_X + 1}
            y={usedY}
            width={TANK_W - 2}
            height={TANK_BOT - usedY}
            className="v-fill"
          />
        ) : null}
        {loaded && used === 0 ? (
          <text
            className="v-empty"
            x={TANK_X + TANK_W / 2}
            y={TANK_BOT - 14}
            textAnchor="middle"
          >
            nothing drawn
          </text>
        ) : null}
        {!loaded ? (
          <g className="v-unknown">
            {[0.18, 0.34, 0.5].map((f) => (
              <line key={f} x1={TANK_X + 1} y1={levelY(f)} x2={TANK_X + TANK_W - 1} y2={levelY(f)} />
            ))}
            <text x={TANK_X + TANK_W / 2} y={levelY(0.34) - 10} textAnchor="middle">
              reading chain
            </text>
          </g>
        ) : null}

        {/* Threshold lines: the caps, drawn where they actually sit on the tank. */}
        <g className={`v-threshold ${capState}`}>
          <line x1={LEAD_X} y1={capY} x2={TANK_X + 4} y2={capY} />
          <text x={LABEL_R} y={capY + 3.5} textAnchor="end">
            80% utilisation cap
          </text>
        </g>
        <g className="v-threshold">
          <line x1={LEAD_X} y1={orgY} x2={TANK_X + 4} y2={orgY} />
          <text x={LABEL_R} y={orgY + 3.5} textAnchor="end">
            10% per-operator cap
          </text>
        </g>

        {/* Inlet: the escrowed receivable the advance is drawn against. */}
        <g className="v-pipe">
          <path d={`M ${TANK_X + TANK_W / 2} 8 L ${TANK_X + TANK_W / 2} ${TANK_TOP}`} />
          <text x={TANK_X + TANK_W / 2 + 8} y="14">escrow</text>
        </g>

        {/* Outlet: the waterfall. Pool repaid first, operator second — drawn in that order. */}
        <g className="v-pipe">
          <path
            d={`M ${TANK_X + TANK_W} ${TANK_BOT - 30} L ${TANK_X + TANK_W + 40} ${TANK_BOT - 30}`}
          />
          <text x={TANK_X + TANK_W + 46} y={TANK_BOT - 26}>pool repaid first</text>
        </g>
        <g className="v-pipe v-pipe-dim">
          <path
            d={`M ${TANK_X + TANK_W} ${TANK_BOT - 10} L ${TANK_X + TANK_W + 40} ${TANK_BOT - 10}`}
          />
          <text x={TANK_X + TANK_W + 46} y={TANK_BOT - 6}>operator, second</text>
        </g>

        {/* A dimension line, which on a drawing always says what it dimensions. This one carried
            no label at all, so it measured nothing and read as decoration under the tank. */}
        <g className="v-callout">
          <line x1={TANK_X} y1={TANK_BOT + 14} x2={TANK_X + TANK_W} y2={TANK_BOT + 14} />
          <line x1={TANK_X} y1={TANK_BOT + 10} x2={TANK_X} y2={TANK_BOT + 18} />
          <line x1={TANK_X + TANK_W} y1={TANK_BOT + 10} x2={TANK_X + TANK_W} y2={TANK_BOT + 18} />
          <text x={TANK_X + TANK_W / 2} y={TANK_BOT + 30} textAnchor="middle">
            total pool capital
          </text>
        </g>
      </svg>

      <figcaption className="vessel-read">
        <div className="vessel-primary">
          <span className="vessel-figure">{tvlUsdc ?? '—'}</span>
          <span className="vessel-unit">USDC</span>
        </div>
        <p className="vessel-source">pool capital · read live from FloatPool on Arc testnet</p>
        {/* Every figure names its source, per the direction contract's FIRST VIEWPORT clause and
            PRODUCT principle 1. These four used to live in a separate stat-tile grid below the
            drawing -- the exact template the contract's THESIS names as refused -- where two of
            them simply restated the vessel and none of them said where it came from. */}
        <dl className="vessel-facts">
          <div>
            <dt>lent out</dt>
            <dd className={loaded ? undefined : 'is-absent'}>
              <span className="vessel-val">
                {loaded ? `${(used * 100).toFixed(1)}%` : 'reading chain'}
              </span>
              <span className="vessel-src">totalOutstanding ÷ totalAssets · cap 80%</span>
            </dd>
          </div>
          <div>
            <dt>fees accrued</dt>
            <dd className={feesAccruedUsdc === null ? 'is-absent' : undefined}>
              <span className="vessel-val">
                {feesAccruedUsdc === null ? 'not reported' : `${feesAccruedUsdc} USDC`}
              </span>
              <span className="vessel-src">FloatPool.feesAccrued · 2% of each principal</span>
            </dd>
          </div>
          <div>
            <dt>first-loss reserve</dt>
            <dd className={reserveUsdc === null ? 'is-absent' : undefined}>
              <span className="vessel-val">
                {reserveUsdc === null ? 'not reported' : `${reserveUsdc} USDC`}
              </span>
              <span className="vessel-src">20% of every fee, held against write-offs</span>
            </dd>
          </div>
          <div>
            <dt>advance rate</dt>
            <dd className={orgRateBps === null ? 'is-absent' : undefined}>
              <span className="vessel-val">
                {orgRateBps === null ? 'no organisation set' : `${(orgRateBps / 100).toFixed(0)}%`}
              </span>
              <span className="vessel-src">FloatPool.advanceRate, per organisation</span>
            </dd>
          </div>
        </dl>
        {orgRateBps === null ? (
          <p className="vessel-note">
            The advance rate is per organisation. Set the treasury address to read this org's rate
            from FloatPool.
          </p>
        ) : null}
      </figcaption>
    </figure>
  );
}
