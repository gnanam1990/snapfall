import { HandPalm } from '@phosphor-icons/react';
import PageHeader from '@/components/PageHeader';
import Card from '@/components/Card';

export default function SettingsPage() {
  return (
    <>
      <PageHeader
        title="Settings"
        sub="Models, integrations, policies, wallet and chain, global freeze."
      />
      <Card className="flex items-center gap-3">
        <HandPalm size={20} weight="regular" color='var(--color-muted)' />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Includes the global kill switch: stop new tasks, signatures, and advances within 1s.
        </p>
      </Card>
    </>
  );
}
