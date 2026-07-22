import type { OpenAdvance } from '@/lib/types';
import { formatUsdc, formatBps } from '@/lib/format';
import Badge from './Badge';

export default function AdvancesTable({ advances }: { advances: OpenAdvance[] }) {
  if (!advances.length) {
    return <div className="py-1 text-sm" style={{ color: 'var(--color-muted)' }}>No open advances.</div>;
  }
  return (
    <table className="w-full border-collapse" style={{ fontVariantNumeric: 'tabular-nums' }}>
      <thead>
        <tr>
          {['Job', 'Principal', 'Fee', 'Rate', 'Status'].map((h) => (
            <th
              key={h}
              className="pb-2 text-left text-[11px] font-semibold uppercase"
              style={{ color: 'var(--color-faint)', letterSpacing: '0.05em' }}
            >
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {advances.map((a) => (
          <tr key={a.jobId}>
            <td className="py-2.5 font-mono text-[12.5px]" style={{ borderTop: '1px solid var(--color-border)', color: 'var(--color-muted)' }}>
              {a.jobId}
            </td>
            <td className="py-2.5 text-sm font-semibold" style={{ borderTop: '1px solid var(--color-border)' }}>
              {formatUsdc(a.principalUsdc)}
            </td>
            <td className="py-2.5 text-sm" style={{ borderTop: '1px solid var(--color-border)' }}>
              {formatUsdc(a.feeUsdc)}
            </td>
            <td className="py-2.5 text-sm" style={{ borderTop: '1px solid var(--color-border)' }}>
              {formatBps(a.rateBps)}
            </td>
            <td className="py-2.5" style={{ borderTop: '1px solid var(--color-border)' }}>
              <Badge kind={a.status} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
