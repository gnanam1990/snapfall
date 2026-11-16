import Link from 'next/link';
import { SECURITY_URL, SOURCE_URL, SUPPORT_EMAIL } from '@/lib/legal';

/** The site footer: where to get help and the policies, on every page including customer links. */
export default function SiteFooter() {
  return (
    <footer className="site-foot">
      <div className="wrap site-foot-inner">
        <nav className="site-foot-links" aria-label="Site">
          <Link href="/guide">Guide</Link>
          <Link href="/support">Support</Link>
          <Link href="/privacy">Privacy</Link>
          <Link href="/terms">Terms</Link>
          <a href={SECURITY_URL} target="_blank" rel="noreferrer">Report a vulnerability</a>
          <a href={SOURCE_URL} target="_blank" rel="noreferrer">Source</a>
        </nav>
        <p className="site-foot-note">
          <a href={`mailto:${SUPPORT_EMAIL}`}>{SUPPORT_EMAIL}</a>
          <span>Open source under the MIT License.</span>
        </p>
      </div>
    </footer>
  );
}
