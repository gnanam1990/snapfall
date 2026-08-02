'use client';

/**
 * V7 — the job detail page.
 *
 * Everything the owner needs to explain one job's money: where it is in the lifecycle, what
 * the advance cost, how much of the operating budget the workforce has spent, what was
 * delivered, and how settlement divided the escrow — each figure traceable to the chain or to
 * a streamed event, with explorer links rather than assertions.
 *
 * Two sources, deliberately split by what each is good for:
 *   - contract state (/api/jobs/[jobId]) is authoritative for amounts and lifecycle;
 *   - the H2 event stream supplies the live timeline, filtered to this job.
 */

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useParams } from 'next/navigation';
import Link from 'next/link';
import type { StreamMessage } from '@/lib/types';
import type { ActivityMessage } from '@/lib/activity';
import { humanizeStreamEvent } from '@/lib/activity';
import { formatUsdc, formatBps, relativeTime } from '@/lib/format';
import { useEventStream } from '@/lib/useEventStream';
import type { JobSnapshot } from '@/lib/jobChain';
import { belongsToJob, expenseRows } from '@/lib/jobTimeline';

/** Lifecycle order as JobVault declares it; the alt terminals sit outside the happy path. */
const HAPPY_PATH = ['Created', 'Funded', 'InProgress', 'Delivered', 'Accepted'] as const;
const TERMINAL_ALT = new Set(['Refunded', 'Cancelled']);

function Row({ label, children, mono }: { label: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className="job-row">
      <span className="job-row-label">{label}</span>
      <span className={`job-row-value${mono ? ' mono' : ''}`}>{children}</span>
    </div>
  );
}

function Hash({ value }: { value: string }) {
  return (
    <span className="mono" title={value}>
      {value.slice(0, 10)}…{value.slice(-8)}
    </span>
  );
}

/**
 * Per-expense provenance.
 *
 * The page previously showed only a SUM read from the contract, so "0.10 spent" had nothing
 * behind it: no merchant, no time, no transaction to open. These rows come from the
 * ExpenseRecorded chain events the H2 relay delivers -- before that relay existed they were
 * indexed and then never reached the browser, so this could not have been built.
 *
 * Derived from the SAME filtered timeline rendered below rather than from a second fetch: two
 * lists built from two sources drift, and a per-row list that disagrees with the total above it
 * is worse than showing no rows.
 */
