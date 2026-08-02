'use client';

/**
 * V7 — the jobs index, drawn as a schedule.
 *
 * What this surface may honestly show is narrower than it looks, and the redesign is mostly about
 * admitting that. docs/handshakes/h2-overview-aggregates.contract.json is explicit:
 *
 *   "activeJob": ["jobId", "priceUsdc"]
 *   "daemonOmits": { "JobSummary": ["vaultJobId", "customer", "title", "state"] }
 *
 * So against a real daemon the wire carries a chain entity id and a funded amount, and nothing
 * else. customer and state live in the daemon's own `jobs` table, title only in Brain's per-job
 * memory, and none of the three is in chain_job_financials -- which is the projection this list is
 * built from. The contract's comment says the dashboard "renders a dash" for them. It did not: it
 * rendered empty <div>s and a `class="badge undefined"` pill with no text, so three of the four
 * visible columns were silently blank against the only deployment that matters.
 *
 * The old subtitle promised "Lifecycle, budget, advance and settlement — per job". Of those, only
 * the advance is actually reachable here, by joining openAdvances on jobId -- both sides are
 * chain_job_financials.job_id, so the join is sound. Budget and settlement exist only on the
 * detail page, behind a per-job chain read. The subtitle now says what the row can source.
 *
 * The list is also NOT live, despite what the old badge claimed. computeAggregates runs once, for
 * the opening snapshot; neither daemon nor chain event frames carry an `aggregates` block, so
 * nothing here updates after connect. Saying "updates in <2s" over a list that never changes is a
 * latency claim the surface cannot keep.
 */

import { useCallback, useState } from 'react';
import Link from 'next/link';
import type { JobSummary, OpenAdvance, StreamMessage } from '@/lib/types';
import { formatUsdcExact } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import Badge from '@/components/Badge';

/**
 * Only a bytes32 JobVault entity can be read from the contracts. The daemon keeps that id
 * separate from the business job id (jobs.vault_job_id), so prefer it and fall back to the
 * business id only when it happens to be bytes32 itself -- which, under a real daemon, is the
 * usual case: chain_job_financials.job_id IS the vault entity.
 */
const isVaultJobID = (id: string | null | undefined): id is string =>
  typeof id === 'string' && /^0x[0-9a-fA-F]{64}$/.test(id);

const vaultIDOf = (job: JobSummary): string | null =>
  isVaultJobID(job.vaultJobId) ? job.vaultJobId : isVaultJobID(job.jobId) ? job.jobId : null;

const shortHash = (id: string) => (id.length > 18 ? `${id.slice(0, 10)}…${id.slice(-6)}` : id);

