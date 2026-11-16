'use client';

import { useEffect, useState } from 'react';

export interface TocItem {
  id: string;
  label: string;
}

/**
 * The guide's contents. On wide screens it sits beside the article and marks the section being
 * read; on narrow ones it is a disclosure above the article. The links work without script: the
 * observer only adds the current-section mark.
 */
export default function GuideToc({ items }: { items: TocItem[] }) {
  const [current, setCurrent] = useState<string | null>(items[0]?.id ?? null);

  useEffect(() => {
    const sections = items.map((i) => document.getElementById(i.id)).filter((n): n is HTMLElement => !!n);
    if (!sections.length || typeof IntersectionObserver === 'undefined') return;
    // The section whose top most recently passed the upper third of the viewport is the one being read.
    const visible = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) visible.set(e.target.id, e.isIntersecting);
        const first = items.find((i) => visible.get(i.id));
        if (first) setCurrent(first.id);
      },
      { rootMargin: '-15% 0px -65% 0px' },
    );
    sections.forEach((s) => io.observe(s));
    return () => io.disconnect();
  }, [items]);

  const list = (
    <ol className="guide-toc-list">
      {items.map((i) => (
        <li key={i.id}>
          <a href={`#${i.id}`} className={current === i.id ? 'is-current' : undefined} aria-current={current === i.id ? 'location' : undefined}>
            {i.label}
          </a>
        </li>
      ))}
    </ol>
  );

  return (
    <nav className="guide-toc" aria-label="On this page">
      <p className="guide-toc-title">On this page</p>
      {list}
      <details className="guide-toc-small">
        <summary>On this page</summary>
        {list}
      </details>
    </nav>
  );
}
