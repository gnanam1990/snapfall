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

const ROLE: Record<string, Icon> = {
  Brain: Brain,
  Research: MagnifyingGlass,
  Delivery: Package,
  QA: ShieldCheck,
  Funding: Wallet,
  Billing: Receipt,
};

export default function WorkforceStrip({ agents }: { agents: AgentCard[] }) {
  return (
    <div className="flex flex-wrap gap-2">
      {agents.map((a) => {
        const Glyph = ROLE[a.role] ?? Robot;
        return (
          <div
            key={a.id}
            className="flex items-center gap-2 rounded-full py-1.5 pl-2.5 pr-3"
            style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)' }}
          >
            <Glyph size={15} weight="regular" color="var(--color-muted)" />
            <span className="text-[13px] font-semibold">{a.role}</span>
            <span className="h-[7px] w-[7px] rounded-full" style={{ background: STAT_COLOR[a.status] }} />
            <span className="text-xs" style={{ color: 'var(--color-faint)' }}>{a.status}</span>
          </div>
        );
      })}
    </div>
  );
}
