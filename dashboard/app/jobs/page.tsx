'use client';

import Link from 'next/link';
import TabIndicator from '@/components/TabIndicator';
import { useRouter } from 'next/navigation';
import { useMemo, useState } from 'react';
import { useChainHistory } from '@/lib/useChain';
import { networkLabel } from '@/lib/network';
import type { ChainJob } from '@/lib/chainHistory';
import { AddrLink, Band, ErrorNotice, Freshness, StageText, StepsMini, pct, shortId, timeAgo, usdc, useNow } from '@/components/ui';

type Filter = 'all' | 'open' | 'settled' | 'other';

const FILTERS: { key: Filter; label: string; test: (j: ChainJob) => boolean }[] = [
  { key: 'all', label: 'All', test: () => true },
  { key: 'open', label: 'Open', test: (j) => j.stage === 'Funded' || j.stage === 'InProgress' || j.stage === 'Delivered' },
  { key: 'settled', label: 'Settled', test: (j) => j.stage === 'Settled' },
  { key: 'other', label: 'Unpaid or closed', test: (j) => j.stage === 'Created' || j.stage === 'Refunded' || j.stage === 'Cancelled' },
];

/** Every job the JobVault has seen, most recently active first, decoded from its events. */
export default function JobsPage() {
  const history = useChainHistory();
  const router = useRouter();
  const now = useNow();
  const [filter, setFilter] = useState<Filter>('all');
  const h = history.data;

  const counts = useMemo(() => Object.fromEntries(FILTERS.map((f) => [f.key, h?.jobs.filter(f.test).length ?? 0])), [h]);
  const rows = useMemo(() => h?.jobs.filter(FILTERS.find((f) => f.key === filter)!.test) ?? [], [h, filter]);

  return (
    <>
      <Band
        title="Jobs"
        lede={h ? `${h.totals.jobs} jobs, ${h.totals.settled} settled, ${h.totals.active} still open.` : history.error ? 'Jobs could not be read just now.' : 'Reading the chain…'}
        meta={<Freshness updatedAt={history.updatedAt} error={history.error} network={networkLabel()} />}
      />

      <div className="wrap content">
      {history.error && !h ? <div className="section"><ErrorNotice message={`Jobs could not be read: ${history.error}`} onRetry={history.refresh} /></div> : null}

      <div className="section">
        <div className="tabs" role="tablist" aria-label="Show jobs">
          <TabIndicator />
          {FILTERS.map((f) => (
            <button key={f.key} role="tab" aria-selected={filter === f.key} className={`tab${filter === f.key ? ' is-on' : ''}`} onClick={() => setFilter(f.key)}>
              {f.label}<span className="tab-n">{counts[f.key]}</span>
            </button>
          ))}
        </div>
        <div className="panel" style={{ marginTop: 16 }}>
          {!h ? (
            <div className="empty">{history.loading ? 'Reading the chain…' : 'Unavailable until the chain can be read.'}</div>
          ) : rows.length === 0 ? (
            <div className="empty">{filter === 'all' ? 'No job yet. A job appears the moment a customer creates it on chain.' : 'No job in this group.'}</div>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>Job</th>
                    <th className="hide-md">Customer</th>
                    <th className="hide-sm">Progress</th>
                    <th className="r">Price</th>
                    <th className="r hide-sm">Advance</th>
                    <th className="r">Operator kept</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((j) => (
                    <tr key={`${j.jobId}:${j.createdTx}`} className="is-link" onClick={() => router.push(`/jobs/${j.jobId}`)}>
                      <td>
                        <Link href={`/jobs/${j.jobId}`} className="mono" onClick={(e) => e.stopPropagation()}>{shortId(j.jobId, 8, 4)}</Link>
                        <span className="cell-sub">{timeAgo(j.lastAt, now)}</span>
                      </td>
                      <td className="hide-md" onClick={(e) => e.stopPropagation()}><AddrLink explorer={h.explorerUrl} address={j.customer} /></td>
                      <td className="hide-sm"><StepsMini job={j} /></td>
                      <td className="r"><span className="amount">{usdc(j.paymentUsdc)}</span></td>
                      <td className="r hide-sm">
                        {j.advance ? (
                          <>
                            <span className="amount">{usdc(j.advance.principalUsdc)}</span>
                            <span className="cell-sub">{pct(j.advance.rateBps)}, {j.advance.status === 'Open' ? 'out now' : j.advance.status === 'Repaid' ? 'repaid' : 'written off'}</span>
                          </>
                        ) : <span className="cell-sub">none</span>}
                      </td>
                      <td className="r">{j.settlement ? <span className="amount">{usdc(j.settlement.operatorNetUsdc)}</span> : <span className="cell-sub">not yet</span>}</td>
                      <td><StageText stage={j.stage} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
      </div>
    </>
  );
}
