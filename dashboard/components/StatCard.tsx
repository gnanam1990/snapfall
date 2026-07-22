'use client';

import type { Icon } from '@phosphor-icons/react';
import { motion } from 'framer-motion';
import { fadeUp } from '@/lib/motion';
import Card from './Card';
import IconChip from './IconChip';

export default function StatCard({
  label,
  value,
  sub,
  icon,
  tint = 'var(--color-accent)',
  index = 0,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: Icon;
  tint?: string;
  index?: number;
}) {
  return (
    <motion.div variants={fadeUp} custom={index} initial="hidden" animate="visible">
      <Card>
        <div className="flex items-start justify-between">
          <div>
            <div className="text-[12.5px] font-medium" style={{ color: 'var(--color-muted)' }}>
              {label}
            </div>
            <div
              className="mt-1.5 text-[26px]"
              style={{ fontFamily: 'var(--font-heading), sans-serif', letterSpacing: '-0.02em', fontVariantNumeric: 'tabular-nums' }}
            >
              {value}
            </div>
            {sub ? (
              <div className="mt-1 text-xs" style={{ color: 'var(--color-faint)' }}>
                {sub}
              </div>
            ) : null}
          </div>
          <IconChip icon={icon} tint={tint} size="lg" />
        </div>
      </Card>
    </motion.div>
  );
}
