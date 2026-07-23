'use client';

import { useEffect, useState } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { MoonStars, Sun } from '@phosphor-icons/react';

const KEY = 'snapfall-theme';
const EVT = 'snapfall-theme-change';
type Theme = 'light' | 'dark';

/** Sun/Moon theme toggle. The boot script in layout.tsx sets data-theme before paint.
 *  All mounted instances (desktop + mobile) stay in sync by re-reading the document on a
 *  shared change event instead of holding independent state (review: PR #10). */
export default function ThemeToggle() {
  const [theme, setTheme] = useState<Theme | null>(null);

  useEffect(() => {
    const read = () => setTheme((document.documentElement.dataset.theme as Theme) || 'light');
    read();
    window.addEventListener(EVT, read);
    window.addEventListener('storage', read);
    return () => {
      window.removeEventListener(EVT, read);
      window.removeEventListener('storage', read);
    };
  }, []);

  const toggle = () => {
    const current = (document.documentElement.dataset.theme as Theme) || 'light';
    const next: Theme = current === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem(KEY, next);
    } catch {
      /* private mode etc. */
    }
    window.dispatchEvent(new Event(EVT));
  };

  return (
    <motion.button
      aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
      onClick={toggle}
      whileTap={{ scale: 0.9 }}
      className="flex h-10 w-10 flex-none items-center justify-center rounded-full"
      style={{
        background: 'var(--color-card)',
        border: '1px solid var(--color-border)',
        color: 'var(--color-text)',
        cursor: 'pointer',
      }}
    >
      <AnimatePresence mode="wait" initial={false}>
        {theme === null ? null : (
          <motion.span
            key={theme}
            initial={{ rotate: -60, opacity: 0, scale: 0.6 }}
            animate={{ rotate: 0, opacity: 1, scale: 1 }}
            exit={{ rotate: 60, opacity: 0, scale: 0.6 }}
            transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
            className="flex"
          >
            {theme === 'dark' ? <Sun size={18} weight="regular" /> : <MoonStars size={18} weight="regular" />}
          </motion.span>
        )}
      </AnimatePresence>
    </motion.button>
  );
}
