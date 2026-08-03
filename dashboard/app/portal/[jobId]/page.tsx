'use client';

/**
 * V9 — the customer portal. A token-scoped magic-link surface for the SETTLEMENT
 * principal (the customer), entirely separate from the owner dashboard: no sidebar, no
 * owner nav, its own credential. The accept token rides the URL (?token=act_…) and is
 * forwarded as the Bearer to the daemon's credential-gated customer routes.
 *
 * Three things, exactly as scoped: status, Accept, receipt.
 *
 * DELIBERATELY NOT AN INSTRUMENT DIAGRAM. The rest of the dashboard was redesigned into a
 * piping-and-instrumentation drawing because the owner is reading a system under load. This
 * reader is not. They arrived from one link, they are accepting one deliverable once, and they
 * have no reason to learn a drafting vocabulary to do it. Same tokens, same flat register, same
 * type scale -- no vessels, no callout leaders, no annotation lettering. The restraint is the
 * design decision here.
 *
 * WHAT THIS SURFACE MAY SHOW is the other half of that. PRODUCT.md: the customer "never sees the
 * operator's internals or any other customer's job. This isolation is a hard product rule, not a
 * preference." The invoice on the wire does not currently honour that -- see CUSTOMER_SAFE below.
 */

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { formatUsdcExact } from '@/lib/format';

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
  deliveryHash?: string;
};

/**
 * The only invoice fields this surface will render, as an allowlist.
 *
 * The customer copy the daemon serves is a shallow copy of the owner copy (billing.go: `customer
 * := owner`) that strips exactly one thing, Gap.Detail. Everything else survives: every Line,
 * including AdvanceIssued and ExpenseRecorded, and all six Totals, including
 * advancePrincipalAtomic, advanceFeeAtomic, expenseTotalAtomic, settlementAdvanceRepaidAtomic and
 * operatorNetAtomic. That is the operator's entire financing position, and it reaches the browser.
 *
 * This page used to render it: the raw line list, and `operatorNetAtomic ?? fundedAtomic` labelled
 * "Total". On a real settled job that reads "Total 0.439 USDC" to a customer who paid 1.00 -- the
 * operator's take after the pool was repaid, presented as the customer's own total, and dropping
 * the moment they accept.
 *
 * An allowlist rather than a denylist, so a Totals field added upstream cannot appear here by
 * default. This does NOT fix the leak: the data is still on the wire and a curl with the token
 * still returns it. The daemon has to build a real customer projection instead of copying the
 * owner one; that is a contract change on the daemon side, flagged rather than papered over.
 */
const CUSTOMER_SAFE = {
  /** What the customer themselves paid into escrow. The only money that is theirs to see. */
  paid: 'fundedAtomic',
} as const;

