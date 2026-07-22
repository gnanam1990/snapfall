import { Users } from 'lucide-react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function WorkforcePage() {
  return (
    <>
      <PageHeader
        title="Workforce"
        sub="Brain, Workers, Funding, Billing. Roles, permissions, current task, limits."
      />
      <Card className="flex items-center gap-3">
        <Users size={18} style={{ color: 'var(--color-muted)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Employee detail pages land alongside the daemon runtime.
        </p>
      </Card>
    </>
  );
}
