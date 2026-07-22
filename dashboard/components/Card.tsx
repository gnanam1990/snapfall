/** Shared surface: white card, hairline border, soft shadow, generous radius. */
export default function Card({
  children,
  className = '',
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={`rounded-2xl p-5 ${className}`}
      style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)', boxShadow: 'var(--shadow-card)' }}
    >
      {children}
    </div>
  );
}

export function CardTitle({ children }: { children: React.ReactNode }) {
  return (
    <p
      className="m-0 mb-4 text-xs font-semibold uppercase"
      style={{ color: 'var(--color-muted)', letterSpacing: '0.06em' }}
    >
      {children}
    </p>
  );
}
