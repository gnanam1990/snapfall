import type { OpenAdvance } from '@/lib/types';
import { formatUsdc, formatBps } from '@/lib/format';

export default function AdvancesTable({ advances }: { advances: OpenAdvance[] }) {
  if (!advances.length) return <div className="empty">No open advances.</div>;
  return (
    <table className="t">
      <thead>
        <tr>
          <th>Job</th>
          <th>Principal</th>
          <th>Fee</th>
          <th>Rate</th>
          <th>Status</th>
        </tr>
      </thead>
      <tbody>
        {advances.map((a) => (
          <tr key={a.jobId}>
            <td className="mono">{a.jobId}</td>
            <td>{formatUsdc(a.principalUsdc)}</td>
            <td>{formatUsdc(a.feeUsdc)}</td>
            <td>{formatBps(a.rateBps)}</td>
            <td>
              <span className={`badge ${a.status}`}>{a.status}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
