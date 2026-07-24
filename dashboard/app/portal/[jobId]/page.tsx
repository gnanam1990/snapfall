'use client';

/**
 * V9 — the customer portal. A token-scoped magic-link surface for the SETTLEMENT
 * principal (the customer), entirely separate from the owner dashboard: no sidebar, no
 * owner nav, its own credential. The accept token rides the URL (?token=act_…) and is
 * forwarded as the Bearer to the daemon's credential-gated customer routes.
 *
 * Three things, exactly as scoped: status, Accept, receipt. The receipt is the CUSTOMER
 * copy of the invoice — plain-language gaps, no owner internals — served by the daemon's
 * customer-credential route (the copy-serving decision, made concrete).
 */

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { formatUsdc } from '@/lib/format';

type Gap = { stage: string; cause: string };
type Line = { kind: string; block: number; payload: Record<string, string> };
type Invoice = {
  copy: string;
  jobId: string;
  status: string;
  lines: Line[] | null;
  gaps: Gap[] | null;
  totals: Record<string, string>;
  disclaimer: string;
};

function PortalInner() {
  const jobId = String(useParams().jobId ?? '');
  const token = useSearchParams().get('token') ?? '';

  const [stage, setStage] = useState<string | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [invoice, setInvoice] = useState<Invoice | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [authError, setAuthError] = useState(false);

  const authHeaders = useCallback(() => ({ authorization: `Bearer ${token}` }), [token]);

  const loadStatus = useCallback(async () => {
    const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/acceptance`, {
      headers: authHeaders(),
      cache: 'no-store',
    });
    if (res.status === 401) {
      setAuthError(true);
      return;
    }
    const body = await res.json().catch(() => ({}));
    setStage(body.stage ?? null);
    setAccepted(Boolean(body.accepted));
  }, [jobId, authHeaders]);

  const loadReceipt = useCallback(async () => {
    const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/invoice`, {
      headers: authHeaders(),
      cache: 'no-store',
    });
    if (res.ok) {
      const body = await res.json();
      setInvoice(body.invoice ?? null);
    }
  }, [jobId, authHeaders]);

  useEffect(() => {
    if (!token) return;
    void loadStatus();
    void loadReceipt();
  }, [token, loadStatus, loadReceipt]);

  const accept = useCallback(async () => {
    setBusy(true);
    setNote(null);
    try {
      const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/accept`, {
        method: 'POST',
        headers: authHeaders(),
        cache: 'no-store',
      });
      const body = await res.json().catch(() => ({}));
      if (res.ok) {
        setNote(body.state ?? 'accepted');
        await loadStatus();
        await loadReceipt();
      } else {
        setNote(body?.error?.message ?? `error ${res.status}`);
      }
    } finally {
      setBusy(false);
    }
  }, [jobId, authHeaders, loadStatus, loadReceipt]);

  if (!token) {
    return <div className="portal-card"><h1 className="portal-title">Invalid link</h1><p className="portal-sub">This delivery link is missing its access token.</p></div>;
  }
  if (authError) {
    return <div className="portal-card"><h1 className="portal-title">Link expired</h1><p className="portal-sub">This delivery link is no longer valid. Ask the operator for a fresh one.</p></div>;
  }

  const total = invoice?.totals?.operatorNetAtomic ?? invoice?.totals?.fundedAtomic;

  return (
    <div className="portal-card">
      <p className="portal-brand">Snapfall · delivery</p>
      <h1 className="portal-title">Your deliverable is ready</h1>
      <p className="portal-sub">Job {jobId}</p>

      <div className="portal-status">
        <span className={`badge ${accepted ? 'Accepted' : 'Delivered'}`}>{accepted ? 'Accepted' : stage ?? '—'}</span>
      </div>

      {!accepted ? (
        <>
          <p className="portal-body">Review is complete. Accept to release payment and settle the job on chain.</p>
          <button type="button" className="portal-accept" disabled={busy} onClick={() => void accept()}>
            {busy ? 'Settling…' : 'Accept & settle'}
          </button>
        </>
      ) : (
        <p className="portal-body portal-accepted">Thank you — this delivery is accepted and settled.</p>
      )}

      {note ? <p className="portal-note">{note.replace(/-/g, ' ')}</p> : null}

      {invoice ? (
        <div className="portal-receipt">
          <p className="card-title">Receipt</p>
          {invoice.lines && invoice.lines.length > 0 ? (
            <ul className="portal-lines">
              {invoice.lines.map((l, i) => (
                <li key={i}><span>{l.kind}</span><span>{l.payload.amountAtomic ? `${formatUsdc(l.payload.amountAtomic)} USDC` : ''}</span></li>
              ))}
            </ul>
          ) : null}
          {total ? <p className="portal-total">Total <strong>{formatUsdc(total)} USDC</strong></p> : null}
          {invoice.gaps && invoice.gaps.length > 0 ? (
            <ul className="portal-gaps">
              {invoice.gaps.map((g, i) => <li key={i}>{g.stage}: {g.cause}</li>)}
            </ul>
          ) : null}
          {invoice.disclaimer ? <p className="portal-disclaimer">{invoice.disclaimer}</p> : null}
        </div>
      ) : null}
    </div>
  );
}

export default function CustomerPortalPage() {
  return (
    <div className="portal-shell">
      <Suspense fallback={<div className="portal-card"><p className="portal-sub">Loading…</p></div>}>
        <PortalInner />
      </Suspense>
    </div>
  );
}
