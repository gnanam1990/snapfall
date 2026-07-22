import type { AgentCard, AgentStatus } from '@/lib/types';

const STAT_COLOR: Record<AgentStatus, string> = {
  idle: 'var(--muted-2)',
  working: 'var(--sky)',
  waiting: 'var(--warn)',
  'approval-required': 'var(--warn)',
  failed: 'var(--neg)',
  frozen: 'var(--muted)',
};

export default function WorkforceStrip({ agents }: { agents: AgentCard[] }) {
  return (
    <div className="agents">
      {agents.map((a) => (
        <div className="agent" key={a.id}>
          <span className="adot" style={{ background: STAT_COLOR[a.status] }} />
          <span className="arole">{a.role}</span>
          <span className="astat">{a.status}</span>
        </div>
      ))}
    </div>
  );
}
