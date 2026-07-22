import { OctagonPause } from 'lucide-react';
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
        <OctagonPause size={18} style={{ color: 'var(--neg)' }} />
        <p className="m-0 text-sm" style={{ color: 'var(--color-muted)' }}>
          Includes the global kill switch: stop new tasks, signatures, and advances within 1s.
        </p>
      </Card>
    </>
  );
}
