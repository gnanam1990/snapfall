import Link from 'next/link';
import GuideToc, { type TocItem } from '@/components/GuideToc';
import { Band, Waterfall } from '@/components/ui';
import { contractAddress, deploymentFor, networkLabel, networkName, type NetworkName } from '@/lib/network';

export const metadata = { title: 'Guide · Snapfall' };

/**
 * The guide: how Snapfall works end to end, for every person who touches it.
 *
 * A reading surface, so it is static and server-rendered: the only live values are the contract
 * addresses this build reads, taken from the same deployment record the rest of the dashboard
 * uses. Figures that change (caps, pause state, the pool) are linked to the pages that read them
 * from chain instead of being copied here, where they would go stale. The worked example is
 * labelled as one; the network status table carries its date.
 */

const TOC: TocItem[] = [
  { id: 'what', label: 'What Snapfall does' },
  { id: 'money', label: 'The money, worked through' },
  { id: 'who', label: 'Who uses it' },
  { id: 'lifecycle', label: 'The life of a job' },
  { id: 'visitors', label: 'For visitors' },
  { id: 'owner', label: 'For the owner' },
  { id: 'customers', label: 'For customers' },
  { id: 'lps', label: 'For liquidity providers' },
  { id: 'verify', label: 'Check it yourself' },
  { id: 'safety', label: 'Safety controls' },
  { id: 'status', label: 'Status on this network' },
  { id: 'glossary', label: 'Glossary' },
];

const STATUS_DATE = '16 Nov 2026';

type Row = { what: string; state: 'yes' | 'no' | 'partial'; note: string };

const STATUS: Record<NetworkName, Row[]> = {
  'arc-testnet': [
    { what: 'Public chain views', state: 'yes', note: 'Jobs, activity, the pool, audit and the cascade all read from the contracts.' },
    { what: 'Owner sign-in and approvals', state: 'yes', note: 'Works wherever an owner daemon is connected to the dashboard.' },
    { what: 'Advances from the pool', state: 'yes', note: 'Proven in full runs with a testnet treasury key. The hosted testnet daemon holds no key, so it cannot sign one.' },
    { what: 'Agent purchases over x402', state: 'yes', note: 'Self-facilitated settlement. The Circle Gateway facilitator has not run against the live service.' },
    { what: 'Customer pays with a passkey wallet', state: 'partial', note: 'Proven with a Circle test client key. Offered only where the dashboard’s host sets one.' },
    { what: 'Settlement waterfall', state: 'yes', note: 'Two full runs, pool repaid first, on 16 Nov 2026.' },
    { what: 'Contracts with the operator allowlist', state: 'yes', note: 'The set redeployed on 16 Nov 2026.' },
  ],
  'arc-mainnet': [
    { what: 'Public chain views', state: 'yes', note: 'Jobs, activity, the pool, audit and the cascade all read from the contracts.' },
    { what: 'Owner sign-in and approvals', state: 'partial', note: 'Available where an owner daemon is connected. The daemon holds no signing keys yet.' },
    { what: 'Advances from the pool', state: 'no', note: 'No treasury signer yet. It will be a Circle developer-controlled wallet, never a raw key.' },
    { what: 'Agent purchases over x402', state: 'no', note: 'No mainnet settlement path is configured.' },
    { what: 'Customer pays with a passkey wallet', state: 'no', note: 'Needs a live Circle client key for this domain.' },
    { what: 'Settlement waterfall', state: 'partial', note: 'One 0.50 USDC self-test job between the owner’s own wallets settled with no advance. It was not a sale.' },
    { what: 'Pool deposits', state: 'no', note: 'The pool is paused and holds nothing.' },
    { what: 'Contracts with the operator allowlist', state: 'no', note: 'The live set predates it and stays paused until a fixed set is deployed.' },
  ],
};

const STATE_WORD: Record<Row['state'], string> = { yes: 'Working', partial: 'Partly', no: 'Not yet' };

