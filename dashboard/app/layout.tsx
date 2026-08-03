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
  THESIS: Money here is plumbing with capacity limits, so the interface is the drawing an engineer
  reads to understand a system under load. It refuses the stat-tile grid and the hero-metric
  template: a vessel with its caps drawn as real thresholds says more than a big number with a
  small label ever does.

  OWN-WORLD: A piping and instrumentation diagram. Ink on vellum, hairline rules, vessels and
  valves as drawn glyphs, callout leaders tying every figure to the thing it measures, annotation
  lettering in tracked small caps, tabular figures throughout. Colour is semantic only: flowing,
  alarm, settled. No gradient, no glow, no shadow as decoration. Recognisable with all content
  removed, by its rules, leaders and registration marks.

  STORY: The visitor sees money enter escrow, an advance drawn against it under a cap they can
  see, spending reduce it, and a waterfall repay the pool before the operator. They believe it
  because every figure carries its source. They act by approving or refusing a purchase while it
  is still pending.

  FIRST VIEWPORT: The pool drawn as a vessel, left of centre, its 80% utilisation and 10% per-org
  caps rendered as real threshold lines rather than labels. Escrow feeds in from above; the
  waterfall leaves in stages, pool first, operator second. Figures sit on callout leaders, each
  naming its source. Pending approvals are the one place colour appears, and the primary action
  sits with them.

  FORM: Instrument diagram (P&ID). Candidate 4 of 7 on the grounded list, ordered by resonance.
  Seed key c0015262.

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
