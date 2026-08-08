/**
 * The landing page: what Snapfall is, to someone who has never seen it.
 *
 * "/" used to be the operator dashboard, so a first-time visitor -- on the deployed site, a judge
 * -- arrived at an instrument panel reading "no daemon connected" on four surfaces, with no
 * sentence anywhere saying what the product does. Strong evidence, no front door.
 *
 * THE FIRST VERSION OF THIS PAGE WAS BUILT IN THE WRONG MODE. It inherited the dashboard's
 * restraint -- muted hairlines, 9.5px annotation lettering, a lot of air -- onto a surface whose
 * job is to make a stranger care in ten seconds. That produced a 1700px whitepaper: one thin
 * strip of drawing adrift in prose, its labels set at annotation scale, and the most compelling
 * thing the product owns (a real escrow split, pool paid first) written out as body text
 * underneath the picture instead of being ON it.
 *
 * So: the drawing is the hero and it carries the figures. The viewBox is deliberately small (660
 * units across a ~860px column, so everything scales UP about 1.3x) because on this page the
 * lettering has to read across a room, not reward a lean-in. The prose around it is cut to what
 * a stranger needs and nothing more; the detail lives one click away on /audit.
 *
 * The direction contract's STORY clause is still the brief -- "the visitor sees money enter
 * escrow, an advance drawn against it under a cap they can see, spending reduce it, and a
 * waterfall repay the pool before the operator" -- and the path is drawn as a LOOP rather than a
 * left-to-right flow, because the loop is the product: the pool funds the work before the
 * customer has paid, and the same escrow locked at the start is what repays it at the end.
 *
 * Server component, no client JavaScript, no API call: the visitor who matters most arrives at a
 * deployment with no daemon behind it. Every figure is from the settlement linked at the bottom.
 */

import Link from 'next/link';
import deployment from '../../deployments/arc-testnet.json';

export const metadata = {
  title: 'Snapfall · the self-financing AI workforce',
  description:
    'Agents that borrow working capital against work they have not been paid for yet, and repay it automatically when the customer accepts.',
};

const EXPLORER = deployment.network.explorerUrl.replace(/\/$/, '');

/** One real settlement on Arc testnet. Its log ordering is the product's central claim. */
const PROOF = {
  hash: '0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b',
  escrow: '1.00',
  pool: '0.561',
  operator: '0.439',
};

/**
 * The money path, drawn once, carrying the real numbers.
 *
 * The figures are not decoration here: putting 0.561 on the leg that reaches the pool and 0.439
 * on the leg that reaches the operator is what makes "repaid first" a fact the eye can check
 * rather than a sentence to be believed.
 */
