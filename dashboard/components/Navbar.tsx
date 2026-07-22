'use client';

import { useState } from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { AnimatePresence, motion } from 'framer-motion';
import { List, X, ArrowSquareOut } from '@phosphor-icons/react';
import Logo from './Logo';
import ThemeToggle from './ThemeToggle';
import { EASE_IN, EASE_OUT } from '@/lib/motion';

const NAV = [
  { href: '/', label: 'Overview' },
  { href: '/jobs', label: 'Jobs' },
  { href: '/workforce', label: 'Workforce' },
  { href: '/approvals', label: 'Approvals' },
  { href: '/float', label: 'Float' },
  { href: '/audit', label: 'Audit' },
  { href: '/settings', label: 'Settings' },
];

const EXPLORER = 'https://testnet.arcscan.app';

export default function Navbar() {
  const pathname = usePathname();
  const [open, setOpen] = useState(false);

  const isActive = (href: string) => (href === '/' ? pathname === '/' : pathname.startsWith(href));

  return (
    <header className="relative z-20">
      <nav className="mx-auto flex max-w-[1280px] items-center justify-between px-5 py-4 sm:px-8 sm:py-5">
        {/* left: brand */}
        <Link href="/" className="flex items-center gap-2.5" style={{ color: 'var(--color-text)' }}>
          <Logo size={26} />
          <span
            className="text-[17px] tracking-tight"
            style={{ fontFamily: 'var(--font-heading), sans-serif' }}
          >
            Snapfall
          </span>
        </Link>

        {/* center: desktop links */}
        <div className="hidden items-center gap-5 md:flex lg:gap-8">
          {NAV.map((n) => (
            <Link
              key={n.href}
              href={n.href}
              className="text-sm font-medium transition-opacity hover:opacity-70"
              style={{
                color: isActive(n.href) ? 'var(--color-accent)' : 'var(--color-text)',
                fontWeight: isActive(n.href) ? 600 : 500,
              }}
            >
              {n.label}
            </Link>
          ))}
        </div>

        {/* right: pills (desktop) */}
        <div className="hidden items-center gap-3 md:flex">
          <span
            className="hidden items-center gap-2 whitespace-nowrap rounded-full px-4 py-2.5 text-sm font-medium lg:flex"
            style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)', color: 'var(--color-muted)' }}
          >
            <span className="dot-live" /> live on Arc testnet
          </span>
          <ThemeToggle />
          <motion.a
            href={EXPLORER}
            target="_blank"
            rel="noreferrer"
            whileHover={{ scale: 1.02 }}
            whileTap={{ scale: 0.95 }}
            className="flex items-center gap-1.5 whitespace-nowrap rounded-full px-5 py-2.5 text-sm font-semibold text-white transition-shadow hover:shadow-[var(--shadow-accent)]"
            style={{ background: 'var(--color-accent)' }}
          >
            Explorer <ArrowSquareOut size={14} weight="bold" />
          </motion.a>
        </div>

        {/* mobile: theme toggle + hamburger */}
        <div className="flex items-center gap-2 md:hidden">
          <ThemeToggle />
          <button
            aria-label={open ? 'Close menu' : 'Open menu'}
            onClick={() => setOpen((o) => !o)}
            style={{ color: 'var(--color-text)', background: 'none', border: 'none', padding: 4 }}
          >
            {open ? <X size={24} weight="bold" /> : <List size={24} weight="bold" />}
          </button>
        </div>
      </nav>

      {/* mobile slide-in sheet */}
      <AnimatePresence>
        {open ? (
          <>
            <motion.div
              key="backdrop"
              className="fixed inset-0 z-30"
              style={{ background: 'rgba(25,40,55,0.35)', backdropFilter: 'blur(4px)' }}
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.3 }}
              onClick={() => setOpen(false)}
            />
            <motion.aside
              key="sheet"
              className="fixed right-0 top-0 z-40 flex flex-col"
              style={{
                width: 'min(88vw, 360px)',
                height: '100dvh',
                background: 'var(--sheet-bg)',
                boxShadow: '-12px 0 48px rgba(25,40,55,0.18)',
              }}
              initial={{ x: '100%' }}
              animate={{ x: 0, transition: { duration: 0.45, ease: EASE_OUT } }}
              exit={{ x: '100%', transition: { duration: 0.35, ease: EASE_IN } }}
            >
              <div className="flex items-center justify-between px-6 py-5" style={{ color: 'var(--color-text)' }}>
                <span className="flex items-center gap-2.5">
                  <Logo size={26} />
                  <span className="text-[17px] tracking-tight" style={{ fontFamily: 'var(--font-heading), sans-serif' }}>
                    Snapfall
                  </span>
                </span>
                <motion.button
                  aria-label="Close menu"
                  whileTap={{ scale: 0.9 }}
                  onClick={() => setOpen(false)}
                  className="flex h-10 w-10 items-center justify-center rounded-full"
                  style={{ background: 'color-mix(in srgb, var(--color-text) 10%, transparent)', border: 'none', color: 'var(--color-text)' }}
                >
                  <X size={20} weight="bold" />
                </motion.button>
              </div>
              <div className="mx-6 h-px" style={{ background: 'color-mix(in srgb, var(--color-text) 12%, transparent)' }} />
              <div className="flex flex-1 flex-col gap-1 px-4 py-4">
                {NAV.map((n, i) => (
                  <motion.div
                    key={n.href}
                    initial={{ opacity: 0, x: 24 }}
                    animate={{ opacity: 1, x: 0, transition: { delay: 0.18 + i * 0.07, duration: 0.4, ease: EASE_OUT } }}
                  >
                    <Link
                      href={n.href}
                      onClick={() => setOpen(false)}
                      className="block rounded-xl px-4 py-3 text-[1.1rem] font-medium transition-colors hover:bg-[rgba(128,128,128,0.16)]"
                      style={{ color: isActive(n.href) ? 'var(--color-accent)' : 'var(--color-text)' }}
                    >
                      {n.label}
                    </Link>
                  </motion.div>
                ))}
              </div>
              <div className="flex flex-col gap-3 px-6 pb-8">
                <a
                  href={EXPLORER}
                  target="_blank"
                  rel="noreferrer"
                  className="flex w-full items-center justify-center gap-1.5 rounded-full py-3.5 text-[0.95rem] font-semibold text-white"
                  style={{ background: 'var(--color-accent)' }}
                >
                  Arc Explorer <ArrowSquareOut size={14} weight="bold" />
                </a>
                <span
                  className="flex w-full items-center justify-center gap-2 rounded-full py-3.5 text-[0.95rem] font-medium"
                  style={{ background: 'var(--color-card)', color: 'var(--color-text)' }}
                >
                  <span className="dot-live" /> live on Arc testnet
                </span>
              </div>
            </motion.aside>
          </>
        ) : null}
      </AnimatePresence>
    </header>
  );
}
