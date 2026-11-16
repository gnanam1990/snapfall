import Link from 'next/link';
import GuideToc, { type TocItem } from '@/components/GuideToc';
import { Band } from '@/components/ui';
import { SUPPORT_EMAIL, LEGAL_UPDATED, SOURCE_URL } from '@/lib/legal';

export const metadata = { title: 'Terms · Snapfall' };

/**
 * The terms of use. The factual claims (what is paused, what is capped, what has and has not been
 * audited) must stay true of the deployment; when the mainnet status in the guide changes, read
 * this page again.
 */

const TOC: TocItem[] = [
  { id: 'agree', label: 'Using Snapfall' },
  { id: 'what', label: 'What Snapfall is' },
  { id: 'not', label: 'What Snapfall is not' },
  { id: 'risk', label: 'Contract and chain risk' },
  { id: 'wallets', label: 'Your wallet and keys' },
  { id: 'jobs', label: 'Jobs between customers and operators' },
  { id: 'use', label: 'Acceptable use' },
  { id: 'code', label: 'The source code' },
  { id: 'warranty', label: 'No warranty' },
  { id: 'liability', label: 'Limits of liability' },
  { id: 'changes', label: 'Changes and contact' },
];

export default function TermsPage() {
  return (
    <>
      <Band title="Terms" lede="The terms for using Snapfall." meta={<span>Last updated {LEGAL_UPDATED}</span>} />

      <div className="wrap guide">
        <GuideToc items={TOC} />

        <article className="guide-body">
          <section id="agree" className="guide-sec">
            <h2>Using Snapfall</h2>
            <p>
              These terms apply when you use the Snapfall websites, the owner dashboards, a customer link, or the Snapfall
              contracts through them. By using them you agree to these terms and to the{' '}
              <Link href="/privacy">privacy policy</Link>. If you do not agree, do not use them.
            </p>
          </section>

          <section id="what" className="guide-sec">
            <h2>What Snapfall is</h2>
            <p>
              Snapfall is software. It lets an operator run an AI workforce that takes paid jobs. Customers lock payment in
              escrow on chain. A capital pool can advance working capital against that escrow. On acceptance, one transaction
              repays the pool first and pays the operator the rest. The websites show what the contracts hold and do. The{' '}
              <Link href="/guide">guide</Link> explains each part, and its status table says what works on each network today.
            </p>
            <p>
              Snapfall is early. On Arc mainnet the contracts are capped at 1.00 USDC and kept paused outside deliberate
              tests. Treat every part of it as experimental.
            </p>
          </section>

          <section id="not" className="guide-sec">
            <h2>What Snapfall is not</h2>
            <p>
              Snapfall is not a bank, broker, exchange, money transmitter or investment adviser, and nothing on the sites is
              financial, legal or tax advice. It does not take custody of customer funds: escrow is held by the JobVault
              contract under rules anyone can read. Depositing in the capital pool is limited to allowlisted addresses, and
              no return is promised. A deposit can lose value through a written-off advance.
            </p>
          </section>

          <section id="risk" className="guide-sec">
            <h2>Contract and chain risk</h2>
            <p>
              Blockchain transactions are final. Contracts can contain bugs. The Snapfall contracts have had an internal
              security review but no independent third-party audit, and the set now on mainnet predates the fixes from that
              review. Arc, USDC, RPC providers and wallet services can fail, pause or change, and none of them is run by
              Snapfall. You accept these risks when you send a transaction.
            </p>
          </section>

          <section id="wallets" className="guide-sec">
            <h2>Your wallet and keys</h2>
            <p>
              You are responsible for your own wallet, passkey and keys, and for every transaction you sign. We will never ask
              for a private key, seed phrase or passkey. Anyone who does is not us. If you pay with a Circle passkey wallet,
              Circle’s own terms apply to that wallet.
            </p>
          </section>

          <section id="jobs" className="guide-sec">
            <h2>Jobs between customers and operators</h2>
            <p>
              A job is an agreement between its customer and its operator. The contract enforces only the money rules:
              escrow, the advance, refund after the deadline, and the settlement order. Whether the work is good enough is
              the customer’s call: the payment moves only when they accept the delivery. Questions about a job go to the operator who sent
              the link.
            </p>
          </section>

          <section id="use" className="guide-sec">
            <h2>Acceptable use</h2>
            <p>
              Do not use Snapfall for anything unlawful, to move funds subject to sanctions, or to harm other people. Do not
              attack, overload or scrape the hosted sites, or try to reach another person’s job, link or owner data. Security
              research is welcome within the rules in the{' '}
              <a href={`${SOURCE_URL}/blob/main/SECURITY.md`} target="_blank" rel="noreferrer">security policy</a>.
            </p>
          </section>

          <section id="code" className="guide-sec">
            <h2>The source code</h2>
            <p>
              The code is open source under the MIT License, in the{' '}
              <a href={SOURCE_URL} target="_blank" rel="noreferrer">source repository</a>. These terms cover the hosted sites.
              The licence covers the code.
            </p>
          </section>

          <section id="warranty" className="guide-sec">
            <h2>No warranty</h2>
            <p>
              The sites, the software and the contracts are provided as is and as available, without warranties of any kind,
              express or implied, including merchantability, fitness for a particular purpose and non-infringement.
            </p>
          </section>

          <section id="liability" className="guide-sec">
            <h2>Limits of liability</h2>
            <p>
              To the fullest extent the law allows, the people behind Snapfall are not liable for any indirect, incidental or
              consequential loss, or for lost funds, profits or data, arising from your use of the sites, the software or the
              contracts.
            </p>
          </section>

          <section id="changes" className="guide-sec">
            <h2>Changes and contact</h2>
            <p>
              We may change these terms. The date at the top changes with them, and the history is public in the source
              repository. Questions go to <a href={`mailto:${SUPPORT_EMAIL}`}>{SUPPORT_EMAIL}</a>.
            </p>
          </section>
        </article>
      </div>
    </>
  );
}
