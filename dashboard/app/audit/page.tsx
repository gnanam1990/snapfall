import { FileCheck } from 'lucide-react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function AuditPage() {
  return (
    <>
      <PageHeader
        title="Audit"
        sub="The receipt: revenue, advance, fee, expenses, margin, hashes, explorer links."
      />
      <Card className="flex items-center gap-3">
        <FileCheck size={18} style={{ color: 'var(--pos)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          The receipt view is comprehensible without reading raw transactions (FR-UI-005).
        </p>
      </Card>
    </>
  );
}
