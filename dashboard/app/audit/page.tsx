import { ClipboardText } from '@phosphor-icons/react/dist/ssr';
import PageHeader from '@/components/PageHeader';
import Card, { CardBody } from '@/components/Card';

export default function AuditPage() {
  return (
    <>
      <PageHeader
        title="Audit"
        sub="The receipt: revenue, advance, fee, expenses, margin, hashes, explorer links."
      />
      <Card><CardBody className="flex items-center gap-3">
        <ClipboardText size={20} weight="regular" color='var(--color-muted)' />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          The receipt view is comprehensible without reading raw transactions (FR-UI-005).
        </p>
      </CardBody></Card>
    </>
  );
}
