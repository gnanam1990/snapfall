import { Landmark } from 'lucide-react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function FloatPage() {
  return (
    <>
      <PageHeader
        title="Float"
        sub="TVL, utilization, fees, first-loss reserve, org rate, loss waterfall."
      />
      <Card className="flex items-center gap-3">
        <Landmark size={18} style={{ color: 'var(--color-accent)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          The Float page is Anandan&apos;s A10, read straight from FloatPool on-chain state.
        </p>
      </Card>
    </>
  );
}
