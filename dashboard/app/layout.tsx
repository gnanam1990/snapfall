import './globals.css';
import type { Metadata } from 'next';
import Sidebar from '@/components/Sidebar';

export const metadata: Metadata = {
  title: 'Snapfall',
  description: 'The self-financing AI workforce, built on Arc.',
};

/**
 * Applies a stored light preference before the first paint.
 *
 * Without this the page renders in the default theme and then swaps once React hydrates, which
 * is a full-screen white flash for anyone who chose light. It has to be inline and synchronous
 * in <head> for that reason: any deferred or bundled script runs too late to prevent the flash.
 *
 * Dark is the default, so only 'light' writes an attribute. An unset preference deliberately
 * does NOT follow prefers-color-scheme yet: the theme is opt-in until the default flips, so
 * nobody's dashboard changes appearance underneath them mid-hackathon.
 */
const THEME_BOOT = `try{if(localStorage.getItem('snapfall-theme')==='light'){document.documentElement.dataset.theme='light'}}catch(e){}`;

/**
 * The direction contract, emitted into the DOM rather than written as a JSX comment.
 *
 * A `{/* … *\/}` comment is stripped by the compiler and never reaches the markup, so a contract
 * written that way is one nobody can audit in a built artifact. Verified by grepping the
 * production output for the seed key and finding nothing. This ships as a real HTML comment.
 */
const DIRECTION_CONTRACT = `<!--
  THESIS: Money in this product is evidence, not atmosphere, so the interface is a ledger. Every
  figure names its source, every state its reason, and nothing is asserted that cannot be read
  back from the chain. It refuses dashboard theatre — glow, gradient, drawn machinery — and lets
  type, hairlines and tabular digits do the work.

  OWN-WORLD: A ledger kept by a careful operator. Flat panels, one hairline system, figures in
  tabular digits with their provenance beside them. Colour is semantic only: settled green,
  refused red, waiting amber, and an iris accent for the live, verifiable element — a link out
  to evidence, a focus ring, money in motion. Recognisable with all content removed by its ruled
  structure and its source lines.

  STORY: The visitor sees the pool's capital and its caps, what is lent out, what is waiting on
  them, and money moving between customer, escrow, pool and operator. They believe it because
  every number says where it came from. They act by approving or refusing a purchase while it is
  still pending.

  FIRST VIEWPORT: The capital pool as a measured panel — TVL as the primary figure, utilisation
  as a meter with the 80% and 10% caps marked as ticks where they sit, and four facts each naming
  their source. Pending approvals are the one coloured region and carry the primary action. The
  live money graph sits below, flat, its droplets the only motion on the page.

  FORM: Ledger / refined fintech. Supersedes the instrument-diagram contract (seed c0015262);
  seed key a91f44c0.

  FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the
  verdict, and DESIGN.md
-->`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The boot script mutates data-theme before hydration, so the server's <html> attributes
    // and the client's legitimately differ.
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT }} />
      </head>
      <body>
        <div hidden dangerouslySetInnerHTML={{ __html: DIRECTION_CONTRACT }} />
        <div className="app">
          <Sidebar />
          <main className="main">{children}</main>
        </div>
      </body>
    </html>
  );
}
