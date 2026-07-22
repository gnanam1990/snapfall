import './globals.css';
import type { Metadata } from 'next';
import { Inter, Inter_Tight } from 'next/font/google';
import Navbar from '@/components/Navbar';

/* Self-hosted at build time via next/font: zero runtime font fetches (demo-safe).
   Inter Tight ExtraBold stands in for the spec's "Helvetica Now Display Bold". */
const heading = Inter_Tight({
  subsets: ['latin'],
  weight: ['700', '800'],
  variable: '--font-heading',
});
const body = Inter({
  subsets: ['latin'],
  weight: ['300', '400', '500', '600', '700', '800', '900'],
  variable: '--font-body',
});

export const metadata: Metadata = {
  title: 'Snapfall',
  description: 'The self-financing AI workforce, built on Arc.',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${heading.variable} ${body.variable}`} suppressHydrationWarning>
      <body>
        {/* Theme boot: runs before paint so a stored/system dark preference never flashes light. */}
        <script
          dangerouslySetInnerHTML={{
            __html:
              "(function(){try{var t=localStorage.getItem('snapfall-theme');if(t!=='light'&&t!=='dark'){t=window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';}document.documentElement.dataset.theme=t;}catch(e){}})();",
          }}
        />
        <Navbar />
        <main className="relative z-10 mx-auto w-full max-w-[1280px] px-5 pb-16 sm:px-8">
          {children}
        </main>
        <footer className="mx-auto max-w-[1280px] px-5 pb-8 sm:px-8">
          <p className="text-xs" style={{ color: 'var(--color-faint)' }}>
            Snapfall, built on Arc. Capital in a snap, settlement in a waterfall. Testnet assets only.
          </p>
        </footer>
      </body>
    </html>
  );
}
