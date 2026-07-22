import { ShieldAlert } from 'lucide-react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function ApprovalsPage() {
  return (
    <>
      <PageHeader
        title="Approvals"
        sub="Approve, reject, or request a cheaper alternative. The rejection beat lives here."
      />
      <Card className="flex items-center gap-3">
        <ShieldAlert size={18} style={{ color: 'var(--warn)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          The approvals inbox arrives with V8, wired to the H2 approval lifecycle.
        </p>
      </Card>
    </>
  );
}
