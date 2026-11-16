import Link from 'next/link';
import GuideToc, { type TocItem } from '@/components/GuideToc';
import { Band } from '@/components/ui';
import { SUPPORT_EMAIL, SECURITY_URL, SOURCE_URL } from '@/lib/legal';

export const metadata = { title: 'Support · Snapfall' };

const TOC: TocItem[] = [
  { id: 'contact', label: 'Contact' },
  { id: 'job', label: 'A question about a job' },
  { id: 'security', label: 'A security problem' },
  { id: 'scams', label: 'We never ask for keys' },
];

export default function SupportPage() {
  return (
    <>
      <Band title="Support" lede="How to reach us, and what to send." />

      <div className="wrap guide">
        <GuideToc items={TOC} />

        <article className="guide-body">
          <section id="contact" className="guide-sec">
            <h2>Contact</h2>
            <p>
              Write to <a href={`mailto:${SUPPORT_EMAIL}`}>{SUPPORT_EMAIL}</a>. Say which site you were on (snapfall.lol is Arc
              mainnet, app.snapfall.lol is Arc testnet), what you did, what you expected, and any job id or transaction hash
              involved. A transaction hash is safe to share. It is public anyway.
            </p>
            <p>
              Before writing, the <Link href="/guide">guide</Link> may already answer it. Its status table says what works on
              each network today.
            </p>
          </section>

          <section id="job" className="guide-sec">
            <h2>A question about a job</h2>
            <p>
              A job is between you and the operator who sent your link, so the operator is the first person to ask about the
              work. The money rules are in the contract and the same for everyone: your payment stays in escrow until you
              accept the delivery, and you can take it back with a refund once the job’s deadline has passed.
            </p>
          </section>

          <section id="security" className="guide-sec">
            <h2>A security problem</h2>
            <p>
              Do not email it or post it in public. Report it privately on GitHub through{' '}
              <a href={SECURITY_URL} target="_blank" rel="noreferrer">Report a vulnerability</a>. The{' '}
              <a href={`${SOURCE_URL}/blob/main/SECURITY.md`} target="_blank" rel="noreferrer">security policy</a> lists what
              is in scope and how to test safely.
            </p>
          </section>

          <section id="scams" className="guide-sec">
            <h2>We never ask for keys</h2>
            <p>
              Snapfall will never ask for a private key, seed phrase, passkey or one-time code, or ask you to send funds to
              “verify” a wallet. Anyone who does is not us. Escrow is only ever paid into the JobVault contract, from the
              page your own link opens.
            </p>
          </section>
        </article>
      </div>
    </>
  );
}
