'use client';

/** Route-level error boundary: a malformed frame or render fault degrades to a
 *  recoverable card instead of blanking the dashboard (review: PR #8 HIGH). */
export default function Error({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <div className="card" style={{ margin: '40px auto', maxWidth: 480, textAlign: 'center' }}>
      <p className="card-title">Something broke while rendering</p>
      <p className="stat-sub" style={{ margin: '0 0 16px' }}>
        {error.message || 'Unexpected error.'} The event stream keeps running; you can retry rendering.
      </p>
      <button
        onClick={reset}
        style={{
          background: 'var(--accent)',
          // Not a literal: the light theme's accent is a DARK iris, so a hardcoded near-black
          // foreground here rendered at roughly 1.5:1. --on-accent is the pinned pair.
          color: 'var(--on-accent)',
          border: 'none',
          borderRadius: 'var(--radius-sm)',
          padding: '8px 18px',
          fontWeight: 600,
          cursor: 'pointer',
        }}
      >
        Retry
      </button>
    </div>
  );
}