function Address({ explorer, address }: { explorer: string; address: string | undefined }) {
  if (!address) return <span className="guide-muted">Not deployed</span>;
  return (
    <a className="guide-addr mono" href={`${explorer}/address/${address}`} target="_blank" rel="noreferrer">
      {address}
    </a>
  );
}

export default function GuidePage() {
  const net = networkName();
  const label = networkLabel(net);
  const dep = deploymentFor(net);
  const explorer = dep.network.explorerUrl;
  const vault = contractAddress('jobVault');
  const pool = contractAddress('floatPool');
  const anchor = contractAddress('auditAnchor');
  const usdcAddr = contractAddress('usdc');
  const rpc = dep.network.rpcUrl;

  return (
    <>
      <Band title="Guide" lede="How Snapfall works, end to end." meta={<span>{label}, chain {dep.network.chainId}</span>} />

      <div className="wrap guide">
        <GuideToc items={TOC} />

        <article className="guide-body">
          <section id="what" className="guide-sec">
            <h2>What Snapfall does</h2>
            <p>
              Snapfall runs a business whose AI agents take paid jobs. A customer’s payment is held in escrow on chain. As
              soon as the job is funded, a capital pool advances part of that payment to the operator as working capital,
              so the agents can buy what the job needs before the customer has paid anyone. When the customer accepts the
              delivery, one transaction repays the pool first and gives the operator what is left.
            </p>
            <dl className="guide-pair">
              <div>
                <dt>The snap</dt>
                <dd>Working capital arrives the moment a job is funded, borrowed against the escrowed payment.</dd>
              </div>
              <div>
                <dt>The fall</dt>
                <dd>On acceptance, the payment falls through a waterfall: the pool’s principal and fee first, the operator last.</dd>
              </div>
            </dl>
            <p>
              Every accepted job raises the operator’s advance rate on chain, so the next job unlocks more. Every figure on
              this dashboard is read from the contracts and linked to its transaction. Nothing here is sample data.
            </p>
          </section>

          <section id="money" className="guide-sec">
            <h2>The money, worked through</h2>
            <p>
              This is a worked example, not a real job: a 25.00 USDC job from an operator at the starting advance rate of
              50%. The arithmetic is the contracts’ own, in whole micro-USDC. Real settlements are on the{' '}
              <Link className="link" href="/jobs">Jobs</Link> page.
            </p>
            <figure className="guide-figure">
              <Waterfall paid={25} pool={12.75} operator={12.25} />
              <figcaption>Customers paid 25.00. The pool is repaid 12.75 first, and the operator keeps 12.25.</figcaption>
            </figure>
            <div className="table-wrap guide-table is-stack">
              <table className="table">
                <thead>
                  <tr>
                    <th>When</th>
                    <th>Movement</th>
                    <th>Call</th>
                    <th className="r">USDC</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td data-label="When"><span>Funded</span></td>
                    <td data-label="Movement"><span>Customer to escrow</span></td>
                    <td data-label="Call"><span><code>JobVault.fund</code></span></td>
                    <td data-label="USDC" className="r"><span>25.00</span></td>
                  </tr>
                  <tr>
                    <td data-label="When"><span>Funded</span></td>
                    <td data-label="Movement"><span>Pool to operator, the advance (50% of 25.00)</span></td>
                    <td data-label="Call"><span><code>FloatPool.requestAdvance</code></span></td>
                    <td data-label="USDC" className="r guide-pool"><span>12.50</span></td>
                  </tr>
                  <tr>
                    <td data-label="When"><span>Accepted</span></td>
                    <td data-label="Movement"><span>Escrow to pool: principal 12.50 plus the 2% fee 0.25</span></td>
                    <td data-label="Call"><span><code>JobVault.acceptDelivery</code></span></td>
                    <td data-label="USDC" className="r guide-pool"><span>12.75</span></td>
                  </tr>
                  <tr>
                    <td data-label="When"><span>Accepted</span></td>
                    <td data-label="Movement"><span>Escrow to operator, the remainder</span></td>
                    <td data-label="Call"><span><code>JobVault.acceptDelivery</code></span></td>
                    <td data-label="USDC" className="r guide-op"><span>12.25</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p>
              Of the 0.25 fee, 20% (0.05) goes into the pool’s first-loss reserve. The operator received 12.50 up front and
              12.25 at the end, so the job cost it 0.25 in fees.
            </p>
            <h3>The rules the contracts enforce</h3>
            <div className="table-wrap guide-table">
              <table className="table">
                <tbody>
                  <tr><th scope="row">Starting advance rate</th><td>50% of the customer’s payment</td></tr>
                  <tr><th scope="row">After each accepted job</th><td>+5 points, up to 85%</td></tr>
                  <tr><th scope="row">After each write-off</th><td>−15 points, never below 30%</td></tr>
                  <tr><th scope="row">Fee</th><td>2% of the advance, 20% of it kept as a first-loss reserve</td></tr>
                  <tr><th scope="row">One operator’s exposure</th><td>At most 10% of the pool</td></tr>
                  <tr><th scope="row">All advances together</th><td>At most 80% of the pool</td></tr>
                  <tr><th scope="row">Job and advance size caps</th><td>Set by the admin; read live on <Link className="link" href="/audit">Audit</Link></td></tr>
                </tbody>
              </table>
            </div>
          </section>

          <section id="who" className="guide-sec">
            <h2>Who uses it</h2>
            <div className="table-wrap guide-table is-stack">
              <table className="table">
                <thead>
                  <tr>
                    <th>Who</th>
                    <th>What they do</th>
                    <th>Where</th>
                    <th>Credential</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td data-label="Who"><span>Visitor</span></td>
                    <td data-label="Does"><span>Checks that the business is real</span></td>
                    <td data-label="Where"><span><Link href="/overview">Overview</Link>, <Link href="/jobs">Jobs</Link>, <Link href="/audit">Audit</Link></span></td>
                    <td data-label="Credential"><span>None</span></td>
                  </tr>
                  <tr>
                    <td data-label="Who"><span>Owner</span></td>
                    <td data-label="Does"><span>Runs the agents and decides on their purchases</span></td>
                    <td data-label="Where"><span><Link href="/approvals">Approvals</Link>, <Link href="/workforce">Workforce</Link></span></td>
                    <td data-label="Credential"><span>Owner secret</span></td>
                  </tr>
                  <tr>
                    <td data-label="Who"><span>Customer</span></td>
                    <td data-label="Does"><span>Pays for one job and accepts the delivery</span></td>
                    <td data-label="Where"><span>A pay link and an accept link</span></td>
                    <td data-label="Credential"><span>Their own passkey wallet</span></td>
                  </tr>
                  <tr>
                    <td data-label="Who"><span>Liquidity provider</span></td>
                    <td data-label="Does"><span>Funds the pool and earns its fees</span></td>
                    <td data-label="Where"><span>The pool contract; history on <Link href="/float">Capital pool</Link></span></td>
                    <td data-label="Credential"><span>Their wallet, once allowlisted</span></td>
                  </tr>
                  <tr>
                    <td data-label="Who"><span>Admin</span></td>
                    <td data-label="Does"><span>Sets caps, allowlists and the emergency pause</span></td>
                    <td data-label="Where"><span>The contracts directly</span></td>
                    <td data-label="Credential"><span>The admin wallet</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p>
              Each deployment has one owner. A customer never sees the owner’s internals or another customer’s job.
            </p>
          </section>

          <section id="lifecycle" className="guide-sec">
            <h2>The life of a job</h2>
            <ol className="guide-steps">
              <li>
                <h3>Fund</h3>
                <p>The operator creates the job and names the customer’s wallet. The customer pays the full price into escrow. Only the named customer can fund it.</p>
                <p className="guide-meta"><code>JobVault.createJob</code>, <code>JobVault.fund</code></p>
              </li>
              <li>
                <h3>Snap</h3>
                <p>The daemon proposes an advance, and the owner approves it. The pool sends the operator its rate’s share of the price, within the exposure limits.</p>
                <p className="guide-meta"><code>FloatPool.requestAdvance</code>, event <code>AdvanceIssued</code></p>
              </li>
              <li>
                <h3>Spend</h3>
                <p>Agents buy the data and services the job needs, paying sellers over x402. Purchases within policy go through on their own.</p>
              </li>
              <li>
                <h3>Escalate</h3>
                <p>A purchase above policy waits for the owner. The owner can approve it, refuse it, or ask for a cheaper alternative with a reason.</p>
              </li>
              <li>
                <h3>Adapt</h3>
                <p>When the owner refuses on cost, the agent looks for a cheaper source instead of routing around the decision.</p>
              </li>
              <li>
                <h3>Deliver</h3>
                <p>A quality check passes the draft, and the delivery’s hash is written on chain.</p>
                <p className="guide-meta"><code>JobVault.submitDelivery</code></p>
              </li>
              <li>
                <h3>Accept</h3>
                <p>The customer accepts from their link. In one transaction the pool is repaid principal and fee, then the operator receives the rest.</p>
                <p className="guide-meta"><code>JobVault.acceptDelivery</code>, event <code>JobSettled</code></p>
              </li>
              <li>
                <h3>Rate rises</h3>
                <p>The contract raises the operator’s advance rate, so the next job can borrow more.</p>
                <p className="guide-meta">event <code>RateChanged</code></p>
              </li>
            </ol>
            <p>
              If a job is never delivered it is refunded. The customer gets the full payment back first, and the pool absorbs
              any open advance from the operator’s bond, then the first-loss reserve, then the pool itself.
            </p>
          </section>

          <section id="visitors" className="guide-sec">
            <h2>For visitors</h2>
            <ol className="guide-list">
              <li><Link className="link" href="/overview">Overview</Link> shows what customers paid, what went back to the pool and what the operator kept. The cascade is built from the settled jobs; drag it to turn it.</li>
              <li><Link className="link" href="/jobs">Jobs</Link> and <Link className="link" href="/activity">Activity</Link> list every job and every movement of money, each linked to its transaction.</li>
              <li><Link className="link" href="/float">Capital pool</Link> shows the pool’s size, how much is lent, the advance rate over time and every deposit.</li>
              <li><Link className="link" href="/audit">Audit</Link> reads the safety controls live and lists what has not been proven.</li>
            </ol>
          </section>

          <section id="owner" className="guide-sec">
            <h2>For the owner</h2>
            <ol className="guide-list">
              <li>Sign in on <Link className="link" href="/settings">Settings</Link> or <Link className="link" href="/approvals">Approvals</Link> with the owner secret configured on this dashboard’s host. A session lasts 12 hours.</li>
              <li><strong>Approvals</strong> lists every purchase and advance waiting for you, with the amount, the seller and the agent’s reason. Approve, refuse, or ask for an alternative; a refusal’s reason is passed to the agent.</li>
              <li>On a job’s page, once the delivery is ready, create the customer’s accept link and send it to them. It is shown once; creating it again replaces it.</li>
              <li><strong>Workforce</strong> shows which agents are hired, their budgets, and whether each may sign or borrow.</li>
            </ol>
            <p className="guide-note">
              Where this dashboard has no owner daemon connected, the owner cards say so. The chain views keep working.
            </p>
          </section>

          <section id="customers" className="guide-sec">
            <h2>For customers</h2>
            <ol className="guide-list">
              <li>Open the pay link the operator sends and sign in with a passkey. It shows your wallet address; the operator names that address when creating your job.</li>
              <li>Fund the job from the same link. If the escrow is not yours, the page says so and nothing can be sent.</li>
              <li>When the work is ready, open the accept link. It shows your job’s status and an Accept button. Accepting settles the job and gives you a receipt.</li>
            </ol>
            <p className="guide-note">You only ever see your own job. Paying needs a Circle client key for this network; see the status table below.</p>
          </section>

          <section id="lps" className="guide-sec">
            <h2>For liquidity providers</h2>
            <p>
              The pool lends to operators against escrowed payments and earns 2% on each advance. Deposits are open only to
              addresses the admin has allowlisted. These are the calls, with this network’s addresses filled in.
            </p>
            <h3>Approve USDC to the pool</h3>
            <pre className="guide-code"><code>{`cast send ${usdcAddr ?? '<usdc>'} \\
  "approve(address,uint256)" ${pool ?? '<floatPool>'} <amount-in-micro-usdc> \\
  --account <your-keystore> --rpc-url ${rpc}`}</code></pre>
            <h3>Deposit</h3>
            <pre className="guide-code"><code>{`cast send ${pool ?? '<floatPool>'} \\
  "deposit(uint256,address)" <amount-in-micro-usdc> <your-address> \\
  --account <your-keystore> --rpc-url ${rpc}`}</code></pre>
            <h3>Withdraw</h3>
            <pre className="guide-code"><code>{`cast send ${pool ?? '<floatPool>'} \\
  "withdraw(uint256,address,address)" <amount-in-micro-usdc> <your-address> <your-address> \\
  --account <your-keystore> --rpc-url ${rpc}`}</code></pre>
            <p className="guide-note">
              USDC has 6 decimals: 1.00 USDC is <code>1000000</code>. USDC is also the gas token on Arc, so keep a little
              more than you deposit. Only idle money can be withdrawn: what is lent out returns as jobs settle.
            </p>
          </section>

          <section id="verify" className="guide-sec">
            <h2>Check it yourself</h2>
            <p>
              Nothing on this dashboard needs to be taken on trust. These are the contracts it reads on {label}, and the
              events every figure comes from.
            </p>
            <div className="table-wrap guide-table">
              <table className="table">
                <tbody>
                  <tr><th scope="row">JobVault, the escrow</th><td><Address explorer={explorer} address={vault} /></td></tr>
                  <tr><th scope="row">FloatPool, the capital pool</th><td><Address explorer={explorer} address={pool} /></td></tr>
                  <tr><th scope="row">AuditAnchor</th><td><Address explorer={explorer} address={anchor} /></td></tr>
                  <tr><th scope="row">USDC</th><td><Address explorer={explorer} address={usdcAddr} /></td></tr>
                </tbody>
              </table>
            </div>
            <h3>The events behind the figures</h3>
            <div className="table-wrap guide-table">
              <table className="table">
                <tbody>
                  <tr><th scope="row"><code>JobCreated</code>, <code>JobFunded</code></th><td>A job and its escrowed payment</td></tr>
                  <tr><th scope="row"><code>AdvanceIssued</code></th><td>The snap: principal, fee and the rate it was issued at</td></tr>
                  <tr><th scope="row"><code>JobSettled</code></th><td>The fall: what the pool was repaid and what the operator kept</td></tr>
                  <tr><th scope="row"><code>AdvanceRepaid</code></th><td>The pool’s side of a settlement, including the reserve’s cut</td></tr>
                  <tr><th scope="row"><code>RateChanged</code></th><td>An operator’s new advance rate</td></tr>
                  <tr><th scope="row"><code>Deposited</code>, <code>Withdrawn</code></th><td>Liquidity in and out of the pool</td></tr>
                </tbody>
              </table>
            </div>
            <h3>Read a job and the rate directly</h3>
            <pre className="guide-code"><code>{`cast call ${vault ?? '<jobVault>'} \\
  "jobStatus(bytes32)(uint8)" <job-id> --rpc-url ${rpc}

cast call ${pool ?? '<floatPool>'} \\
  "openAdvanceOf(bytes32)(uint256,uint256,bool)" <job-id> --rpc-url ${rpc}

cast call ${pool ?? '<floatPool>'} \\
  "advanceRate(address)(uint16)" <operator-address> --rpc-url ${rpc}`}</code></pre>
            <p className="guide-note">
              Job status reads 0 Created, 1 Funded, 2 In progress, 3 Delivered, 4 Accepted, 5 Refunded, 6 Cancelled. The
              rate is in basis points: 5000 is 50%.
            </p>
          </section>

          <section id="safety" className="guide-sec">
            <h2>Safety controls</h2>
            <div className="table-wrap guide-table">
              <table className="table">
                <tbody>
                  <tr><th scope="row">Emergency pause</th><td>The admin can pause the escrow and the pool, one call each. A paused escrow takes no new jobs or payments; a paused pool takes no deposits and lends nothing.</td></tr>
                  <tr><th scope="row">Caps</th><td>A ceiling on any one job, any one advance and the pool’s total exposure. Lowering the pool’s caps is immediate and raising them waits 2 days. The job cap changes at once.</td></tr>
                  <tr><th scope="row">Operator allowlist</th><td>Only allowlisted operators can draw advances. A new grant waits 2 days; a revocation is immediate.</td></tr>
                  <tr><th scope="row">Depositor allowlist</th><td>Only allowlisted addresses can deposit into the pool.</td></tr>
                  <tr><th scope="row">Admin handover</th><td>Two steps: the current admin proposes, then the new admin accepts from its own wallet after 2 days.</td></tr>
                  <tr><th scope="row">Customer first on refunds</th><td>A refund returns the customer’s full payment before the pool counts any loss.</td></tr>
                </tbody>
              </table>
            </div>
            <p>The current values are read live on <Link className="link" href="/audit">Audit</Link>.</p>
          </section>

          <section id="status" className="guide-sec">
            <h2>Status on this network</h2>
            <p>What works on {label} as of {STATUS_DATE}.</p>
            <div className="table-wrap guide-table is-stack">
              <table className="table">
                <thead>
                  <tr>
                    <th>Capability</th>
                    <th>State</th>
                    <th>Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {STATUS[net].map((r) => (
                    <tr key={r.what}>
                      <td data-label="Capability"><span>{r.what}</span></td>
                      <td data-label="State"><span><span className={`guide-state is-${r.state}`}>{STATE_WORD[r.state]}</span></span></td>
                      <td data-label="Detail"><span>{r.note}</span></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          <section id="glossary" className="guide-sec">
            <h2>Glossary</h2>
            <dl className="guide-gloss">
              <div><dt>Escrow</dt><dd>The customer’s payment, held by JobVault until the job is accepted or refunded.</dd></div>
              <div><dt>Advance</dt><dd>Working capital the pool lends the operator against a funded job.</dd></div>
              <div><dt>Advance rate</dt><dd>The share of a job’s price an operator can borrow. It starts at 50% and moves with delivery history.</dd></div>
              <div><dt>Waterfall</dt><dd>The order a settlement pays in: the pool’s principal and fee first, the operator last.</dd></div>
              <div><dt>First-loss reserve</dt><dd>20% of every fee, kept by the pool to absorb a failed job before depositors do.</dd></div>
              <div><dt>Owner daemon</dt><dd>The program that runs the agents and holds the owner’s decisions. The dashboard reads it for approvals and the workforce.</dd></div>
              <div><dt>x402</dt><dd>Payment over HTTP: a seller answers 402 with a price, and the agent pays in USDC to get the resource.</dd></div>
              <div><dt>Micro-USDC</dt><dd>USDC in its smallest unit. 1.00 USDC is 1,000,000. The contracts count only in these.</dd></div>
            </dl>
            <p className="guide-note">
              Operators setting up their own deployment: the technical walkthrough is{' '}
              <a className="link" href="https://github.com/gnanam1990/snapfall/blob/main/docs/END-TO-END.md" target="_blank" rel="noreferrer">docs/END-TO-END.md</a>.
            </p>
          </section>
        </article>
      </div>
    </>
  );
}
