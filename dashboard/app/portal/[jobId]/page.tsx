'use client';

/**
 * V9 — the customer portal. A token-scoped magic-link surface for the SETTLEMENT
 * principal (the customer), entirely separate from the owner dashboard: no sidebar, no
 * owner nav, its own credential. The accept token rides the URL (?token=act_…) and is
 * forwarded as the Bearer to the daemon's credential-gated customer routes.
 *
 * Three things, exactly as scoped: status, Accept, receipt.
 *
 * DELIBERATELY THE PLAINEST SURFACE IN THE PRODUCT. The owner dashboard is a ledger because the
 * owner is reading a system under load. This reader is not. They arrived from one link, they are
 * accepting one deliverable once, and they owe nobody an operator's vocabulary to do it. Same
 * tokens, same flat register, same type scale -- no meters, no state dots beyond the one status
 * that is theirs. The restraint is the design decision here.
 *
 * WHAT THIS SURFACE MAY SHOW is the other half of that. PRODUCT.md: the customer "never sees the
 * operator's internals or any other customer's job. This isolation is a hard product rule, not a
 * preference." The invoice on the wire does not currently honour that -- see CUSTOMER_SAFE below.
 */

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { formatUsdcExact } from '@/lib/format';
import { OwnWalletPanel } from '@/components/OwnWalletPanel';
import { passkeyConfig } from '@/lib/passkeyWallet';
import {
  parseAcceptance,
  portalBadge,
  portalView,
  settlementFromAcceptResponse,
  type AcceptanceRead,
  type Settlement,
} from '@/lib/portalSettlement';

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

  /** The daemon's status read: stage, accepted, the job's on-chain id, and (newer daemons) the
   *  settlement. Accepted is not settled: lib/portalSettlement.ts decides what may be said. */
  const [read, setRead] = useState<AcceptanceRead>({ stage: null, accepted: false, vaultJobId: null, settlement: null });
  /** What this page's own last successful Accept reported, for a daemon whose read omits it. */
  const [lastAccept, setLastAccept] = useState<Settlement | null>(null);
  const [checking, setChecking] = useState(false);
  const [invoice, setInvoice] = useState<Invoice | null>(null);
  // The note is a failed Accept, only. A success is told by the settlement view, never by a
  // note: .portal-note is success-green, so a 503 once rendered as reassuring green text under the
  // heading "Your deliverable is ready", and a state slug like "accepted pending chain" in the
  // same green read as done when nothing had settled.
  const [note, setNote] = useState<string | null>(null);
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
      setRead(parseAcceptance(await res.json().catch(() => ({}))));
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
    try {
      const res = await fetch(`/api/customer/${encodeURIComponent(jobId)}/accept`, {
        method: 'POST',
        headers: authHeaders(),
        cache: 'no-store',
      });
      const body = await res.json().catch(() => ({}));
      if (res.ok) {
        // The outcome is the settlement view below, announced by its own live region.
        setLastAccept(settlementFromAcceptResponse(body));
        await loadStatus();
        await loadReceipt();
      } else {
        setNote(
          body?.error?.message ?? `The acceptance could not be recorded (error ${res.status}).`,
        );
      }
    } catch {
      // The request may have reached the daemon even though its answer did not reach us, so this
      // must not claim that nothing settled. Retrying is safe: the job settles at most once.
      setNote('The acceptance could not be confirmed. Check again, or try once more: the job can only settle once.');
    } finally {
      setBusy(false);
    }
  }, [jobId, authHeaders, loadStatus, loadReceipt]);

  const checkAgain = useCallback(async () => {
    setChecking(true);
    setNote(null);
    try {
      await loadStatus();
      await loadReceipt();
    } finally {
      setChecking(false);
    }
  }, [loadStatus, loadReceipt]);

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
  // SELF-CUSTODY MODE: with a Circle client key configured, the customer settles from their own
  // passkey wallet and the daemon-custodial button is not offered at all. Two Accept buttons that
  // settle through different keys would be a choice the customer cannot make well, and the
  // custodial one would revert against a job whose customer is the customer's own wallet.
  const selfCustody = passkeyConfig() !== null;
  const view = portalView(read, lastAccept);
  const badge = portalBadge(view, read.stage);

  const acceptButton = (label: string, className: string) => (
    <button
      type="button"
      className={className}
      disabled={busy || checking || !loaded}
      aria-busy={busy || undefined}
      onClick={() => void accept()}
    >
      {busy ? 'Settling…' : label}
    </button>
  );
  const ownWallet = read.vaultJobId ? (
    <OwnWalletPanel vaultJobId={read.vaultJobId} onAccepted={accept} />
  ) : (
    <p className="portal-body">
      This delivery is not linked to its on-chain escrow yet, so it cannot be settled from your
      wallet. Please ask the operator.
    </p>
  );

  return (
    <div className="portal-card">
      <p className="portal-brand">Snapfall · delivery</p>
      <h1 className="portal-title">Your deliverable is ready</h1>
      <p className="portal-sub portal-jobid">Job {jobId}</p>

      <div className="portal-status">
        {statusUnavailable ? (
          <span className="status-badge">status unavailable</span>
        ) : (
          <span className={`status-badge ${badge.tone}`}>{badge.label}</span>
        )}
      </div>

      {/* What happened to the money is announced, not just painted: one persistent live region
          holds the status sentence, so a change of view (settling, not settled, settled) is read
          out. The actions sit outside it. */}
      <div role="status" aria-live="polite">
        {statusUnavailable ? (
          <p className="portal-body">
            We could not reach the operator’s system to check this delivery just now. Nothing has
            been accepted or settled. Please refresh in a moment, or ask the operator for a fresh
            link.
          </p>
        ) : view === 'settled' ? (
          <p className="portal-body portal-accepted">
            Thank you — this delivery is accepted and settled.
          </p>
        ) : view === 'recorded' ? (
          <p className="portal-body">
            Thank you — your acceptance is recorded. This page cannot confirm whether the payment
            has settled on chain yet; ask the operator if you need that confirmed.
          </p>
        ) : view === 'pending' ? (
          <p className="portal-body">
            Your acceptance is recorded, and the payment is still settling on chain. Check again in
            a moment.
            {selfCustody ? null : ' Trying again is safe: the job can only settle once.'}
          </p>
        ) : view === 'retry' ? (
          <p className="portal-body">
            Your acceptance is recorded, but the payment did not settle on chain. It is still held
            in escrow, and you can try settling again.
          </p>
        ) : selfCustody ? null : (
          <p className="portal-body">
            Review is complete. Accepting releases your escrowed payment and settles the job on
            chain. This cannot be undone.
          </p>
        )}
      </div>

      {statusUnavailable || view === 'settled' || view === 'recorded' ? null : view === 'open' ? (
        selfCustody ? ownWallet : acceptButton('Accept & settle', 'portal-accept')
      ) : (
        // pending or retry: accepted and NOT settled, so there is always a way forward.
        <>
          {selfCustody ? (
            // Self-custody settles from the customer's own wallet; the custodial Accept would
            // revert against it. The panel offers the wallet's accept only while the chain still
            // allows it, and records the Accept with the daemon once its transaction succeeds.
            ownWallet
          ) : view === 'retry' ? (
            acceptButton('Try settling again', 'portal-accept')
          ) : null}
          <div className="portal-wallet-actions portal-recheck">
            <button
              type="button"
              className={view === 'pending' && !selfCustody ? 'portal-accept' : 'portal-secondary'}
              disabled={checking || busy}
              aria-busy={checking || undefined}
              onClick={() => void checkAgain()}
            >
              {checking ? 'Checking…' : 'Check again'}
            </button>
            {view === 'pending' && !selfCustody ? acceptButton('Try settling again', 'portal-secondary') : null}
          </div>
        </>
      )}

      {/* A failed Accept is announced too, in the error register. */}
      <div aria-live="polite" role="status">
        {note ? <p className="portal-note is-error">{note}</p> : null}
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
