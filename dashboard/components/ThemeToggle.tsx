'use client';

import { useEffect, useState } from 'react';

const KEY = 'snapfall-theme';
const EVT = 'snapfall-theme-change';

export type Theme = 'light' | 'dark';

/** Dark is the default, so only an explicit light preference is stored as an attribute. */
export function readTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}

/**
 * Sun/moon theme switch.
 *
 * The document is the single source of truth, not component state: the boot script in
 * layout.tsx sets data-theme before first paint, and every mounted instance re-reads the
 * document on a shared event. Holding independent state instead lets two toggles disagree
 * about the theme they are both controlling (review finding on the original toggle), and a
 * `storage` listener extends the same sync to other tabs.
 *
 * No icon or animation dependency: the dashboard ships no UI libraries and this is two paths.
 */
export default function ThemeToggle() {
  // null until mounted. The server cannot know the stored preference, so rendering either icon
  // during SSR would be a hydration mismatch on every load that chose the other one.
  const [theme, setTheme] = useState<Theme | null>(null);

  useEffect(() => {
    const read = () => setTheme(readTheme());
    read();
    window.addEventListener(EVT, read);
    window.addEventListener('storage', read);
    return () => {
      window.removeEventListener(EVT, read);
      window.removeEventListener('storage', read);
    };
  }, []);

  const toggle = () => {
    const next: Theme = readTheme() === 'dark' ? 'light' : 'dark';
    if (next === 'light') document.documentElement.dataset.theme = 'light';
    else delete document.documentElement.dataset.theme;
    try {
      localStorage.setItem(KEY, next);
    } catch {
      // Private browsing and blocked storage: the choice still applies to this page, it just
      // does not survive a reload. Losing persistence must not lose the toggle.
    }
    window.dispatchEvent(new Event(EVT));
  };

  const isDark = theme === 'dark';
  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={toggle}
      // The label names the ACTION, so there is deliberately no aria-pressed: pairing the two
      // makes a screen reader announce "switch to dark mode, pressed", which contradicts itself.
      // A state-style label ("Dark mode") would be the alternative that wants aria-pressed.
      aria-label={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
    >
      {theme === null ? (
        <span className="theme-toggle-icon" aria-hidden="true" />
      ) : isDark ? (
        <svg className="theme-toggle-icon" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="12" r="4.2" />
          <g strokeLinecap="round">
            <path d="M12 2.6v2.4M12 19v2.4M2.6 12h2.4M19 12h2.4M5.3 5.3l1.7 1.7M17 17l1.7 1.7M18.7 5.3L17 7M7 17l-1.7 1.7" />
          </g>
        </svg>
      ) : (
        <svg className="theme-toggle-icon" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M20.2 14.4A8.6 8.6 0 1 1 9.6 3.8a7 7 0 0 0 10.6 10.6z" />
        </svg>
      )}
    </button>
  );
}
