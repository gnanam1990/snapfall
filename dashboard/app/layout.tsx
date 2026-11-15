import './legacy.css';
import './globals.css';
import type { Metadata } from 'next';
import { Fira_Code, Fira_Sans } from 'next/font/google';
import AppShell from '@/components/Sidebar';

const sans = Fira_Sans({ subsets: ['latin'], weight: ['300', '400', '500', '600', '700'], variable: '--font-sans', display: 'swap' });
const mono = Fira_Code({ subsets: ['latin'], weight: ['400', '500', '600', '700'], variable: '--font-mono', display: 'swap' });

export const metadata: Metadata = {
  title: 'Snapfall',
  description: 'The self-financing AI workforce, built on Arc. Every figure is read from the chain.',
};

/**
 * Applies a stored light preference before the first paint, so choosing light never flashes the
 * dark default first. It has to be inline and synchronous in <head> for that reason.
 */
const THEME_BOOT = `try{if(localStorage.getItem('snapfall-theme')==='light'){document.documentElement.dataset.theme='light'}}catch(e){}`;

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
