'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { Fragment, type ReactNode } from 'react';
import ThemeToggle from '@/components/ThemeToggle';
import Logo from '@/components/Logo';
import { networkLabel } from '@/lib/network';

/** The business, the agents, and the proof — separated by a rule rather than by labels. */
const GROUPS: { href: string; label: string }[][] = [
  [
    { href: '/overview', label: 'Overview' },
    { href: '/jobs', label: 'Jobs' },
    { href: '/activity', label: 'Activity' },
    { href: '/float', label: 'Capital pool' },
  ],
  [
    { href: '/approvals', label: 'Approvals' },
    { href: '/workforce', label: 'Workforce' },
  ],
  [
    { href: '/audit', label: 'Audit' },
    { href: '/settings', label: 'Settings' },
    { href: '/guide', label: 'Guide' },
  ],
];

/** Customer-facing pages (the acceptance portal and the pay page) are for a different person and
 *  render without the owner's navigation. */
function isBare(pathname: string | null): boolean {
  return !!pathname && (pathname.startsWith('/portal') || pathname.startsWith('/pay/'));
}

/** Owner pages whose markup predates this template: framed in the content column, with their
 *  page header drawn as the same navy band (globals.css, `.legacy`). */
function isLegacy(pathname: string | null): boolean {
  if (!pathname) return false;
  return ['/approvals', '/workforce', '/settings', '/audit'].some((p) => pathname.startsWith(p)) || /^\/jobs\/.+/.test(pathname);
}

export default function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  if (isBare(pathname)) {
    return (
      <div className="shell-bare">
        <main className="main">{children}</main>
      </div>
    );
  }
  return (
    <>
      <header className="top">
        <div className="wrap top-inner">
          <Link href="/overview" className="top-brand">
            <Logo size={22} />
            <span className="top-name">Snapfall</span>
          </Link>
          <nav className="top-nav" aria-label="Dashboard">
            {GROUPS.map((group, gi) => (
              <Fragment key={gi}>
                {gi > 0 ? <span className="top-sep" aria-hidden="true" /> : null}
                {group.map((n) => {
                  const active = pathname?.startsWith(n.href) ?? false;
                  return (
                    <Link key={n.href} href={n.href} className={`top-link${active ? ' is-active' : ''}`} aria-current={active ? 'page' : undefined}>
                      {n.label}
                    </Link>
                  );
                })}
              </Fragment>
            ))}
          </nav>
          <div className="top-side">
            <span className="net">{networkLabel()}</span>
            <ThemeToggle />
          </div>
        </div>
      </header>
      <main className="main" style={{ overflowX: 'clip' }}>
        {isLegacy(pathname) ? <div className="wrap legacy">{children}</div> : children}
      </main>
    </>
  );
}
