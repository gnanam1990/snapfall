'use client';

import { useMemo, useState } from 'react';
import { useChainHistory } from '@/lib/useChain';
import { networkLabel } from '@/lib/network';
import { ActivityFeed, Band, ErrorNotice, Freshness, useNow } from '@/components/ui';

type Filter = 'all' | 'jobs' | 'money';

/** The full on-chain timeline: every job step and every pool movement, grouped by day. */
export default function ActivityPage() {
  const history = useChainHistory();
  const now = useNow();
  const [filter, setFilter] = useState<Filter>('all');
  const h = history.data;
  const net = networkLabel();

  const items = useMemo(() => {
    const all = h?.activity ?? [];
    if (filter === 'jobs') return all.filter((a) => a.kind.startsWith('job.'));
    if (filter === 'money') return all.filter((a) => a.amountUsdc !== null);
    return all;
  }, [h, filter]);

  return (
    <>
      <Band
        title="Activity"
        lede={h ? `${h.activity.length} transactions, every one of them checkable.` : history.error ? 'Activity could not be read just now.' : 'Reading the chain…'}
        sub={`Everything that happened on ${net}, newest first. Each line links to the transaction that proves it.`}
        meta={<Freshness updatedAt={history.updatedAt} error={history.error} network={net} />}
      />
      <div className="wrap content">
      {history.error && !h ? <div className="section"><ErrorNotice message={`Activity could not be read: ${history.error}`} onRetry={history.refresh} /></div> : null}
      <div className="section">
        <div className="tabs" role="tablist" aria-label="Show activity">
          {([['all', 'Everything'], ['jobs', 'Job steps'], ['money', 'Money moved']] as const).map(([k, label]) => (
            <button key={k} role="tab" aria-selected={filter === k} className={`tab${filter === k ? ' is-on' : ''}`} onClick={() => setFilter(k)}>
              {label}
            </button>
          ))}
        </div>
        <div className="panel" style={{ marginTop: 16 }}>
          {h ? <ActivityFeed items={items} explorer={h.explorerUrl} groupByDay now={now} /> : <div className="empty">Reading the chain…</div>}
        </div>
      </div>
      </div>
    </>
  );
}
