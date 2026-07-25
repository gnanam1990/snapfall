'use client';

import { useEffect, useState } from 'react';

const KEY = 'snapfall-theme';

export type Theme = 'light' | 'dark';

/** Dark is the default, so only an explicit light preference is stored as an attribute. */
export function readTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}

/** Writes a theme to the document. Dark removes the attribute rather than setting it, so the
 *  default lives in exactly one place (the :root block) instead of two. */
function applyTheme(t: Theme): void {
  if (t === 'light') document.documentElement.dataset.theme = 'light';
  else delete document.documentElement.dataset.theme;
}

// ── document-level theme controller ──
//
// Applying a theme mutates one shared thing: the root element. So exactly one listener does it,
// installed once per document, and mounted toggles are only subscribers that re-read afterwards.
// Doing it per instance instead means N components racing to write the same attribute; it happens
// to be idempotent, which is why it looks harmless, but it makes the number of mounted toggles
// part of how theming behaves, and the original review finding here was precisely about there
// being more than one (review: cubic on #43).

const subscribers = new Set<() => void>();
let installed = false;

function notify(): void {
  for (const s of subscribers) s();
}

function install(): void {
  if (installed || typeof window === 'undefined') return;
  installed = true;

  // Cross-tab. Our document is NOT authoritative here: the other tab changed ITS document, not
  // ours, so re-reading our own data-theme would just report the value we already had. That is
  // exactly how the first version advertised cross-tab sync while doing nothing. The
  // StorageEvent's newValue is the only carrier of the other tab's choice.
  window.addEventListener('storage', (e: StorageEvent) => {
    if (e.storageArea && e.storageArea !== localStorage) return;
    // key === null means the other tab called clear(), removing the preference entirely.
    if (e.key !== null && e.key !== KEY) return;
    applyTheme(e.key === null ? 'dark' : e.newValue === 'light' ? 'light' : 'dark');
    notify();
  });
}

/** Switches the theme, persists it, and tells every mounted toggle to re-read. */
function setThemePreference(next: Theme): void {
  applyTheme(next);
  try {
    localStorage.setItem(KEY, next);
  } catch {
    // Private browsing and blocked storage: the choice still applies to this page, it just does
    // not survive a reload. Losing persistence must not lose the toggle.
  }
  notify();
}

/**
 * Sun/moon theme switch.
 *
 * The document is the single source of truth, not component state: the boot script in layout.tsx
 * sets data-theme before first paint, and every mounted instance re-reads the document when the
 * controller above signals a change. Holding independent state instead lets two toggles disagree
 * about the theme they are both controlling.
 *
 * No icon or animation dependency: the dashboard ships no UI libraries and this is two paths.
 */
export default function ThemeToggle() {
  // null until mounted. The server cannot know the stored preference, so rendering either icon
  // during SSR would be a hydration mismatch on every load that chose the other one.
  const [theme, setTheme] = useState<Theme | null>(null);

  useEffect(() => {
    install();
    const sync = () => setTheme(readTheme());
    sync();
    subscribers.add(sync);
    return () => {
      subscribers.delete(sync);
    };
  }, []);

  const toggle = () => setThemePreference(readTheme() === 'dark' ? 'light' : 'dark');

  const isDark = theme === 'dark';
  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={toggle}
      // The label names the ACTION, so there is deliberately no aria-pressed: pairing the two
      // makes a screen reader announce "switch to dark mode, pressed", which contradicts itself.
      // A state-style label ("Dark mode") would be the alternative that wants aria-pressed.
      //
      // Before mount the theme is genuinely unknown, and naming an action then is a coin flip:
      // `isDark` is false pre-mount, so this used to announce "switch to dark mode" on a page
      // whose default IS dark, describing the opposite of what activation does (review: cubic on
      // #43). A neutral label is correct until the effect resolves the real theme.
      aria-label={theme === null ? 'Toggle colour theme' : isDark ? 'Switch to light mode' : 'Switch to dark mode'}
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