function ExpenseRows({ timeline, total }: { timeline: ActivityMessage[]; total: string | null }) {
  const rows = useMemo(() => expenseRows(timeline), [timeline]);
  if (rows.length === 0) return null;
  return (
    <div className="card mt">
      <div className="job-expense-head">
        <p className="card-title">Expenses</p>
        {total ? <span className="stat-sub">{formatUsdc(total)} on chain</span> : null}
      </div>
      <ol className="job-expenses">
        {rows.map((row) => (
          <li key={row.id} className="job-expense">
            <span className="job-expense-text">{row.text}</span>
            <span className="job-expense-meta">
              {row.amountUsdc ? (
                <span className="job-expense-amount">{formatUsdc(row.amountUsdc)}</span>
              ) : null}
              <span>{relativeTime(row.at)}</span>
              {row.explorerUrl ? (
                <a className="feed-link" href={row.explorerUrl} target="_blank" rel="noreferrer noopener">
                  transaction
                </a>
              ) : null}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * Mints the customer's magic link and puts it on the clipboard.
 *
 * V9's portal worked and nothing in the product could produce a link to it: the mint endpoint had
 * no proxy and no UI, so the only route to the customer surface was to assemble the URL by hand
 * from a curl response. This is the owner's side of the handover, on the page where the owner is
 * already looking at the job.
 *
 * The token is shown ONCE and rotates any prior credential (the daemon says so in its response),
 * so this deliberately renders the full link for copying rather than storing it anywhere.
 */
function AcceptLink({ jobId }: { jobId: string }) {
  const [link, setLink] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  const mint = useCallback(async () => {
    setBusy(true);
    setError(null);
    setCopied(false);
    try {
      const res = await fetch(`/api/jobs/${encodeURIComponent(jobId)}/accept-link`, { method: 'POST' });
      const body = (await res.json()) as { acceptToken?: string; error?: { message?: string } };
      if (!res.ok || !body.acceptToken) {
        // Surface the daemon's own reason. A nil MintAccept seam answers 503 NOT_WIRED, which is
        // a different problem from "no such job", and flattening them wastes the operator's time.
        setError(body?.error?.message ?? `mint failed (${res.status})`);
        return;
      }
      const url = `${window.location.origin}/portal/${encodeURIComponent(jobId)}?token=${encodeURIComponent(body.acceptToken)}`;
      setLink(url);
      try {
        await navigator.clipboard.writeText(url);
        setCopied(true);
      } catch {
        // Clipboard access is denied outside a secure context or without permission. The link is
        // rendered regardless, so the operator can still select it by hand rather than being told
        // nothing happened.
        setCopied(false);
      }
    } catch {
      setError('the dashboard could not reach its API');
    } finally {
      setBusy(false);
    }
  }, [jobId]);

  return (
    <div className="card mt">
      <div className="job-expense-head">
        <p className="card-title">Customer link</p>
        <button type="button" className="activity-action" onClick={() => void mint()} disabled={busy}>
          {busy ? 'Minting…' : link ? 'Mint a new link' : 'Mint customer link'}
        </button>
      </div>
      <p className="stat-sub" style={{ margin: '6px 0 0' }}>
        Shown once, and minting again rotates any link already issued for this job.
      </p>
      {error ? <p className="job-link-error">{error}</p> : null}
      {link ? (
        <p className="job-link mono">
          {link}
          {copied ? <span className="job-link-copied">copied</span> : null}
        </p>
      ) : null}
    </div>
  );
}

export default function JobDetailPage() {
  const jobId = String(useParams().jobId ?? '');
  const [job, setJob] = useState<JobSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [timeline, setTimeline] = useState<ActivityMessage[]>([]);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`/api/jobs/${encodeURIComponent(jobId)}`, { cache: 'no-store' });
      const body = (await res.json()) as JobSnapshot & { error?: string };
      if (res.status === 404) {
        setMissing(true);
        setJob(null);
        return;
      }
      if (!res.ok) {
        setError(body.error ?? `chain read failed (${res.status})`);
        return;
      }
      setJob(body);
      setMissing(false);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'chain read failed');
    }
  }, [jobId]);

  useEffect(() => {
    void load();
  }, [load]);

  // The stream both fills the timeline and tells us when to re-read the chain: any event
  // touching this job may have moved money, and the contract is the authority on that.
  const onMessage = useCallback(
    (msg: StreamMessage) => {
      if (msg.kind !== 'event') return;
      const item = humanizeStreamEvent(msg);
      // Fails CLOSED, and joins both vocabularies. The old inline predicate was wrong in both
      // directions: `item.jobId && item.jobId !== jobId` ADMITTED events carrying no job id at
      // all, and DROPPED every daemon event, whose business id never equals the bytes32 route
      // param. See lib/jobTimeline.ts.
      if (!belongsToJob(item, { vaultJobId: jobId })) return;
      setTimeline((prev) => [item, ...prev.filter((p) => p.id !== item.id)].slice(0, 40));
      void load();
    },
    [jobId, load],
  );
  const status = useEventStream('/api/events/stream', onMessage);

  const stageIndex = useMemo(
    () => (job?.status ? HAPPY_PATH.indexOf(job.status as (typeof HAPPY_PATH)[number]) : -1),
    [job?.status],
  );

  if (missing) {
    return (
      <>
        <div className="topbar">
          <div>
            <h1 className="page-title">Job not found</h1>
            <p className="page-sub">The vault has no record of this job id.</p>
          </div>
        </div>
        <div className="card">
          <p className="stat-sub mono" style={{ marginTop: 0 }}>{jobId}</p>
          <p className="stat-sub">
            A job exists on chain only after <code>createJob</code>. If you just seeded a run, check{' '}
            <code>.demo/current.json</code> for this take&apos;s job id. <Link href="/jobs">Back to jobs</Link>
          </p>
        </div>
      </>
    );
  }

  if (error) {
    return (
      <>
        <div className="topbar">
          <h1 className="page-title">Job detail</h1>
        </div>
        <div className="card">
          <p className="card-title">Could not read the chain</p>
          <p className="stat-sub" style={{ marginBottom: 12 }}>{error}</p>
          <button className="activity-filter is-active" onClick={() => void load()}>
            Retry
          </button>
        </div>
      </>
    );
  }

  if (!job) {
    return (
      <>
        <div className="topbar">
          <h1 className="page-title">Job detail</h1>
        </div>
        <div className="loading">Reading the job from the vault…</div>
      </>
    );
  }

  const advance = job.advance;
  const drawn = advance?.drawn ?? false;

  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Job detail</h1>
          <p className="page-sub mono">{job.jobId}</p>
        </div>
        {status === 'live' ? (
          <span className="badge-live">live · chain-read on every event</span>
        ) : (
          <span className="badge-live badge-reconnecting">reconnecting…</span>
        )}
      </div>

      {/* lifecycle */}
      <div className="card">
        <p className="card-title">Lifecycle</p>
        {job.status && TERMINAL_ALT.has(job.status) ? (
          <p className="job-alt-terminal">
            This job ended as <b>{job.status}</b> — the escrow returned to the customer and any open
            advance was written off through the pool&apos;s loss waterfall.
          </p>
        ) : (
          <ol className="job-stages">
            {HAPPY_PATH.map((stage, i) => {
              const state = i < stageIndex ? 'done' : i === stageIndex ? 'current' : 'ahead';
              return (
                <li key={stage} className={`job-stage is-${state}`}>
                  <span className="job-stage-dot" />
                  <span className="job-stage-name">{stage}</span>
                </li>
              );
            })}
          </ol>
        )}
        <div className="job-grid mt">
          <Row label="Customer" mono>{job.customer}</Row>
          <Row label="Operator" mono>{job.operator}</Row>
          <Row label="Terms hash">{job.termsHash ? <Hash value={job.termsHash} /> : '—'}</Row>
          <Row label="Deadline">{job.deadline ? new Date(job.deadline).toLocaleString() : '—'}</Row>
        </div>
      </div>

      <div className="grid cols-2 mt">
        {/* advance card */}
        <div className="card">
          <p className="card-title">Advance</p>
          {drawn && advance ? (
            <>
              <div className="job-figure">
                {formatUsdc(advance.principalUsdc)} <span className="u">USDC</span>
                <span className={`job-chip ${advance.open ? 'is-open' : 'is-repaid'}`}>
                  {advance.open ? 'Open' : 'Repaid'}
                </span>
              </div>
              <div className="job-grid mt">
                <Row label="Fee (200 bps)">{formatUsdc(advance.feeUsdc)} USDC</Row>
                <Row label="Owed to the pool">
                  <b>{formatUsdc(advance.repaymentUsdc)} USDC</b>
                </Row>
                <Row label="Rate at draw">
                  {job.orgRateBps === null ? '—' : formatBps(job.orgRateBps)}
                </Row>
              </div>
              <p className="stat-sub">
                The waterfall repays this in full before the operator receives anything, in the same
                transaction as acceptance.
              </p>
            </>
          ) : (
            <>
              <div className="job-figure job-figure-muted">no advance drawn</div>
              <p className="stat-sub">
                {job.orgRateBps === null
                  ? 'The org rate is unavailable, so the available advance cannot be shown.'
                  : `At the org's current ${formatBps(job.orgRateBps)} rate this job could draw ${formatUsdc(
                      ((BigInt(job.customerPaymentUsdc ?? '0') * BigInt(job.orgRateBps)) / 10_000n).toString(),
                    )} USDC.`}
              </p>
            </>
          )}
        </div>

        {/* settlement */}
        <div className="card">
          <p className="card-title">Settlement</p>
          <div className="job-grid">
            <Row label="Customer payment (escrow)">
              {job.customerPaymentUsdc === null ? '—' : `${formatUsdc(job.customerPaymentUsdc)} USDC`}
            </Row>
            <Row label="Repaid to the pool first">
              {drawn && advance ? `${formatUsdc(advance.repaymentUsdc)} USDC` : '0.00 USDC'}
            </Row>
            <Row label="Operator receives">
              <b>{job.operatorNetUsdc === null ? '—' : `${formatUsdc(job.operatorNetUsdc)} USDC`}</b>
            </Row>
            <Row label="Delivery hash">
              {job.deliveryHash ? <Hash value={job.deliveryHash} /> : <span className="stat-sub">not submitted</span>}
            </Row>
          </div>
          <p className="stat-sub">
            {job.status === 'Accepted'
              ? 'Settled. Both transfers happened in one transaction, pool first.'
              : 'Figures are what settlement would pay out at the current chain state.'}
          </p>
          <div className="job-links">
            <a href={job.explorer.jobVault} target="_blank" rel="noreferrer">JobVault on the explorer ↗</a>
            <a href={job.explorer.floatPool} target="_blank" rel="noreferrer">FloatPool on the explorer ↗</a>
          </div>
        </div>
      </div>

      {/* budget */}
      <div className="card mt">
        <p className="card-title">Operating budget</p>
        <div className="job-budget">
          <div className="job-budget-bar">
            <div
              className="job-budget-fill"
              style={{ width: `${Math.min(100, (job.budgetUsedBps ?? 0) / 100)}%` }}
            />
          </div>
          <div className="job-budget-legend">
            <span>
              <b>{job.onchainExpensesUsdc === null ? '—' : formatUsdc(job.onchainExpensesUsdc)}</b> spent
            </span>
            <span>
              {job.budgetRemainingUsdc === null ? '—' : formatUsdc(job.budgetRemainingUsdc)} remaining
            </span>
            <span className="stat-sub">
              of {job.maxOperatingBudgetUsdc === null ? '—' : formatUsdc(job.maxOperatingBudgetUsdc)} USDC
            </span>
          </div>
        </div>
        <p className="stat-sub">
          Expenses recorded on chain against the budget bound (SC-JV-003). Purchases are paid from the
          advance in the treasury, so escrow stays whole for the waterfall.
        </p>
      </div>

      {/* timeline */}
      <ExpenseRows timeline={timeline} total={job?.onchainExpensesUsdc ?? null} />

      <AcceptLink jobId={jobId} />

      <div className="card mt">
        <p className="card-title">Timeline</p>
        {timeline.length === 0 ? (
          <p className="stat-sub" style={{ margin: 0 }}>
            Waiting for events on this job. The chain figures above are already current.
          </p>
        ) : (
          <ol className="job-timeline">
            {timeline.map((item) => (
              <li key={item.id} className={`job-event tone-${item.tone}`}>
                <span className="job-event-dot" />
                <div className="job-event-body">
                  <div className="job-event-head">
                    <span className="job-event-actor">{item.actor}</span>
                    <span className="job-event-text">{item.text}</span>
                    {item.amountUsdc ? (
                      <span className="job-event-amount">{formatUsdc(item.amountUsdc)}</span>
                    ) : null}
                  </div>
                  <div className="job-event-meta">
                    <span className="mono">{item.kind}</span>
                    <span>·</span>
                    <span>{relativeTime(item.at)}</span>
                    {item.explorerUrl ? (
                      <>
                        <span>·</span>
                        <a
                          className="feed-link"
                          href={item.explorerUrl}
                          target="_blank"
                          rel="noreferrer noopener"
                        >
                          transaction
                        </a>
                      </>
                    ) : null}
                  </div>
                </div>
              </li>
            ))}
          </ol>
        )}
      </div>

      <p className="stat-sub mt">
        Chain state read at {new Date(job.observedAt).toLocaleTimeString()}. <Link href="/jobs">All jobs</Link>
      </p>
    </>
  );
}
