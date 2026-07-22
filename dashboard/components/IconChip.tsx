import type { Icon } from '@phosphor-icons/react';

/** Quiet icon chip: flat neutral square, hairline border, muted monochrome glyph.
 *  No gradients, no tints, no glows - color belongs to data, not decoration. */
export default function IconChip({
  icon: Glyph,
  size = 'md',
}: {
  icon: Icon;
  size?: 'sm' | 'md' | 'lg';
}) {
  const px = size === 'lg' ? 40 : size === 'md' ? 32 : 28;
  const glyph = size === 'lg' ? 20 : size === 'md' ? 16 : 14;
  return (
    <span
      className="flex flex-none items-center justify-center"
      style={{
        width: px,
        height: px,
        borderRadius: 8,
        color: 'var(--color-muted)',
        background: 'color-mix(in srgb, var(--color-text) 4%, transparent)',
        border: '1px solid var(--color-border)',
      }}
    >
      <Glyph size={glyph} weight="regular" />
    </span>
  );
}
