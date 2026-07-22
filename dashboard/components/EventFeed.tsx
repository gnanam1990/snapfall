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
    return <div className="px-5 py-4 text-sm" style={{ color: 'var(--color-muted)' }}>No events yet.</div>;
  }
  return (
    <div className="flex flex-col py-1.5">
      {events.map((e) => {
        const Glyph = CAT[e.category];
        return (
          <motion.div
            key={e.seq}
            initial={{ opacity: 0, y: -10 }}
            animate={{ opacity: 1, y: 0, transition: { duration: 0.35, ease: [0.22, 1, 0.36, 1] } }}
            className="group flex items-center gap-3 px-5 py-[9px] transition-colors hover:bg-[var(--row-hover)]"
          >
            <span className="flex flex-none">
              <Glyph size={16} weight="regular" color="var(--color-faint)" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-3">
                <span className="truncate text-[13px] font-medium">{e.summary}</span>
                {e.amountUsdc ? (
                  <span className="flex-none text-[13px] font-semibold" style={{ fontVariantNumeric: 'tabular-nums' }}>
                    {formatUsdc(e.amountUsdc)}
                  </span>
                ) : null}
              </div>
              <div className="mt-px flex items-center gap-2 text-[11.5px]" style={{ color: 'var(--color-faint)' }}>
                <span className="font-mono">{e.type}</span>
                <span>·</span>
                <span>{relativeTime(e.ts)}</span>
                {e.explorerUrl ? (
                  <a
                    href={e.explorerUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-0.5 font-medium opacity-0 transition-opacity group-hover:opacity-100"
                    style={{ color: 'var(--sky)' }}
                  >
                    explorer <ArrowSquareOut size={11} weight="bold" />
                  </a>
                ) : null}
              </div>
            </div>
          </motion.div>
        );
      })}
    </div>
  );
}
