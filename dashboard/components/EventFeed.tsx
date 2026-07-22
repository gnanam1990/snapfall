'use client';

import { motion } from 'framer-motion';
import {
  Lightning,
  CreditCard,
  ShieldWarning,
  Package,
  Tray,
  ListChecks,
  Robot,
  CursorClick,
  ClipboardText,
  ArrowSquareOut,
  type Icon,
} from '@phosphor-icons/react';
import type { FinancialEvent, EventCategory } from '@/lib/types';
import { formatUsdc, relativeTime } from '@/lib/format';
import IconChip from './IconChip';

const CAT: Record<EventCategory, Icon> = {
  Float: Lightning,
  Finance: CreditCard,
  Approval: ShieldWarning,
  Job: Package,
  Intake: Tray,
  Task: ListChecks,
  Agent: Robot,
  Action: CursorClick,
  Audit: ClipboardText,
};

export default function EventFeed({ events }: { events: FinancialEvent[] }) {
  if (!events.length) {
    return <div className="py-2 text-sm" style={{ color: 'var(--color-muted)' }}>No events yet.</div>;
  }
  return (
    <div className="flex flex-col">
      {events.map((e, i) => {
        const icon = CAT[e.category];
        return (
          <motion.div
            key={e.seq}
            initial={{ opacity: 0, y: -10 }}
            animate={{ opacity: 1, y: 0, transition: { duration: 0.35, ease: [0.22, 1, 0.36, 1] } }}
            className="flex items-start gap-3 py-3"
            style={{ borderTop: i === 0 ? 'none' : '1px solid var(--color-border)' }}
          >
            <span className="mt-0.5">
              <IconChip icon={icon} size="md" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-2">
                <span className="text-sm font-medium">{e.summary}</span>
                {e.amountUsdc ? (
                  <span className="flex-none text-sm font-bold" style={{ fontVariantNumeric: 'tabular-nums' }}>
                    {formatUsdc(e.amountUsdc)}
                  </span>
                ) : null}
              </div>
              <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs" style={{ color: 'var(--color-faint)' }}>
                <span className="font-mono text-[11px]">{e.type}</span>
                <span>·</span>
                <span>{relativeTime(e.ts)}</span>
                {e.explorerUrl ? (
                  <>
                    <span>·</span>
                    <a
                      href={e.explorerUrl}
                      target="_blank"
                      rel="noreferrer"
                      className="inline-flex items-center gap-0.5 font-medium"
                      style={{ color: 'var(--sky)' }}
                    >
                      explorer <ArrowSquareOut size={11} weight="bold" />
                    </a>
                  </>
                ) : null}
              </div>
            </div>
          </motion.div>
        );
      })}
    </div>
  );
}
