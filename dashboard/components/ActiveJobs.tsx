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
              {/* customer/title/state are daemon-side (jobs.customer_ref / jobs.status; title
                  lives in Brain memory) with no chain equivalent, so the H2 aggregate omits them
                  (daemonOmits.JobSummary). The agreed decision is to leave them as dashes, not to
                  re-source them — so render an explicit — when absent, otherwise an omitted field
                  reads as a broken cell (empty div, `badge undefined`) rather than an intentional
                  gap. #79. */}
              <div>{j.customer || '—'}</div>
              <div className="mono">{j.title || '—'}</div>
            </td>
            <td>{formatUsdc(j.priceUsdc)}</td>
            <td>
              {j.state ? (
                <span className={`badge ${j.state}`}>{j.state}</span>
              ) : (
                <span className="badge">—</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
