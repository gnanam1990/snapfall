'use client';

/**
 * V8 — the approvals inbox. Pending H2 approval requests with approve / reject /
 * request-alternative, posting to the daemon's decision endpoint through the
 * /api/approvals proxy. Each decision binds to the intentHash the owner was SHOWN
 * (the daemon rejects a stale view at click time), and the structured reason the owner
 * types is what the worker adapts on — the rejection beat, rendered verbatim.
 *
 * Deep-linked from the activity feed: /approvals?requestId=…&decision=… highlights the
 * matching card and pre-selects the action.
 */

import { Suspense, useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { formatUsdc, relativeTime } from '@/lib/format';

type Approval = {
  requestId: string;
  jobId: string;
  intentHash: string;
  merchant: string;
  resource: string;
  amountUsdc: string; // atomic micros
  purpose: string;
  expiresAt: string;
  alternativeTo: string;
};

type Decision = 'approve' | 'reject' | 'request_alternative';

// The recorded decider. There is no owner-identity auth in the dashboard yet; the
// daemon treats `by` as a recorded label (the bearer token is the authenticated
// identity when configured). Real owner auth replaces this constant.
const DECIDED_BY = 'gnanam';

const ACTION_LABEL: Record<Decision, string> = {
  approve: 'Approve',
  reject: 'Reject',
  request_alternative: 'Request cheaper',
};

function ApprovalsInner() {
  const params = useSearchParams();
  const focusId = params.get('requestId') ?? '';
  const focusDecision = (params.get('decision') as Decision | null) ?? null;

  const [approvals, setApprovals] = useState<Approval[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [pending, setPending] = useState<string | null>(null); // requestId being decided
  const [results, setResults] = useState<Record<string, { state: string; reason: string }>>({});

  const load = useCallback(async () => {
    try {
      const res = await fetch('/api/approvals', { cache: 'no-store' });
      const body = await res.json();
      setApprovals(body.approvals ?? []);
      setError(body.error ?? null);
    } catch {
      setError('could not reach the approvals API');
      setApprovals([]);
    }
  }, []);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 4000);
    return () => window.clearInterval(t);
  }, [load]);

  const decide = useCallback(
    async (a: Approval, kind: Decision) => {
      setPending(a.requestId);
      try {
        const res = await fetch(`/api/approvals/${encodeURIComponent(a.requestId)}/decision`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          // The intentHash is the one we RENDERED — the daemon 409s if it changed since.
          body: JSON.stringify({ kind, by: DECIDED_BY, reason: reasons[a.requestId] ?? '', intentHash: a.intentHash }),
        });
        const body = await res.json().catch(() => ({}));
        if (res.ok) {
          setResults((r) => ({ ...r, [a.requestId]: { state: body.state, reason: body.reason ?? '' } }));
          await load();
        } else {
          const code = body?.error?.code ?? `HTTP ${res.status}`;
          setResults((r) => ({ ...r, [a.requestId]: { state: code, reason: body?.error?.message ?? '' } }));
          if (code === 'STALE_VIEW') await load(); // re-render against the current view
        }
      } catch {
        setResults((r) => ({ ...r, [a.requestId]: { state: 'UNREACHABLE', reason: 'the owner API did not respond' } }));
      } finally {
        setPending(null);
      }
    },
    [reasons, load],
  );

  const list = useMemo(() => approvals ?? [], [approvals]);

  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Approvals</h1>
          <p className="page-sub">Approve, reject, or request a cheaper alternative — the rejection beat.</p>
        </div>
        <span className={`badge ${error ? 'Failed' : list.length ? 'InProgress' : ''}`}>
          {error ? 'API offline' : `${list.length} pending`}
        </span>
      </div>

      {approvals === null ? (
        <div className="card"><p className="stat-sub">Loading the inbox…</p></div>
      ) : list.length === 0 ? (
        <div className="card">
          <p className="stat-sub">
            Nothing awaiting a decision.{' '}
            {error ? `(${error})` : 'Escalations from the workforce appear here in real time.'}
          </p>
        </div>
      ) : (
        list.map((a) => {
          const result = results[a.requestId];
          const focused = a.requestId === focusId;
          return (
            <div key={a.requestId} className="card" style={focused ? { borderColor: 'var(--accent)' } : undefined}>
              <div className="approval-head">
                <div>
                  <p className="card-title" style={{ margin: 0 }}>{a.resource}</p>
                  <p className="stat-sub" style={{ marginTop: 4 }}>{a.merchant} · job {a.jobId}</p>
                </div>
                <div className="approval-amount">
                  <span className="stat-value">{formatUsdc(a.amountUsdc)}</span>
                  <span className="stat-label"> USDC</span>
                </div>
              </div>

              <p className="approval-purpose">{a.purpose}</p>
              <p className="stat-sub">
                Expires {relativeTime(a.expiresAt)}
                {a.alternativeTo ? <span className="badge Issued" style={{ marginLeft: 8 }}>alternative to {a.alternativeTo}</span> : null}
              </p>

              <textarea
                className="approval-reason"
                placeholder="Reason (the worker adapts on this — e.g. “too expensive, find a cheaper source”)"
                value={reasons[a.requestId] ?? ''}
                onChange={(e) => setReasons((r) => ({ ...r, [a.requestId]: e.target.value }))}
              />

              <div className="activity-actions">
                {(['approve', 'reject', 'request_alternative'] as Decision[]).map((kind) => (
                  <button
                    key={kind}
                    type="button"
                    className={`activity-action ${kind === 'request_alternative' ? 'alternative' : kind}`}
                    style={focusDecision === kind ? { outline: '2px solid var(--accent)' } : undefined}
                    disabled={pending === a.requestId}
                    onClick={() => void decide(a, kind)}
                  >
                    {ACTION_LABEL[kind]}
                  </button>
                ))}
              </div>

              {result ? (
                <p className={`approval-result ${result.state}`}>
                  <strong>{result.state}</strong>
                  {result.reason ? ` — ${result.reason}` : ''}
                </p>
              ) : null}
            </div>
          );
        })
      )}
    </>
  );
}

export default function ApprovalsPage() {
  return (
    <Suspense fallback={<div className="card"><p className="stat-sub">Loading…</p></div>}>
      <ApprovalsInner />
    </Suspense>
  );
}
