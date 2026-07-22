'use client';

import { useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { AnimatePresence, motion } from 'framer-motion';
import { X, ArrowSquareOut } from '@phosphor-icons/react';
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
  const burgerRef = useRef<HTMLButtonElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const sheetRef = useRef<HTMLElement>(null);

  const isActive = (href: string) => (href === '/' ? pathname === '/' : pathname.startsWith(href));

  // Modal semantics for the sheet: Escape dismisses, focus moves in on open and back to the
  // trigger on close, and Tab stays inside while open (review: PR #10 a11y).
  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false);
        return;
      }
      if (e.key !== 'Tab' || !sheetRef.current) return;
      const focusables = sheetRef.current.querySelectorAll<HTMLElement>('a[href], button:not([disabled])');
      if (!focusables.length) return;
      const first = focusables[0]!;
      const last = focusables[focusables.length - 1]!;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      burgerRef.current?.focus();
    };
  }, [open]);

  return (
    <header className="sticky top-0 z-20 px-5 pb-2 pt-4 sm:px-8">
      <nav
        className="mx-auto flex max-w-[1232px] items-center justify-between rounded-full py-2.5 pl-5 pr-2.5"
        style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)', boxShadow: 'var(--shadow-card)' }}
      >
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
              aria-current={isActive(n.href) ? 'page' : undefined}
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
            <span className="dot-live" /> demo replay · Arc testnet next
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

        {/* mobile: theme toggle + animated hamburger */}
        <div className="flex items-center gap-2 md:hidden">
          <ThemeToggle />
          <button
            ref={burgerRef}
            aria-label={open ? 'Close menu' : 'Open menu'}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
            className="relative flex h-10 w-10 items-center justify-center"
            style={{ color: 'var(--color-text)', background: 'none', border: 'none' }}
          >
            <span className="hamburger-line" style={{ transform: open ? 'rotate(45deg)' : 'translateY(-4px)' }} />
            <span className="hamburger-line" style={{ transform: open ? 'rotate(-45deg)' : 'translateY(4px)' }} />
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
              ref={sheetRef}
              role="dialog"
              aria-modal="true"
              aria-label="Navigation menu"
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
                  ref={closeRef}
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
                      aria-current={isActive(n.href) ? 'page' : undefined}
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
                  <span className="dot-live" /> demo replay · Arc testnet next
                </span>
              </div>
            </motion.aside>
          </>
        ) : null}
      </AnimatePresence>
    </header>
  );
}
