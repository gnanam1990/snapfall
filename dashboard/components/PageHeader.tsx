import type { ReactNode } from 'react';

/**
 * Shared page header: one tight bold heading, a muted lede, and an optional right-hand slot.
 *
 * Ported from feat/ui-rework, where the entry animation came from framer-motion (fadeUp with a
 * per-node stagger) and the layout from Tailwind utilities. Both are reproduced by the .page-header
 * block in app/globals.css: two keyframes and two animation delays, disabled under
 * prefers-reduced-motion. Dropping the library also drops the 'use client' the branch needed, so
 * this renders on the server.
 *
 * `sub` is text, not a node, exactly as on the branch: the lede is a paragraph with a measure, and
 * letting callers nest block content in it is how that measure gets lost.
 */
export default function PageHeader({
  title,
  sub,
  right,
}: {
  title: ReactNode;
  sub?: string;
  right?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div className="page-header-text">
        <h1>{title}</h1>
        {sub ? <p className="page-header-sub">{sub}</p> : null}
      </div>
      {right ? <div className="page-header-aside">{right}</div> : null}
    </div>
  );
}
