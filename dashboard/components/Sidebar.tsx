'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';

const NAV: { href: string; label: string; pill?: string }[] = [
  { href: '/', label: 'Overview' },
  { href: '/jobs', label: 'Jobs' },
  { href: '/workforce', label: 'Workforce' },
  { href: '/approvals', label: 'Approvals' },
  { href: '/float', label: 'Float' },
  { href: '/audit', label: 'Audit' },
  { href: '/settings', label: 'Settings' },
];

export default function Sidebar() {
  const pathname = usePathname();
  // The customer portal (V9) is a magic-link surface for a different principal — it must
  // never show the owner's navigation. The portal page renders full-bleed over the grid.
  if (pathname?.startsWith('/portal')) return null;
  return (
    <aside className="sidebar">
      <div className="brand">
        <div className="brand-mark" />
        <div className="brand-name">Snapfall</div>
      </div>
      <nav>
        {NAV.map((n) => {
          const active = n.href === '/' ? pathname === '/' : pathname.startsWith(n.href);
          return (
            <Link
              key={n.href}
              href={n.href}
              className={`nav-link${active ? ' active' : ''}`}
              aria-current={active ? 'page' : undefined}
            >
              <span>{n.label}</span>
              {n.pill ? <span className="pill">{n.pill}</span> : null}
            </Link>
          );
        })}
      </nav>
      <div className="sidebar-foot">
        <div className="dot-live">● demo replay · Arc testnet next</div>
        <div style={{ marginTop: 4 }}>capital in a snap,<br />settlement in a waterfall</div>
      </div>
    </aside>
  );
}
