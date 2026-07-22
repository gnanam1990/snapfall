import { Package } from '@phosphor-icons/react/dist/ssr';
import PageHeader from '@/components/PageHeader';
import Card, { CardBody } from '@/components/Card';

export default function JobsPage() {
  return (
    <>
      <PageHeader
        title="Jobs"
        sub="Lifecycle, task graph, budgets, advance card, settlement."
      />
      <Card><CardBody className="flex items-center gap-3">
        <Package size={20} weight="regular" color='var(--color-muted)' />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Job detail arrives with V7. The Overview carries the live demo spine.
        </p>
      </CardBody></Card>
    </>
  );
}
