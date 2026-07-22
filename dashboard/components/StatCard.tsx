'use client';

import type { Icon } from '@phosphor-icons/react';
import { motion } from 'framer-motion';
import { fadeUp } from '@/lib/motion';
import Card from './Card';

/** Metric tile: label row with a bare micro-icon, a big tabular number, quiet context.
 *  The number owns the tile; nothing decorates it. */
export default function StatCard({
  label,
  value,
  sub,
  icon: Glyph,
  index = 0,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: Icon;
  index?: number;
}) {
  return (
    <motion.div variants={fadeUp} custom={index} initial="hidden" animate="visible">
      <Card>
        <div className="p-5">
          <div className="flex items-center gap-1.5">
            <Glyph size={14} weight="regular" color="var(--color-faint)" />
            <span className="text-[13px] font-medium" style={{ color: 'var(--color-muted)' }}>
              {label}
            </span>
          </div>
          <div
            className="mt-2 text-[28px] leading-none"
            style={{ fontFamily: 'var(--font-heading), sans-serif', letterSpacing: '-0.02em', fontVariantNumeric: 'tabular-nums' }}
          >
            {value}
          </div>
          {sub ? (
            <div className="mt-2 text-xs" style={{ color: 'var(--color-faint)' }}>
              {sub}
            </div>
          ) : null}
        </div>
      </Card>
    </motion.div>
  );
}
