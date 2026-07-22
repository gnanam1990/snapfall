'use client';

import { motion } from 'framer-motion';
import {
  Zap,
  CreditCard,
  ShieldAlert,
  PackageCheck,
  Inbox,
  ListChecks,
  Bot,
  MousePointerClick,
  FileCheck,
  ExternalLink,
  type LucideIcon,
} from 'lucide-react';
import type { FinancialEvent, EventCategory } from '@/lib/types';
import { formatUsdc, relativeTime } from '@/lib/format';

const CAT: Record<EventCategory, { icon: LucideIcon; tint: string }> = {
  Float: { icon: Zap, tint: 'var(--color-accent)' },
  Finance: { icon: CreditCard, tint: 'var(--sky)' },
  Approval: { icon: ShieldAlert, tint: 'var(--warn)' },
  Job: { icon: PackageCheck, tint: 'var(--pos)' },
  Intake: { icon: Inbox, tint: 'var(--color-faint)' },
  Task: { icon: ListChecks, tint: 'var(--color-muted)' },
  Agent: { icon: Bot, tint: 'var(--color-muted)' },
  Action: { icon: MousePointerClick, tint: 'var(--sky)' },
  Audit: { icon: FileCheck, tint: 'var(--color-muted)' },
};

export default function EventFeed({ events }: { events: FinancialEvent[] }) {
  if (!events.length) {
    return <div className="py-2 text-sm" style={{ color: 'var(--color-muted)' }}>No events yet.</div>;
  }
  return (
    <div className="flex flex-col">
      {events.map((e, i) => {
        const { icon: Icon, tint } = CAT[e.category];
        return (
          <motion.div
            key={e.seq}
            initial={{ opacity: 0, y: -10 }}
            animate={{ opacity: 1, y: 0, transition: { duration: 0.35, ease: [0.22, 1, 0.36, 1] } }}
            className="flex items-start gap-3 py-3"
            style={{ borderTop: i === 0 ? 'none' : '1px solid var(--color-border)' }}
          >
            <span
              className="mt-0.5 flex h-8 w-8 flex-none items-center justify-center rounded-lg"
              style={{ background: `color-mix(in srgb, ${tint} 12%, white)`, color: tint }}
            >
              <Icon size={15} />
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
                      explorer <ExternalLink size={11} />
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
