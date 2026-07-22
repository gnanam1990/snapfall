import { UsersThree } from '@phosphor-icons/react/dist/ssr';
import PageHeader from '@/components/PageHeader';
import Card, { CardBody } from '@/components/Card';

export default function WorkforcePage() {
  return (
    <>
      <PageHeader
        title="Workforce"
        sub="Brain, Workers, Funding, Billing. Roles, permissions, current task, limits."
      />
      <Card><CardBody className="flex items-center gap-3">
        <UsersThree size={20} weight="regular" color='var(--color-muted)' />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Employee detail pages land alongside the daemon runtime.
        </p>
      </CardBody></Card>
    </>
  );
}
