import type { Icon } from '@phosphor-icons/react';

/** HQ icon tile: Phosphor duotone glyph on a tinted gradient with a soft tinted ring.
 *  The gradient + ring + duotone layering is what makes these read rich instead of wiry. */
export default function IconChip({
  icon: Glyph,
  tint,
  size = 'md',
}: {
  icon: Icon;
  tint: string;
  size?: 'sm' | 'md' | 'lg';
}) {
  const px = size === 'lg' ? 44 : size === 'md' ? 34 : 30;
  const glyph = size === 'lg' ? 24 : size === 'md' ? 18 : 16;
  return (
    <span
      className="flex flex-none items-center justify-center"
      style={{
        width: px,
        height: px,
        borderRadius: px * 0.32,
        color: tint,
        background: `linear-gradient(135deg, color-mix(in srgb, ${tint} 22%, var(--color-card)), color-mix(in srgb, ${tint} 7%, var(--color-card)))`,
        border: `1px solid color-mix(in srgb, ${tint} 28%, transparent)`,
        boxShadow: `0 2px 8px color-mix(in srgb, ${tint} 18%, transparent)`,
      }}
    >
      <Glyph size={glyph} weight="duotone" />
    </span>
  );
}
