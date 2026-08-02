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
 * Honest about absence: with no indexer running, utilisation is genuinely unknown rather than
 * zero, so the vessel says so instead of drawing an empty tank that would read as a fact.
 */

interface Props {
  /** Pool TVL as a 6dp decimal string, read live from FloatPool.totalAssets. */
  tvlUsdc: string | null;
  /** Drawn principal in basis points of TVL, or null when the indexer has not reported. */
  utilizationBps: number | null;
  /** The org's current advance rate in bps, from FloatPool.advanceRate. */
  orgRateBps: number | null;
}

/* FloatPool.sol constants, drawn rather than described. */
const UTILISATION_CAP = 0.8; // UTILIZATION_CAP_BPS 8000, pool-wide
const ORG_CAP = 0.1; // ORG_EXPOSURE_CAP_BPS 1000, per org

const W = 560;
const H = 250;
const TANK_X = 34;
const TANK_W = 148;
const TANK_TOP = 30;
const TANK_BOT = 206;
const TANK_H = TANK_BOT - TANK_TOP;

/** y for a fraction of capacity, measured up from the base of the vessel. */
const levelY = (frac: number) => TANK_BOT - Math.max(0, Math.min(1, frac)) * TANK_H;

export default function PoolVessel({ tvlUsdc, utilizationBps, orgRateBps }: Props) {
  const known = utilizationBps !== null;
  const used = known ? utilizationBps / 10_000 : 0;
  const capY = levelY(UTILISATION_CAP);
  const orgY = levelY(ORG_CAP);
  const usedY = levelY(used);

  return (
    <figure className="vessel">
      <svg
        className="vessel-svg"
        viewBox={`0 0 ${W} ${H}`}
        role="img"
        aria-label={
          `Capital pool holding ${tvlUsdc ?? 'an unread amount of'} USDC. ` +
          (known
            ? `${(used * 100).toFixed(1)} percent is currently lent out. `
            : 'Current lending is unknown until the chain indexer reports. ') +
          'Lending is capped at 80 percent of the pool overall, and 10 percent for any one operator.'
        }
      >
        <rect x={TANK_X} y={TANK_TOP} width={TANK_W} height={TANK_H} className="v-wall" />

        {/* Held capital, drawn only when the level is actually known: an empty tank is a claim,
            and "nothing is lent" is a different statement from "nobody has told us yet". */}
        {known ? (
          <rect
            x={TANK_X + 1}
            y={usedY}
            width={TANK_W - 2}
            height={TANK_BOT - usedY}
            className="v-fill"
          />
        ) : (
          <g className="v-unknown">
            {[0.18, 0.34, 0.5].map((f) => (
              <line key={f} x1={TANK_X + 1} y1={levelY(f)} x2={TANK_X + TANK_W - 1} y2={levelY(f)} />
            ))}
          </g>
        )}

        {/* Threshold lines: the caps, drawn where they actually sit on the tank. */}
        <g className="v-threshold">
          <line x1={TANK_X - 9} y1={capY} x2={TANK_X + TANK_W + 74} y2={capY} />
          <text x={TANK_X + TANK_W + 80} y={capY + 3.5}>80% utilisation cap</text>
        </g>
        <g className="v-threshold v-threshold-soft">
          <line x1={TANK_X - 9} y1={orgY} x2={TANK_X + TANK_W + 74} y2={orgY} />
          <text x={TANK_X + TANK_W + 80} y={orgY + 3.5}>10% per-operator cap</text>
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

        {/* Callout leader under the vessel: the dimension the figure below belongs to. */}
        <g className="v-callout">
          <line x1={TANK_X} y1={TANK_BOT + 14} x2={TANK_X + TANK_W} y2={TANK_BOT + 14} />
          <line x1={TANK_X} y1={TANK_BOT + 10} x2={TANK_X} y2={TANK_BOT + 18} />
          <line x1={TANK_X + TANK_W} y1={TANK_BOT + 10} x2={TANK_X + TANK_W} y2={TANK_BOT + 18} />
        </g>
      </svg>

      <figcaption className="vessel-read">
        <div className="vessel-primary">
          <span className="vessel-figure">{tvlUsdc ?? '—'}</span>
          <span className="vessel-unit">USDC</span>
        </div>
        <p className="vessel-source">pool capital · read live from FloatPool on Arc testnet</p>
        <dl className="vessel-facts">
          <div>
            <dt>lent out</dt>
            <dd>{known ? `${(used * 100).toFixed(1)}%` : 'awaiting indexer'}</dd>
          </div>
          <div>
            <dt>advance rate</dt>
            <dd>{orgRateBps === null ? 'unavailable' : `${(orgRateBps / 100).toFixed(0)}%`}</dd>
          </div>
        </dl>
      </figcaption>
    </figure>
  );
}
