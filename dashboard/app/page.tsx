/**
 * The landing page: what Snapfall is, to someone who has never seen it.
 *
 * "/" used to be the operator dashboard, so a first-time visitor -- on the deployed site, a judge
 * -- arrived at an instrument panel reading "no daemon connected" on four surfaces, with no
 * sentence anywhere saying what the product does. Strong evidence, no front door.
 *
 * The page is the product's ledger, shown to a stranger: the money path as four numbered steps,
 * and then the thing the product owns -- a real escrow split, pool paid first -- drawn as the
 * same settlement bar the job detail page uses, carrying the real figures from one settlement on
 * Arc testnet. The split IS the argument, so it is drawn, not written out as prose to be
 * believed: 0.561 on the pool's leg and 0.439 on the operator's is what makes "repaid first" a
 * fact the eye can check. The detail lives one click away on /audit.
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

const POOL_PCT = (Number(PROOF.pool) / Number(PROOF.escrow)) * 100;

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
        {/* The money path as a numbered schedule: the order is the evidence, so it is numbered. */}
        <ol className="lp-steps">
          <li>
            <span>1</span>
            <p>
              <strong>The customer locks {PROOF.escrow} USDC in escrow</strong> on chain, before
              any work begins.
            </p>
          </li>
          <li>
            <span>2</span>
            <p>
              <strong>The capital pool advances working capital</strong> against the escrow, capped
              at 10% per operator.
            </p>
          </li>
          <li>
            <span>3</span>
            <p>
              <strong>Agents spend the advance to do the work.</strong> The escrow stays whole.
            </p>
          </li>
          <li>
            <span>4</span>
            <p>
              <strong>The customer accepts. One transaction splits the escrow:</strong>
            </p>
          </li>
        </ol>

        {/* The split, drawn as the same bar the job detail page uses: the pool's leg first and
            solid, the operator's whatever survives it. Segment widths are the actual ratio. */}
        <div className="lp-split">
          <div
            className="wf-track"
            role="img"
            aria-label={`Of ${PROOF.escrow} USDC held in escrow, ${PROOF.pool} was repaid to the capital pool first, and the operator received the remaining ${PROOF.operator}.`}
          >
            <div className="wf-pool" style={{ width: `${POOL_PCT}%` }} />
            <div className="wf-operator" style={{ width: `${100 - POOL_PCT}%` }} />
          </div>
          <dl className="wf-facts">
            <div>
              <dt>escrow held</dt>
              <dd>{PROOF.escrow} USDC</dd>
            </div>
            <div>
              <dt>1 · repaid to the pool, first</dt>
              <dd>{PROOF.pool} USDC</dd>
            </div>
            <div>
              <dt>2 · to the operator, second</dt>
              <dd>{PROOF.operator} USDC</dd>
            </div>
          </dl>
        </div>

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
