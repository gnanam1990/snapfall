import './legacy.css';
import './globals.css';
import type { Metadata } from 'next';
import { JetBrains_Mono, Schibsted_Grotesk } from 'next/font/google';
import AppShell from '@/components/Sidebar';

// One family does the interface, from the 13px label to the 52px sentence; the mono is only for
// what is genuinely machine text: hashes, addresses, ids.
const sans = Schibsted_Grotesk({ subsets: ['latin'], weight: ['400', '500', '600', '700', '800'], variable: '--font-sans', display: 'swap' });
const mono = JetBrains_Mono({ subsets: ['latin'], weight: ['400', '500'], variable: '--font-mono', display: 'swap' });

export const metadata: Metadata = {
  title: 'Snapfall',
  description: 'The self-financing AI workforce, built on Arc. Every figure is read from the chain.',
};

/**
 * Applies a stored dark preference before the first paint, so choosing dark never flashes the
 * light default first. It has to be inline and synchronous in <head> for that reason.
 */
const THEME_BOOT = `try{if(localStorage.getItem('snapfall-theme')==='dark'){document.documentElement.dataset.theme='dark'}}catch(e){}`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The boot script mutates data-theme before hydration, so the server's <html> attributes
    // and the client's legitimately differ.
    <html lang="en" className={`${sans.variable} ${mono.variable}`} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT }} />
      </head>
      <body>
        <AppShell>{children}</AppShell>
      </body>
    </html>
  );
}
