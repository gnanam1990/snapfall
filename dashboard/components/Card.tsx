import type { ReactNode } from 'react';

/**
 * Card system: flat layered surface, 14px radius, a real header row with a hairline divider, and an
 * optional nested Well canvas for visual content.
 *
 * Ported from feat/ui-rework, where the same system was written in Tailwind utilities. This
 * dashboard carries no UI dependencies (no Tailwind, no framer-motion, no icon package), so the API
 * is preserved and the implementation moves to the plain-CSS classes in app/globals.css, which
 * resolve every colour through the existing design tokens. See the "Card system" block there for
 * the token mapping and the measured contrast ratios.
 *
 * These are additive: the older .card / .card-title classes and the pages using them are untouched.
 */
export default function Card({
  children,
  className = '',
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={`card-shell ${className}`.trim()}>{children}</div>;
}

/** Header row: sentence-case title left, quiet meta right, hairline divider below. */
export function CardHeader({ title, meta }: { title: string; meta?: ReactNode }) {
  return (
    <div className="card-head">
      <h3 className="card-heading">{title}</h3>
      {meta ? <span className="card-meta">{meta}</span> : null}
    </div>
  );
}

export function CardBody({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`card-body ${className}`.trim()}>{children}</div>;
}

/** Nested inset canvas for visuals: the graph stage, media, code. */
export function Well({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`card-well ${className}`.trim()}>{children}</div>;
}

/**
 * Back-compat kicker for card sections that have no header row. A real heading, so screen readers
 * can navigate card sections (review: PR #10 a11y).
 */
export function CardTitle({ children }: { children: ReactNode }) {
  return <h3 className="card-heading card-heading-block">{children}</h3>;
}
