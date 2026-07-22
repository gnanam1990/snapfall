'use client';

import type { LucideIcon } from 'lucide-react';
import { motion } from 'framer-motion';
import { fadeUp } from '@/lib/motion';
import Card from './Card';

export default function StatCard({
  label,
  value,
  sub,
  icon: Icon,
  tint = 'var(--color-accent)',
  index = 0,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: LucideIcon;
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
          <span
            className="flex h-10 w-10 flex-none items-center justify-center rounded-xl"
            style={{ background: `color-mix(in srgb, ${tint} 14%, var(--color-card))`, color: tint }}
          >
            <Icon size={19} />
          </span>
        </div>
      </Card>
    </motion.div>
  );
}
