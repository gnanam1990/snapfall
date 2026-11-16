import Link from 'next/link';
import GuideToc, { type TocItem } from '@/components/GuideToc';
import { Band } from '@/components/ui';
import { SUPPORT_EMAIL, LEGAL_UPDATED } from '@/lib/legal';

export const metadata = { title: 'Privacy · Snapfall' };

/**
 * The privacy policy. Every statement here is checked against the code it describes: there is no
 * analytics or tracking script in the dashboard, browser storage holds only the three keys named
 * below, the owner cookie is HttpOnly and host-only (lib/ownerSession.ts), and the scoper only
 * sends request text when a key is configured (README, "Honest limits"). Change the code and this
 * page together.
 */

const TOC: TocItem[] = [
  { id: 'scope', label: 'What this covers' },
  { id: 'not', label: 'What we do not collect' },
  { id: 'chain', label: 'Public blockchain data' },
  { id: 'browser', label: 'Your browser' },
  { id: 'jobs', label: 'Job records' },
  { id: 'providers', label: 'Services we rely on' },
  { id: 'retention', label: 'How long it is kept' },
  { id: 'choices', label: 'Your choices' },
  { id: 'changes', label: 'Changes and contact' },
];

export default function PrivacyPage() {
  return (
    <>
      <Band title="Privacy" lede="What Snapfall collects, and what it does not." meta={<span>Last updated {LEGAL_UPDATED}</span>} />

      <div className="wrap guide">
        <GuideToc items={TOC} />

        <article className="guide-body">
          <section id="scope" className="guide-sec">
            <h2>What this covers</h2>
            <p>
              This policy covers the Snapfall websites (snapfall.lol, www.snapfall.lol and app.snapfall.lol), the owner
              dashboards, and the customer links an operator sends for a job. It does not cover the Arc blockchain, Circle’s
              wallet services, or a wallet app you bring yourself. Those are run by others under their own policies.
            </p>
          </section>

          <section id="not" className="guide-sec">
            <h2>What we do not collect</h2>
            <p>
              Visitors do not need an account. The sites run no analytics, advertising or tracking scripts and set no
              tracking cookies. We do not sell or rent personal information, and we do not build profiles of visitors.
            </p>
          </section>

          <section id="chain" className="guide-sec">
            <h2>Public blockchain data</h2>
            <p>
              Most of what the dashboard shows is read from the Snapfall contracts on Arc: wallet addresses, amounts, job
              ids, content hashes and transaction hashes. That data is public, anyone can read it, and it cannot be edited or
              deleted, by us or anyone else. If you fund, deliver or accept a job, your wallet address and the amounts become
              part of that public record. Work and job details stay off chain. Only their hashes are recorded.
            </p>
          </section>

          <section id="browser" className="guide-sec">
            <h2>Your browser</h2>
            <p>The sites keep a few small values in your own browser. They are not sent to us.</p>
            <div className="guide-table">
              <table className="table is-stack">
                <tbody>
                  <tr>
                    <th scope="row"><span>Theme</span></th>
                    <td data-label="What it holds"><span><code>snapfall-theme</code>: whether you chose light or dark.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Passkey wallet</span></th>
                    <td data-label="What it holds"><span>The id of a passkey you created to pay for a job, so the page can find it again. The passkey itself stays on your device.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Bridge progress</span></th>
                    <td data-label="What it holds"><span>If you bring USDC from another chain, the step a transfer has reached, so a reload can resume it.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Owner session</span></th>
                    <td data-label="What it holds"><span>Only for the operator: an HttpOnly, same-site cookie set when they sign in to the owner dashboard, which expires on its own.</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p>You can clear all of these at any time from your browser’s settings.</p>
          </section>

          <section id="jobs" className="guide-sec">
            <h2>Job records</h2>
            <p>
              When an operator takes a job, their own Snapfall daemon keeps the working record: the job’s description and
              terms, the customer’s wallet address, approvals, payment intents, delivery hashes and an event log. The daemon
              runs on the operator’s machine or on a private server the operator controls. A customer link opens one job and
              nothing else.
            </p>
            <p>
              If the operator turns on the optional AI scoper, the text of a job request is sent to Anthropic to draft the
              scope. Keys, wallet addresses and job state are not sent. It is off unless the operator configures it.
            </p>
          </section>

          <section id="providers" className="guide-sec">
            <h2>Services we rely on</h2>
            <div className="guide-table">
              <table className="table is-stack">
                <tbody>
                  <tr>
                    <th scope="row"><span>Vercel and Render</span></th>
                    <td data-label="Why"><span>Host the sites and the owner daemons. Like any web host, they log requests (IP address, browser, page) to run and secure the service.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Arc RPC providers</span></th>
                    <td data-label="Why"><span>Answer the chain reads the dashboard makes, such as Alchemy or Arc’s public endpoint.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Circle</span></th>
                    <td data-label="Why"><span>Provides the passkey wallet you can pay with, and the operator’s developer-controlled wallet. <a href="https://www.circle.com/legal/privacy-policy" target="_blank" rel="noreferrer">Circle’s privacy policy</a> applies to those services.</span></td>
                  </tr>
                  <tr>
                    <th scope="row"><span>Anthropic</span></th>
                    <td data-label="Why"><span>Only if the operator turns on the AI scoper, as above.</span></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <section id="retention" className="guide-sec">
            <h2>How long it is kept</h2>
            <p>
              Chain data is permanent. Hosting logs are kept for as long as each host’s own retention allows. A job record is
              kept by the operator’s daemon for as long as the operator needs it to account for the job, and can be deleted by
              the operator at any time.
            </p>
          </section>

          <section id="choices" className="guide-sec">
            <h2>Your choices</h2>
            <p>
              To ask what we hold about you, or to have a job record corrected or deleted, write to{' '}
              <a href={`mailto:${SUPPORT_EMAIL}`}>{SUPPORT_EMAIL}</a>. We will tell you what we can and cannot do. Chain data
              is outside anyone’s control. The service is not meant for anyone under 18.
            </p>
          </section>

          <section id="changes" className="guide-sec">
            <h2>Changes and contact</h2>
            <p>
              If this policy changes, the date at the top changes with it, and the history is public in the{' '}
              <a href="https://github.com/gnanam1990/snapfall" target="_blank" rel="noreferrer">source repository</a>. Questions
              go to <a href={`mailto:${SUPPORT_EMAIL}`}>{SUPPORT_EMAIL}</a>. See also the <Link href="/terms">terms</Link>.
            </p>
          </section>
        </article>
      </div>
    </>
  );
}
