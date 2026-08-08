'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';
import ThemeToggle from '@/components/ThemeToggle';
import Logo from '@/components/Logo';

const NAV: { href: string; label: string }[] = [
  { href: '/overview', label: 'Overview' },
  { href: '/jobs', label: 'Jobs' },
  { href: '/workforce', label: 'Workforce' },
  { href: '/approvals', label: 'Approvals' },
  { href: '/float', label: 'Float' },
  { href: '/audit', label: 'Audit' },
  { href: '/settings', label: 'Settings' },
];

export default function Sidebar() {
  const pathname = usePathname();
  // Two surfaces are not the owner's dashboard and must not carry its navigation.
  // The customer portal (V9) is a magic-link surface for a different principal. The landing
  // page at "/" is for someone who does not yet know what this is; showing them an operator
  // nav before the product has been explained is the wrong first move. Both render full-bleed.
  if (pathname === '/' || pathname?.startsWith('/portal')) return null;
  return (
    <aside className="sidebar">
      <div className="brand">
        <Logo size={22} />
        <div className="brand-name">Snapfall</div>
      </div>
      <nav>
        {NAV.map((n) => {
          const active = pathname.startsWith(n.href);
          return (
            <Link
              key={n.href}
              href={n.href}
              className={`nav-link${active ? ' active' : ''}`}
              aria-current={active ? 'page' : undefined}
            >
              {n.label}
            </Link>
          );
        })}
      </nav>
      <div className="sidebar-foot">
        <div className="sidebar-foot-row">
          <div className="sidebar-net">
            <span className="sidebar-net-dot" aria-hidden="true" /> Arc testnet
          </div>
          <ThemeToggle />
        </div>
        <div style={{ marginTop: 4 }}>capital in a snap,<br />settlement in a waterfall</div>
      </div>
    </aside>
  );
}