function MoneyPath() {
  return (
    <svg
      className="lp-svg"
      viewBox="0 0 700 320"
      role="img"
      aria-label={`The money path. A customer locks ${PROOF.escrow} USDC in escrow on chain. The capital pool advances working capital against it, capped at 10 percent per operator. Agents spend the advance to do the work. When the customer accepts, one transaction pays the pool ${PROOF.pool} USDC first, and the operator receives the remaining ${PROOF.operator}.`}
    >
      {/* the pool, above: it funds the work before anyone has been paid */}
      <rect x="205" y="8" width="250" height="60" className="lp-wall" />
      <text x="330" y="34" textAnchor="middle" className="lp-name">
        capital pool
      </text>
      <text x="330" y="54" textAnchor="middle" className="lp-note">
        capped at 10% per operator
      </text>

      {/* the advance, down into the work */}
      <g className="lp-pipe">
        <path d="M270 68 L270 126" />
        <path d="M270 126 l-5 -10 l10 0 z" className="lp-head" />
        <text x="262" y="102" textAnchor="end" className="lp-label">
          advance
        </text>
      </g>

      {/* the customer, and the escrow they lock */}
      <text x="14" y="150" className="lp-label">
        customer pays
      </text>
      <rect x="14" y="160" width="118" height="86" className="lp-wall" />
      <rect x="15" y="161" width="116" height="84" className="lp-fill" />
      <text x="73" y="186" textAnchor="middle" className="lp-name">
        escrow
      </text>
      <text x="73" y="230" textAnchor="middle" className="lp-fig">
        {PROOF.escrow}
      </text>
      <text x="73" y="264" textAnchor="middle" className="lp-note">
        locked on chain
      </text>

      {/* the work: agents spending the advance, never the escrow */}
      <rect x="205" y="160" width="250" height="86" className="lp-wall" />
      <text x="330" y="196" textAnchor="middle" className="lp-name">
        agents do the work
      </text>
      <text x="330" y="220" textAnchor="middle" className="lp-note">
        spending the advance, not the escrow
      </text>

      <g className="lp-pipe">
        <path d="M132 203 L201 203" />
        <path d="M201 203 l-10 -5 l0 10 z" className="lp-head" />
      </g>
      <g className="lp-pipe">
        <path d="M455 203 L521 203" />
        <path d="M521 203 l-10 -5 l0 10 z" className="lp-head" />
        <text x="488" y="193" textAnchor="middle" className="lp-label">
          accepts
        </text>
      </g>

      {/* leg one: the pool, made whole first. Full weight. */}
      <g className="lp-pipe">
        <path d="M525 203 L525 38 L459 38" />
        <path d="M459 38 l10 -5 l0 10 z" className="lp-head" />
        <text x="537" y="96" className="lp-fig">
          {PROOF.pool}
        </text>
        <text x="537" y="116" className="lp-label">
          1 · pool repaid first
        </text>
      </g>

      {/* leg two: whatever survives leg one. Dimmed, because the order is the point. */}
      <g className="lp-pipe lp-pipe-dim">
        <path d="M525 203 L525 288 L656 288" />
        <path d="M656 288 l-10 -5 l0 10 z" className="lp-head" />
        <text x="537" y="258" className="lp-fig">
          {PROOF.operator}
        </text>
        <text x="537" y="278" className="lp-label">
          2 · operator, second
        </text>
      </g>

      <text x="515" y="243" textAnchor="end" className="lp-note">
        one transaction
      </text>
    </svg>
  );
}

export default function LandingPage() {
  return (
    <div className="lp">
      <header className="lp-header">
        <p className="lp-brand">Snapfall</p>
        <h1 className="lp-title">The AI workforce that finances itself.</h1>
        <p className="lp-lede">
          Agents borrow working capital against work they have not been paid for yet, and repay it
          automatically the moment the customer accepts.
        </p>
        <div className="lp-actions">
          <Link className="lp-cta" href="/overview">
            Open the dashboard <span aria-hidden="true">→</span>
          </Link>
          <Link className="lp-alt" href="/audit">
            See what can be verified
          </Link>
        </div>
      </header>

      <section className="lp-figure" aria-labelledby="lp-path-title">
        <h2 className="lp-h2" id="lp-path-title">
          One job, from escrow to settlement
        </h2>
        <MoneyPath />
        <p className="lp-caption">
          Real figures, from one settlement on {deployment.network.name}. The pool is paid at log
          index 12 and the operator at log index 15 — same transaction, pool lower, so it cannot
          have been paid second. That ordering is <code>JobVault.acceptDelivery</code> calling{' '}
          <code>repayAdvance</code> before it transfers to the operator, in frozen code.{' '}
          <a className="lp-ref" href={`${EXPLORER}/tx/${PROOF.hash}`} target="_blank" rel="noreferrer">
            Check the transaction <span aria-hidden="true">↗</span>
          </a>
        </p>
      </section>

      <section className="lp-honest" aria-labelledby="lp-honest-title">
        <h2 className="lp-h2" id="lp-honest-title">
          What is not proven yet
        </h2>
        <p className="lp-body">
          A 0.04 USDC agent purchase has settled end to end on Arc through the x402 path,
          self-facilitated — its transaction is on the audit page. What is still not proven: the
          Circle Gateway facilitator path is built and has never run against the live service, and
          compliance screening is a labelled stub that fabricates no result. A product about
          verifiability should be easiest to check where it is weakest, so the full list is on the
          audit page beside the evidence.
        </p>
        <Link className="lp-alt" href="/audit">
          Read the gaps
        </Link>
      </section>

      <footer className="lp-foot">
        <span>
          {deployment.network.name} · chain {deployment.network.chainId}
        </span>
        <Link href="/overview">Dashboard</Link>
        <Link href="/float">Pool</Link>
        <Link href="/audit">Audit</Link>
      </footer>
    </div>
  );
}