export default function JobsPage() {
  const [jobs, setJobs] = useState<JobSummary[] | null>(null);
  const [advances, setAdvances] = useState<OpenAdvance[]>([]);
  const [demo, setDemo] = useState(false);
  /** False on the public, daemon-less deploy. Ignoring it is how this page claimed "0 jobs". */
  const [daemonConnected, setDaemonConnected] = useState<boolean | null>(null);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setDemo(msg.demo === true);
      setDaemonConnected(msg.snapshot.daemonConnected !== false);
      setJobs(msg.snapshot.activeJobs ?? []);
      setAdvances(msg.snapshot.openAdvances ?? []);
      return;
    }
    // Event frames carry no aggregates against a real daemon, so this only fires under the demo
    // replay. Merge rather than replace: the aggregate shape is the two-field chain projection,
    // and assigning it wholesale used to wipe customer/title/state off rows that had them.
    const next = msg.aggregates?.activeJobs;
    if (next) {
      setJobs((prev) => {
        if (!prev) return next;
        const byId = new Map(prev.map((j) => [j.jobId, j]));
        return next.map((j) => ({ ...(byId.get(j.jobId) ?? {}), ...j }) as JobSummary);
      });
    }
    if (msg.aggregates?.openAdvances) setAdvances(msg.aggregates.openAdvances);
  }, []);

  const status = useEventStream('/api/events/stream', onMessage);
  const noDaemon = daemonConnected === false;

  return (
    <>
      <div className="page-header">
        <div className="page-header-text">
          <h1>Jobs</h1>
          <p className="page-header-sub">
            Every job the chain projection has seen funded, with the advance drawn against it.
            Budget and settlement are per job, on the detail page.
          </p>
        </div>
        <span className="page-header-aside">
          {noDaemon ? (
            <span className="status-badge Cancelled">no daemon</span>
          ) : demo ? (
            <span className="status-badge AwaitingFunding">demo replay</span>
          ) : status === 'live' ? (
            // Not "updates in <2s": nothing re-sends this list after the opening snapshot.
            <span className="status-badge Funded">connected · list as at connect</span>
          ) : (
            <span className="status-badge AwaitingFunding">connecting…</span>
          )}
        </span>
      </div>

      {noDaemon ? (
        <div className="card">
          <p className="card-title">No daemon connected</p>
          <p>
            The job list comes from the owner’s daemon, which does not run on this public site. It
            is left empty rather than filled with examples. The pool and advance figures on{' '}
            <Link className="job-open" href="/float">
              Float
            </Link>{' '}
            are read straight from Arc testnet and do not need a daemon.
          </p>
        </div>
      ) : jobs === null ? (
        <div className="card">
          <p className="stat-sub">Waiting for the first snapshot from the daemon event stream…</p>
        </div>
      ) : jobs.length === 0 ? (
        <div className="card">
          <p className="stat-sub">
            The daemon is connected and has no funded jobs in its chain projection. A job appears
            here once its escrow is funded on chain.
          </p>
        </div>
      ) : (
        <ul className="job-list">
          {jobs.map((job) => {
            const vault = vaultIDOf(job);
            // Both sides are chain_job_financials.job_id, so this join is sound.
            const advance = advances.find((a) => a.jobId === job.jobId);
            return (
              <li key={job.jobId} className="job-row">
                <div className="job-line">
                  <div className="job-ident">
                    <h2 className="job-title">
                      {job.title || (vault ? shortHash(vault) : job.jobId)}
                    </h2>
                    <p className="job-origin">
                      {job.customer || (
                        <span className="is-absent">
                          customer is daemon-side and not in this projection
                        </span>
                      )}
                    </p>
                  </div>
                  <div className="job-amount">
                    <span className="job-figure">{formatUsdcExact(job.priceUsdc)}</span>
                    <span className="job-unit">USDC</span>
                  </div>
                </div>

                <p className="job-source">escrowed price · indexer projection of JobVault</p>

                <dl className="job-facts">
                  <div>
                    <dt>lifecycle</dt>
                    <dd>
                      {job.state ? (
                        <Badge kind={job.state} />
                      ) : (
                        <span className="is-absent">not carried by the chain projection</span>
                      )}
                    </dd>
                  </div>
                  <div>
                    <dt>advance drawn</dt>
                    <dd>
                      {advance ? (
                        <>
                          {formatUsdcExact(advance.principalUsdc)} USDC{' '}
                          <span className="job-sub">
                            + {formatUsdcExact(advance.feeUsdc)} fee · {advance.status}
                          </span>
                        </>
                      ) : (
                        <span className="is-absent">none open</span>
                      )}
                    </dd>
                  </div>
                  <div>
                    <dt>chain identity</dt>
                    <dd className="job-hash">
                      {vault ? (
                        shortHash(vault)
                      ) : (
                        <span className="is-absent">no vault entity yet</span>
                      )}
                    </dd>
                  </div>
                </dl>

                {vault ? (
                  <Link
                    className="job-open"
                    href={`/jobs/${vault}`}
                    // Every row renders the same words, so the name has to carry the job.
                    aria-label={`Open the detail page for ${job.title || shortHash(vault)}`}
                  >
                    Open job detail <span aria-hidden="true">→</span>
                  </Link>
                ) : (
                  <p className="job-blocked">
                    This job has no bytes32 vault entity yet, so there is nothing to read from the
                    contracts and no detail page to open.
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}
