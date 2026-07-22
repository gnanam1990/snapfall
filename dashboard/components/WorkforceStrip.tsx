import { Brain, Search, Package, ShieldCheck, Wallet, Receipt, Bot, type LucideIcon } from 'lucide-react';
import type { AgentCard, AgentStatus } from '@/lib/types';

const STAT_COLOR: Record<AgentStatus, string> = {
  idle: 'var(--color-faint)',
  working: 'var(--sky)',
  waiting: 'var(--warn)',
  'approval-required': 'var(--warn)',
  failed: 'var(--neg)',
  frozen: 'var(--color-muted)',
};

const ROLE_ICON: Record<string, LucideIcon> = {
  Brain: Brain,
  Research: Search,
  Delivery: Package,
  QA: ShieldCheck,
  Funding: Wallet,
  Billing: Receipt,
};

export default function WorkforceStrip({ agents }: { agents: AgentCard[] }) {
  return (
    <div className="flex flex-wrap gap-2">
      {agents.map((a) => {
        const Icon = ROLE_ICON[a.role] ?? Bot;
        return (
          <div
            key={a.id}
            className="flex items-center gap-2 rounded-full py-1.5 pl-2.5 pr-3"
            style={{ background: 'var(--color-card)', border: '1px solid var(--color-border)' }}
          >
            <Icon size={14} style={{ color: 'var(--color-muted)' }} />
            <span className="text-[13px] font-semibold">{a.role}</span>
            <span className="h-[7px] w-[7px] rounded-full" style={{ background: STAT_COLOR[a.status] }} />
            <span className="text-xs" style={{ color: 'var(--color-faint)' }}>{a.status}</span>
          </div>
        );
      })}
    </div>
  );
}
