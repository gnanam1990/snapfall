import { ShieldWarning } from '@phosphor-icons/react';
import PageHeader from '@/components/PageHeader';
import Card, { CardBody } from '@/components/Card';

export default function ApprovalsPage() {
  return (
    <>
      <PageHeader
        title="Approvals"
        sub="Approve, reject, or request a cheaper alternative. The rejection beat lives here."
      />
      <Card><CardBody className="flex items-center gap-3">
        <ShieldWarning size={20} weight="regular" color='var(--color-muted)' />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          The approvals inbox arrives with V8, wired to the H2 approval lifecycle.
        </p>
      </CardBody></Card>
    </>
  );
}
