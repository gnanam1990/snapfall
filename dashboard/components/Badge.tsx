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

/** Status pill tinted by state. */
export default function Badge({ kind }: { kind: string }) {
  const tint = TINTS[kind] ?? 'var(--color-muted)';
  return (
    <span
      className="inline-block rounded-full px-2.5 py-0.5 text-[11.5px] font-semibold"
      style={{ background: `color-mix(in srgb, ${tint} 14%, var(--color-card))`, color: tint }}
    >
      {kind}
    </span>
  );
}
