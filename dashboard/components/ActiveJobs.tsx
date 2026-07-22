import type { JobSummary } from '@/lib/types';
import { formatUsdc } from '@/lib/format';

export default function ActiveJobs({ jobs }: { jobs: JobSummary[] }) {
  if (!jobs.length) return <div className="empty">No active jobs.</div>;
  return (
    <table className="t">
      <thead>
        <tr>
          <th>Customer / job</th>
          <th>Price</th>
          <th>State</th>
        </tr>
      </thead>
      <tbody>
        {jobs.map((j) => (
          <tr key={j.jobId}>
            <td>
              <div>{j.customer}</div>
              <div className="mono">{j.title}</div>
            </td>
            <td>{formatUsdc(j.priceUsdc)}</td>
            <td>
              <span className={`badge ${j.state}`}>{j.state}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
