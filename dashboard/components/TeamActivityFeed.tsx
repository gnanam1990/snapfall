'use client';

import Link from 'next/link';
import { useEffect, useMemo, useState } from 'react';
import type { ActivityFilter, ActivityMessage } from '@/lib/activity';
import { formatUsdc, isSafeExplorerUrl, relativeTime } from '@/lib/format';

const FILTERS: Array<{ value: ActivityFilter; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'work', label: 'Work' },
  { value: 'money', label: 'Money' },
  { value: 'approvals', label: 'Approvals' },
];

function approvalHref(message: ActivityMessage, decision: 'approve' | 'reject' | 'request_alternative'): string {
  const params = new URLSearchParams({
    requestId: message.approval?.requestId ?? '',
    intentHash: message.approval?.intentHash ?? '',
    decision,
  });
  return `/approvals?${params.toString()}`;
}

export default function TeamActivityFeed({
  messages,
  live = false,
}: {
  messages: ActivityMessage[];
  live?: boolean;
}) {
  const [filter, setFilter] = useState<ActivityFilter>('all');
  const [now, setNow] = useState(() => Date.now());
  const visible = useMemo(
    () => (filter === 'all' ? messages : messages.filter((message) => message.filter === filter)),
    [filter, messages],
  );

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);

  return (
    <section className="activity" aria-labelledby="activity-title">
      <div className="activity-head">
        <div className="activity-heading">
          <h2 id="activity-title">Team activity</h2>
          <span className={live ? 'activity-live' : 'activity-live is-waiting'}>{live ? 'Live' : 'Reconnecting'}</span>
        </div>
        <div className="activity-filters" role="group" aria-label="Filter team activity">
          {FILTERS.map((item) => (
            <button
              className={filter === item.value ? 'activity-filter is-active' : 'activity-filter'}
              key={item.value}
              type="button"
              aria-pressed={filter === item.value}
              onClick={() => setFilter(item.value)}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>

      {visible.length ? (
        <ol className="activity-list">
          {visible.map((message) => (
            <li
              className={`activity-item tone-${message.tone}${message.threadKey ? ' has-thread' : ''}`}
              key={message.id}
            >
              <span className="activity-node" aria-hidden="true" />
              <span className="activity-avatar" aria-hidden="true">{message.initials}</span>
              <div className="activity-copy">
                <div className="activity-identity">
                  <strong>{message.actor}</strong>
                  <span>{message.role}</span>
                </div>
                <p>{message.text}</p>
                {message.approval ? (
                  <div className="activity-actions" aria-label={`Decision options for ${message.approval.requestId}`}>
                    <Link className="activity-action approve" href={approvalHref(message, 'approve')}>Approve</Link>
                    <Link className="activity-action reject" href={approvalHref(message, 'reject')}>Reject</Link>
                    <Link className="activity-action alternative" href={approvalHref(message, 'request_alternative')}>
                      Find cheaper
                    </Link>
                  </div>
                ) : null}
              </div>
              <div className="activity-meta">
                <time dateTime={message.at}>{relativeTime(message.at, now)}</time>
                <div className="activity-tags">
                  {message.jobId ? <span className="activity-job">{message.jobId}</span> : null}
                  {message.amountUsdc ? <strong>{formatUsdc(message.amountUsdc)} USDC</strong> : null}
                  {message.explorerUrl && isSafeExplorerUrl(message.explorerUrl) ? (
                    <a href={message.explorerUrl} target="_blank" rel="noreferrer">Explorer ↗</a>
                  ) : null}
                </div>
              </div>
            </li>
          ))}
        </ol>
      ) : (
        <div className="activity-empty">No {filter === 'all' ? '' : `${filter} `}activity yet.</div>
      )}
    </section>
  );
}
