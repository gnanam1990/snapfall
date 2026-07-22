/** Modern card system: layered surface (inset top highlight in dark), 14px radius,
 *  real header row with divider, optional nested Well canvas for visual content. */

export default function Card({
  children,
  className = '',
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={`overflow-hidden rounded-[14px] ${className}`}
      style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)', boxShadow: 'var(--shadow-card)' }}
    >
      {children}
    </div>
  );
}

/** Header row: sentence-case title left, quiet meta right, hairline divider below. */
export function CardHeader({ title, meta }: { title: string; meta?: React.ReactNode }) {
  return (
    <div
      className="flex min-h-[46px] items-center justify-between gap-3 px-5"
      style={{ borderBottom: '1px solid var(--color-divider)' }}
    >
      <span className="text-[13px] font-semibold">{title}</span>
      {meta ? (
        <span className="flex items-center gap-1.5 text-xs" style={{ color: 'var(--color-faint)' }}>
          {meta}
        </span>
      ) : null}
    </div>
  );
}

export function CardBody({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <div className={`p-5 ${className}`}>{children}</div>;
}

/** Nested inset canvas for visuals (the graph stage, media, code). */
export function Well({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return (
    <div
      className={`rounded-[10px] ${className}`}
      style={{ background: 'var(--color-well)', border: '1px solid var(--color-divider)' }}
    >
      {children}
    </div>
  );
}

/** Back-compat kicker (still used by stub pages until they migrate). */
export function CardTitle({ children }: { children: React.ReactNode }) {
  return <p className="m-0 mb-4 text-[13px] font-semibold">{children}</p>;
}
