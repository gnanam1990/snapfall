const TINTS: Record<string, string> = {
  Funded: 'var(--pos)',
  Accepted: 'var(--pos)',
  Repaid: 'var(--pos)',
  InProgress: 'var(--sky)',
  Delivered: 'var(--sky)',
  DeliveryReady: 'var(--sky)',
  Issued: 'var(--color-accent)',
  AwaitingFunding: 'var(--warn)',
  Draft: 'var(--color-faint)',
  WrittenOff: 'var(--neg)',
  Refunded: 'var(--neg)',
  Cancelled: 'var(--color-faint)',
  Failed: 'var(--neg)',
};

/** Status as dot + label (the modern register), not a filled pill. */
export default function Badge({ kind }: { kind: string }) {
  const tint = TINTS[kind] ?? 'var(--color-muted)';
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-[12.5px] font-medium" style={{ color: tint }}>
      <span className="h-1.5 w-1.5 rounded-full" style={{ background: tint }} />
      {kind}
    </span>
  );
}
