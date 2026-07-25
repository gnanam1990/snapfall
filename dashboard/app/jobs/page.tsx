'use client';

/**
 * V7 — the jobs index. A live list of the jobs the daemon is running, each linking to its
 * detail page. The list rides the H2 snapshot (the daemon knows which jobs exist and their
 * business state); the detail page is where chain state becomes authoritative.
 */

import { useCallback, useState } from 'react';
import Link from 'next/link';
import type { JobSummary, StreamMessage } from '@/lib/types';
import { formatUsdc } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';

/**
 * Only a bytes32 JobVault entity can be read from the contracts. The daemon keeps that id
 * separate from the business job id (jobs.vault_job_id), so prefer it and fall back to the
 * business id only when it happens to be bytes32 itself.
 */
const isVaultJobID = (id: string | null | undefined): id is string =>
  typeof id === 'string' && /^0x[0-9a-fA-F]{64}$/.test(id);

const vaultIDOf = (job: JobSummary): string | null =>
  isVaultJobID(job.vaultJobId) ? job.vaultJobId : isVaultJobID(job.jobId) ? job.jobId : null;

export default function JobsPage() {
  const [jobs, setJobs] = useState<JobSummary[] | null>(null);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setJobs(msg.snapshot.activeJobs ?? []);
      return;
    }
    const next = msg.aggregates?.activeJobs;
    if (next) setJobs(next);
  }, []);

  const status = useEventStream('/api/events/stream', onMessage);

  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Jobs</h1>
          <p className="page-sub">Lifecycle, budget, advance and settlement — per job.</p>
        </div>
        {status === 'live' ? (
          <span className="badge-live">live · updates in &lt;2s</span>
        ) : (
          <span className="badge-live badge-reconnecting">reconnecting…</span>
        )}
      </div>

      <div className="card">
        {jobs === null ? (
          <div className="loading" style={{ padding: '24px 0' }}>Connecting to the daemon event stream…</div>
        ) : jobs.length === 0 ? (
          <p className="stat-sub" style={{ margin: 0 }}>
            No active jobs. Seed a demo run with <code>./scripts/seed_demo</code>, which creates and funds
            one on chain.
          </p>
        ) : (
          <table className="t">
            <thead>
              <tr>
                <th>Customer / job</th>
                <th>Price</th>
                <th>State</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <tr key={job.jobId}>
                  <td>
                    <div>{job.customer}</div>
                    <div className="mono">{job.title}</div>
                  </td>
                  <td>{formatUsdc(job.priceUsdc)}</td>
                  <td>
                    <span className={`badge ${job.state}`}>{job.state}</span>
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    {vaultIDOf(job) ? (
                      <Link className="feed-link" href={`/jobs/${vaultIDOf(job)}`}>
                        detail →
                      </Link>
                    ) : (
                      <span
                        className="stat-sub"
                        title="This job has no bytes32 vault entity yet, so there is nothing to read from the contracts"
                      >
                        no chain identity yet
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
