'use client';

import {
  Brain,
  MagnifyingGlass,
  Package,
  ShieldCheck,
  Wallet,
  Receipt,
  Robot,
  type Icon,
} from '@phosphor-icons/react';
import type { AgentCard, AgentStatus } from '@/lib/types';

const STAT_COLOR: Record<AgentStatus, string> = {
  idle: 'var(--color-faint)',
  working: 'var(--sky)',
  waiting: 'var(--warn)',
  'approval-required': 'var(--warn)',
  failed: 'var(--neg)',
  frozen: 'var(--color-muted)',
};

const ROLE: Record<string, { icon: Icon; tint: string }> = {
  Brain: { icon: Brain, tint: 'var(--color-accent)' },
  Research: { icon: MagnifyingGlass, tint: 'var(--sky)' },
  Delivery: { icon: Package, tint: 'var(--pos)' },
  QA: { icon: ShieldCheck, tint: 'var(--warn)' },
  Funding: { icon: Wallet, tint: 'var(--color-accent)' },
  Billing: { icon: Receipt, tint: 'var(--color-muted)' },
};

export default function WorkforceStrip({ agents }: { agents: AgentCard[] }) {
  return (
    <div className="flex flex-wrap gap-2">
      {agents.map((a) => {
        const { icon: Glyph, tint } = ROLE[a.role] ?? { icon: Robot, tint: 'var(--color-muted)' };
        return (
          <div
            key={a.id}
            className="flex items-center gap-2 rounded-full py-1.5 pl-2.5 pr-3"
            style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)' }}
          >
            <Glyph size={16} weight="duotone" color={tint} />
            <span className="text-[13px] font-semibold">{a.role}</span>
            <span className="h-[7px] w-[7px] rounded-full" style={{ background: STAT_COLOR[a.status] }} />
            <span className="text-xs" style={{ color: 'var(--color-faint)' }}>{a.status}</span>
          </div>
        );
      })}
    </div>
  );
}
