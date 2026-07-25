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

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The boot script mutates data-theme before hydration, so the server's <html> attributes
    // and the client's legitimately differ.
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT }} />
      </head>
      <body>
        <div className="app">
          <Sidebar />
          <main className="main">{children}</main>
        </div>
      </body>
    </html>
  );
}
