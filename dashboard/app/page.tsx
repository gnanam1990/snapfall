/**
 * The landing page: what Snapfall is, to someone who has never seen it.
 *
 * This route used to be the operator dashboard, which meant a first-time visitor -- on the
 * deployed site, a judge -- arrived at an instrument panel reading "no daemon connected" on four
 * surfaces, with no sentence anywhere saying what the product does. The evidence was strong and
 * there was no front door.
 *
 * The direction contract's STORY clause is exactly this page's brief: "The visitor sees money
 * enter escrow, an advance drawn against it under a cap they can see, spending reduce it, and a
 * waterfall repay the pool before the operator." So the money path is drawn, once, whole. The
 * dashboard surfaces are the instruments on that path; this is the sheet they sit on.
 *
 * A server component with no client JavaScript and no API call, because the one visitor who
 * matters most arrives at a deployment with no daemon behind it.
 *
 * Nothing here is a claim the product cannot support. The settlement figures are read from a real
 * transaction on Arc testnet, linked; the honest gaps live on /audit and this page points at them
 * rather than talking around them.
 */

import Link from 'next/link';
import deployment from '../../deployments/arc-testnet.json';

export const metadata = {
  title: 'Snapfall · the self-financing AI workforce',
  description:
    'Agents that borrow working capital against work they have not been paid for yet, and repay it automatically when the customer accepts.',
};

const EXPLORER = deployment.network.explorerUrl.replace(/\/$/, '');

/** The settlement whose log ordering is the product's central claim. See /audit. */
const PROOF = {
  hash: '0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b',
  escrow: '1.00',
  pool: '0.561',
  operator: '0.439',
};

/**
 * The money path, drawn once.
 *
 * Deliberately the whole loop rather than a linear flow, because the loop is the product: the
 * pool funds the work before the customer has paid, and the same escrow that was locked at the
 * start is what repays it at the end. A left-to-right arrow would hide that.
 */
function MoneyPath() {
  return (
    <svg
      className="lp-svg"
      viewBox="0 0 880 300"
      role="img"
      aria-label="The money path. A customer funds an escrow on chain. The capital pool advances working capital against that escrow, under a cap. Agents spend the advance to do the work. When the customer accepts, one transaction repays the pool in full first, and the operator receives what remains."
    >
      {/* the pool, above: it funds the work before anyone has been paid */}
      <rect x="330" y="18" width="200" height="62" className="lp-wall" />
      <text x="430" y="44" textAnchor="middle" className="lp-name">
        capital pool
      </text>
      <text x="430" y="62" textAnchor="middle" className="lp-note">
        capped at 10% per operator, 80% pool-wide
      </text>

      {/* the advance, drawn downward into the work */}
      <g className="lp-pipe">
        <path d="M400 80 L400 132" />
        <path d="M400 132 l-4 -8 l8 0 z" className="lp-head" />
        <text x="392" y="112" textAnchor="end" className="lp-label">
          advance
        </text>
      </g>

      {/* the repayment, drawn upward: first leg of the waterfall */}
      <g className="lp-pipe">
        <path d="M700 210 L700 50 L534 50" />
        <path d="M534 50 l8 -4 l0 8 z" className="lp-head" />
        <text x="710" y="128" className="lp-label">
          1 · pool repaid first
        </text>
      </g>

      {/* the customer, and the escrow they lock */}
      <text x="16" y="164" className="lp-name">
        customer
      </text>
      <g className="lp-pipe">
        <path d="M16 178 L148 178" />
        <path d="M148 178 l-8 -4 l0 8 z" className="lp-head" />
        <text x="20" y="196" className="lp-label">
          pays into escrow
        </text>
      </g>

      <rect x="152" y="144" width="92" height="70" className="lp-wall" />
      <rect x="153" y="145" width="90" height="68" className="lp-fill" />
      <text x="198" y="184" textAnchor="middle" className="lp-name">
        escrow
      </text>
      <text x="198" y="234" textAnchor="middle" className="lp-note">
        held on chain
      </text>

      {/* the work: agents spending the advance */}
      <rect x="330" y="144" width="200" height="70" className="lp-wall" />
      <text x="430" y="174" textAnchor="middle" className="lp-name">
        agents do the work
      </text>
      <text x="430" y="194" textAnchor="middle" className="lp-note">
        spending the advance, not the escrow
      </text>

      <g className="lp-pipe">
        <path d="M244 178 L326 178" />
        <path d="M326 178 l-8 -4 l0 8 z" className="lp-head" />
      </g>
      <g className="lp-pipe">
        <path d="M530 178 L696 178" />
        <path d="M696 178 l-8 -4 l0 8 z" className="lp-head" />
        <text x="613" y="170" textAnchor="middle" className="lp-label">
          customer accepts
        </text>
      </g>

      {/* the operator's leg, dimmed: whatever survives leg one */}
      <g className="lp-pipe lp-pipe-dim">
        <path d="M700 210 L700 262 L806 262" />
        <path d="M806 262 l-8 -4 l0 8 z" className="lp-head" />
        <text x="710" y="282" className="lp-label">
          2 · operator, second
        </text>
      </g>

      <text x="700" y="228" textAnchor="middle" className="lp-note">
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
          Agents borrow working capital against work they have not been paid for yet, spend it to
          do the job, and repay it automatically the moment the customer accepts. Every movement
          settles on chain, so someone who does not trust the operator can still check it.
        </p>
        <p className="lp-tag">capital in a snap, settlement in a waterfall</p>
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
          How the money moves
        </h2>
        <MoneyPath />
      </section>

      <section className="lp-proof" aria-labelledby="lp-proof-title">
        <h2 className="lp-h2" id="lp-proof-title">
          The claim, and how to check it
        </h2>
        <p className="lp-body">
          The pool is repaid in full <strong>before</strong> the operator receives anything, in the
          same transaction. That is not a promise about intent — it is contract control flow, and
          it is visible in the logs of a settlement already on {deployment.network.name}.
        </p>
        <ol className="lp-legs">
          <li>
            <span className="lp-leg-i">log 12</span>
            <span className="lp-leg-to">capital pool</span>
            <span className="lp-leg-amt">{PROOF.pool} USDC</span>
          </li>
          <li>
            <span className="lp-leg-i">log 15</span>
            <span className="lp-leg-to">operator</span>
            <span className="lp-leg-amt">{PROOF.operator} USDC</span>
          </li>
        </ol>
        <p className="lp-body">
          {PROOF.escrow} USDC of escrow, divided. The pool is paid at the lower log index, so it
          cannot have been paid second. <code>JobVault.acceptDelivery</code> calls{' '}
          <code>repayAdvance</code> before it transfers to the operator, and the contracts are
          frozen.
        </p>
        <a className="lp-ref" href={`${EXPLORER}/tx/${PROOF.hash}`} target="_blank" rel="noreferrer">
          <code>{`${PROOF.hash.slice(0, 16)}…${PROOF.hash.slice(-8)}`}</code>
          <span aria-hidden="true"> ↗</span>
        </a>
      </section>

      <section className="lp-honest" aria-labelledby="lp-honest-title">
        <h2 className="lp-h2" id="lp-honest-title">
          What is not proven yet
        </h2>
        <p className="lp-body">
          No agent purchase has settled through the x402 payment path, no end-to-end run has been
          logged, and compliance screening is a labelled stub that fabricates no result. Those are
          listed in full, with the rest, on the audit page — a product about verifiability should
          be easiest to check where it is weakest.
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
