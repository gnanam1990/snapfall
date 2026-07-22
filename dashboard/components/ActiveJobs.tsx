import type { JobSummary } from '@/lib/types';
import { formatUsdc } from '@/lib/format';
import Badge from './Badge';

export default function ActiveJobs({ jobs }: { jobs: JobSummary[] }) {
  if (!jobs.length) {
    return <div className="py-1 text-sm" style={{ color: 'var(--color-muted)' }}>No active jobs.</div>;
  }
  return (
    <div className="flex flex-col">
      {jobs.map((j, i) => (
        <div
          key={j.jobId}
          className="flex items-center justify-between gap-3 py-3"
          style={{ borderTop: i === 0 ? 'none' : '1px solid var(--color-border)' }}
        >
          <div className="min-w-0">
            <div className="text-sm font-semibold">{j.customer}</div>
            <div className="truncate text-xs" style={{ color: 'var(--color-muted)' }}>{j.title}</div>
          </div>
          <div className="flex flex-none items-center gap-3">
            <span className="text-sm font-bold" style={{ fontVariantNumeric: 'tabular-nums' }}>
              {formatUsdc(j.priceUsdc)}
            </span>
            <Badge kind={j.state} />
          </div>
        </div>
      ))}
    </div>
  );
}
