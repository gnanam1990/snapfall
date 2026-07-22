'use client';

import { motion } from 'framer-motion';
import { fadeUp } from '@/lib/motion';

/** Shared page header in the template's hero style: tight bold heading + muted subtext. */
export default function PageHeader({
  title,
  sub,
  right,
}: {
  title: React.ReactNode;
  sub?: string;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-4 pb-6 pt-2 sm:pt-4">
      <div>
        <motion.h1
          variants={fadeUp}
          custom={0}
          initial="hidden"
          animate="visible"
          className="m-0"
          style={{
            fontSize: 'clamp(1.5rem, 3.4vw, 2.2rem)',
            lineHeight: 1.05,
            letterSpacing: '-0.01em',
          }}
        >
          {title}
        </motion.h1>
        {sub ? (
          <motion.p
            variants={fadeUp}
            custom={1}
            initial="hidden"
            animate="visible"
            className="mb-0 mt-2 max-w-[560px]"
            style={{ color: 'var(--color-muted)', lineHeight: 1.65, fontSize: 'clamp(0.9rem, 2vw, 1rem)' }}
          >
            {sub}
          </motion.p>
        ) : null}
      </div>
      {right ? (
        <motion.div variants={fadeUp} custom={2} initial="hidden" animate="visible">
          {right}
        </motion.div>
      ) : null}
    </div>
  );
}
