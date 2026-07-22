import { PackageSearch } from 'lucide-react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function JobsPage() {
  return (
    <>
      <PageHeader
        title="Jobs"
        sub="Lifecycle, task graph, budgets, advance card, settlement."
      />
      <Card className="flex items-center gap-3">
        <PackageSearch size={18} style={{ color: 'var(--color-muted)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Job detail arrives with V7. The Overview carries the live demo spine.
        </p>
      </Card>
    </>
  );
}