function PortalInner() {
  const jobId = String(useParams().jobId ?? '');
  const token = useSearchParams().get('token') ?? '';

  const [stage, setStage] = useState<string | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [invoice, setInvoice] = useState<Invoice | null>(null);
  // A note is either the new state after a successful accept, or a failure. They must not look
  // alike: .portal-note is success-green, so a 503 rendered as reassuring green text under the
  // heading "Your deliverable is ready", telling a customer their settlement worked when it had
  // not. The tone travels with the note so the two cannot be confused again.
  const [note, setNote] = useState<string | null>(null);
  const [noteTone, setNoteTone] = useState<'ok' | 'error'>('ok');
  const [busy, setBusy] = useState(false);
  const [authError, setAuthError] = useState(false);
  /** A failed status read is not "no status": it must not leave an Accept button looking ready. */
  const [statusUnavailable, setStatusUnavailable] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const authHeaders = useCallback(() => ({ authorization: `Bearer ${token}` }), [token]);

  const loadStatus = useCallback(async () => {
    try {
      const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/acceptance`, {
        headers: authHeaders(),
        cache: 'no-store',
      });
      if (res.status === 401) {
        setAuthError(true);
        return;
      }
      if (!res.ok) {
        setStatusUnavailable(true);
        return;
      }
      const body = await res.json().catch(() => ({}));
      setStage(body.stage ?? null);
      setAccepted(Boolean(body.accepted));
      setStatusUnavailable(false);
    } catch {
      setStatusUnavailable(true);
    } finally {
      setLoaded(true);
    }
  }, [jobId, authHeaders]);

  const loadReceipt = useCallback(async () => {
    try {
      const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/invoice`, {
        headers: authHeaders(),
        cache: 'no-store',
      });
      if (res.ok) {
        const body = await res.json();
        setInvoice(body.invoice ?? null);
      }
    } catch {
      // A missing receipt is not an error worth alarming the customer with; the status and the
      // Accept action stand on their own, and the receipt simply does not render.
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
    setNoteTone('ok');
    try {
      const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/accept`, {
        method: 'POST',
        headers: authHeaders(),
        cache: 'no-store',
      });
      const body = await res.json().catch(() => ({}));
      if (res.ok) {
        setNote(body.state ?? 'accepted');
        setNoteTone('ok');
        await loadStatus();
        await loadReceipt();
      } else {
        setNote(
          body?.error?.message ?? `The acceptance could not be recorded (error ${res.status}).`,
        );
        setNoteTone('error');
      }
    } catch {
      setNote('The acceptance could not be sent. Nothing was settled; you can try again.');
      setNoteTone('error');
    } finally {
      setBusy(false);
    }
  }, [jobId, authHeaders, loadStatus, loadReceipt]);

  if (!token) {
    return (
      <div className="portal-card">
        <h1 className="portal-title">Invalid link</h1>
        <p className="portal-sub">This delivery link is missing its access token.</p>
      </div>
    );
  }
  if (authError) {
    return (
      <div className="portal-card">
        <h1 className="portal-title">Link expired</h1>
        <p className="portal-sub">
          This delivery link is no longer valid. Ask the operator for a fresh one.
        </p>
      </div>
    );
  }

  const paid = invoice?.totals?.[CUSTOMER_SAFE.paid] ?? null;

  return (
    <div className="portal-card">
      <p className="portal-brand">Snapfall · delivery</p>
      <h1 className="portal-title">Your deliverable is ready</h1>
      <p className="portal-sub portal-jobid">Job {jobId}</p>

      <div className="portal-status">
        {statusUnavailable ? (
          <span className="status-badge">status unavailable</span>
        ) : (
          <span className={`status-badge ${accepted ? 'Accepted' : stage ? 'Delivered' : ''}`}>
            {accepted ? 'Accepted' : (stage ?? 'checking…')}
          </span>
        )}
      </div>

      {statusUnavailable ? (
        <p className="portal-body">
          We could not reach the operator’s system to check this delivery just now. Nothing has
          been accepted or settled. Please refresh in a moment, or ask the operator for a fresh
          link.
        </p>
      ) : !accepted ? (
        <>
          <p className="portal-body">
            Review is complete. Accepting releases your escrowed payment and settles the job on
            chain. This cannot be undone.
          </p>
          <button
            type="button"
            className="portal-accept"
            disabled={busy || !loaded}
            aria-busy={busy || undefined}
            onClick={() => void accept()}
          >
            {busy ? 'Settling…' : 'Accept & settle'}
          </button>
        </>
      ) : (
        <p className="portal-body portal-accepted">
          Thank you — this delivery is accepted and settled.
        </p>
      )}

      {/* The outcome of an irreversible money action has to be announced, not just painted. */}
      <div aria-live="polite" role="status">
        {note ? (
          <p className={`portal-note${noteTone === 'error' ? ' is-error' : ''}`}>
            {noteTone === 'error' ? note : note.replace(/-/g, ' ')}
          </p>
        ) : null}
      </div>

      {invoice ? (
        <div className="portal-receipt">
          <p className="portal-receipt-title">Receipt</p>

          {paid ? (
            <p className="portal-total">
              <span>You paid</span>
              <strong>{formatUsdcExact(paid)} USDC</strong>
            </p>
          ) : (
            <p className="portal-total">
              <span>You paid</span>
              <span className="portal-absent">no on-chain funding record yet</span>
            </p>
          )}

          {invoice.deliveryHash ? (
            <p className="portal-proof">
              Delivery fingerprint <code>{invoice.deliveryHash.slice(0, 18)}…</code> — anchored on
              chain, so what you received can be checked against what was recorded.
            </p>
          ) : null}

          {invoice.gaps && invoice.gaps.length > 0 ? (
            <ul className="portal-gaps">
              {invoice.gaps.map((g, i) => (
                <li key={i}>
                  {g.stage}: {g.cause}
                </li>
              ))}
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
      <Suspense
        fallback={
          <div className="portal-card">
            <p className="portal-sub">Loading…</p>
          </div>
        }
      >
        <PortalInner />
      </Suspense>
    </div>
  );
}
