import type { OpenAdvance } from '@/lib/types';
import { formatUsdc, formatBps } from '@/lib/format';
import Badge from './Badge';

export default function AdvancesTable({ advances }: { advances: OpenAdvance[] }) {
  if (!advances.length) {
    return <div className="text-sm" style={{ color: 'var(--color-muted)' }}>No open advances.</div>;
  }
  return (
    <table className="w-full border-collapse" style={{ fontVariantNumeric: 'tabular-nums' }}>
      <thead>
        <tr>
          {['Job', 'Principal', 'Fee', 'Rate', 'Status'].map((h) => (
            <th
              key={h}
              className="pb-2 text-left text-[12px] font-medium"
              style={{ color: 'var(--color-faint)' }}
            >
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {advances.map((a) => (
          <tr key={a.jobId} className="transition-colors hover:bg-[var(--row-hover)]">
            <td className="py-2.5 font-mono text-[12px]" style={{ borderTop: '1px solid var(--color-divider)', color: 'var(--color-muted)' }}>
              {a.jobId}
            </td>
            <td className="py-2.5 text-[13px] font-semibold" style={{ borderTop: '1px solid var(--color-divider)' }}>
              {formatUsdc(a.principalUsdc)}
            </td>
            <td className="py-2.5 text-[13px]" style={{ borderTop: '1px solid var(--color-divider)' }}>
              {formatUsdc(a.feeUsdc)}
            </td>
            <td className="py-2.5 text-[13px]" style={{ borderTop: '1px solid var(--color-divider)' }}>
              {formatBps(a.rateBps)}
            </td>
            <td className="py-2.5" style={{ borderTop: '1px solid var(--color-divider)' }}>
              <Badge kind={a.status} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
