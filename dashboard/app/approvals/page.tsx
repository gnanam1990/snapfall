'use client';

/**
 * V8 — the approvals inbox.
 *
 * An agent has reached a purchase it may not make alone, so the spend is held and waiting for a
 * hand. That is not a metaphor reached for after the fact: it is what the daemon is doing.
 *
 * The direction contract names this surface as the one place colour appears and the place the
 * primary action lives. PRODUCT.md principle 3 constrains how: "Refusing must be as easy and as
 * legible as approving." So approve and refuse are the same size, the same weight and the same
 * distance from the pointer, and neither is styled as the default. Requesting a cheaper source is
 * the third option and sits apart, because it is a different kind of answer rather than a weaker
 * one. A decision already taken is stated as a dot and a word, painted by the row's own state
 * class, so the marker and its lettering can never disagree about what happened.
 *
 * Two things this file deliberately does NOT do:
 *
 *   It does not require a reason before refusing. The worker adapts on the owner's text, so a
 *   reason is worth encouraging, but gating a refusal behind a form field would make refusing
 *   harder than approving and break the principle above. The default the worker will receive is
 *   shown instead, so an empty box is an informed choice rather than an accident.
 *
 *   It does not name a human as the decider. See DECIDED_BY.
 *
 * Deep-linked from the activity feed: /approvals?requestId=…&decision=… moves focus to the
 * matching request and marks the suggested action.
 */

import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { formatUsdcExact, timeUntil } from '@/lib/format';

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

/**
 * A decision the daemon accepted, held on screen after the request leaves the pending list.
 *
 * PendingRequests() returns only StatePending, so a decided request drops out of /api/approvals on
 * the next poll. The confirmation used to be rendered inside that list, which meant a successful
 * decision unmounted its own receipt: the card simply vanished and the owner was told nothing.
 * Failures stayed on screen because a failed decision leaves the request pending -- so the surface
 * made refusal illegible and error legible, the exact inverse of what this product promises.
 */
type Settled = {
  approval: Approval;
  state: string;
  decidedBy: string;
  reason: string;
};

/**
 * The recorded decider.
 *
 * H2 requires a non-empty `by`, and the daemon writes it verbatim into the durable events table
 * (lifecycle.go: `"by": by`) where it becomes the permanent audit claim about who authorised a
 * payment. This constant used to be a teammate's name, which meant every recorded decision
 * asserted that a specific named individual approved or refused money.
 *
 * Nothing authenticates that. withAuth checks a shared bearer token when one is configured and
 * never an identity -- its own comment concedes `by` is "a recorded label, not an authenticated
 * identity". So this records what is actually established: someone at the local console. When
 * real owner identity exists, it replaces this; until then the audit log should not carry a name
 * it cannot stand behind.
 */
const DECIDED_BY = 'local-console';

const ACTION_LABEL: Record<Decision, string> = {
  approve: 'Approve',
  reject: 'Refuse',
  request_alternative: 'Request cheaper',
};

/** What the worker receives when the owner refuses without typing anything (purchasing.go). */
const DEFAULT_REFUSAL = 'owner declined the purchase';

/** Plain-language cover for the daemon's error codes, so a refusal never dead-ends in a token. */
const FAILURE_TEXT: Record<string, string> = {
  APPROVAL_EXPIRED: 'The approval window closed before this decision reached the daemon.',
  ALREADY_DECIDED: 'This request was already decided.',
  STALE_VIEW: 'The request changed while it was on screen. The current version is shown above.',
  UNKNOWN_REQUEST: 'The daemon no longer knows this request.',
  NO_DAEMON: 'No daemon is connected, so the decision was not recorded.',
  UNREACHABLE: 'The owner API did not respond, so the decision was not recorded.',
  UNAUTHENTICATED: 'The owner API rejected this dashboard’s credentials.',
  UPSTREAM: 'The owner API failed to answer.',
  INTERNAL: 'The daemon failed while recording this decision.',
};

/**
 * The GET body's `error` is a string in this route's own failure path but a daemon-shaped object
 * when the upstream answers with one, which used to render as the literal "[object Object]".
 */
function errorText(value: unknown): string | null {
  if (value === null || value === undefined || value === '') return null;
  if (typeof value === 'string') return value;
  if (typeof value === 'object') {
    const shaped = value as { message?: unknown; code?: unknown };
    if (typeof shaped.message === 'string' && shaped.message) return shaped.message;
    if (typeof shaped.code === 'string' && shaped.code) return shaped.code;
  }
  return 'the owner API returned an error it did not describe';
}

function ApprovalsInner() {
  const params = useSearchParams();
  const focusId = params.get('requestId') ?? '';
  const focusDecision = (params.get('decision') as Decision | null) ?? null;

  const [approvals, setApprovals] = useState<Approval[] | null>(null);
  /** 'mock' is the route's marker for "no daemon configured", not for fabricated data. */
  const [source, setSource] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reasons, setReasons] = useState<Record<string, string>>({});
  /** A set, not one id: a single string let a second card re-enable the first mid-flight. */
  const [inFlight, setInFlight] = useState<ReadonlySet<string>>(new Set());
  const [settled, setSettled] = useState<Settled[]>([]);
  const [failures, setFailures] = useState<Record<string, { code: string; message: string }>>({});
  /** Drives the countdown. Held in state so the deadline ticks rather than freezing at load. */
  const [now, setNow] = useState(() => Date.now());

  const focusRef = useRef<HTMLLIElement | null>(null);
  const focusedOnce = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch('/api/approvals', { cache: 'no-store' });
      const body = await res.json();
      setApprovals(body.approvals ?? []);
      setSource(typeof body.source === 'string' ? body.source : null);
      setError(errorText(body.error));
    } catch {
      setError('could not reach the approvals API');
      setApprovals([]);
      setSource(null);
    }
  }, []);

  useEffect(() => {
    void load();
    const poll = window.setInterval(() => void load(), 4000);
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      window.clearInterval(poll);
      window.clearInterval(tick);
    };
  }, [load]);

  const decide = useCallback(
    async (a: Approval, kind: Decision) => {
      setInFlight((s) => new Set(s).add(a.requestId));
      setFailures((f) => {
        const next = { ...f };
        delete next[a.requestId];
        return next;
      });
      try {
        const res = await fetch(`/api/approvals/${encodeURIComponent(a.requestId)}/decision`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          // The intentHash is the one we RENDERED — the daemon 409s if it changed since.
          body: JSON.stringify({
            kind,
            by: DECIDED_BY,
            reason: reasons[a.requestId] ?? '',
            intentHash: a.intentHash,
          }),
        });
        const body = await res.json().catch(() => ({}));
        if (res.ok) {
          // Kept outside the pending list, so the receipt survives the request leaving it.
          setSettled((s) => [
            {
              approval: a,
              state: String(body.state ?? 'decided'),
              decidedBy: String(body.decidedBy ?? DECIDED_BY),
              reason: String(body.reason ?? ''),
            },
            ...s.filter((entry) => entry.approval.requestId !== a.requestId),
          ]);
        } else {
          const code = String(body?.error?.code ?? `HTTP_${res.status}`);
          setFailures((f) => ({
            ...f,
            [a.requestId]: {
              code,
              message:
                FAILURE_TEXT[code] ?? body?.error?.message ?? 'The decision was not recorded.',
            },
          }));
        }
        await load();
      } catch {
        setFailures((f) => ({
          ...f,
          [a.requestId]: { code: 'UNREACHABLE', message: FAILURE_TEXT.UNREACHABLE },
        }));
      } finally {
        setInFlight((s) => {
          const next = new Set(s);
          next.delete(a.requestId);
          return next;
        });
      }
    },
    [reasons, load],
  );

  const list = useMemo(() => approvals ?? [], [approvals]);
  const noDaemon = source === 'mock';

  // The deep link used to only tint a border, which is a colour-only signal that moves nothing.
  useEffect(() => {
    if (focusedOnce.current || !focusId || !focusRef.current) return;
    focusedOnce.current = true;
    focusRef.current.focus();
    focusRef.current.scrollIntoView({ block: 'center' });
  }, [focusId, list]);

  return (
    <>
      <div className="page-header">
        <div className="page-header-text">
          <h1>Approvals</h1>
          <p className="page-header-sub">
            Purchases an agent may not make alone. Nothing moves until you answer.
          </p>
        </div>
        <span className="page-header-aside">
          {error ? (
            <span className="status-badge Failed">owner API error</span>
          ) : noDaemon ? (
            <span className="status-badge Cancelled">no daemon</span>
          ) : (
            <span className={`status-badge ${list.length ? 'InProgress' : 'Accepted'}`}>
              {list.length} awaiting you
            </span>
          )}
        </span>
      </div>

      {/* Results are announced here because a decided request leaves the list underneath. */}
      <div aria-live="polite" className="approval-announce">
        {settled.length
          ? `${settled[0].approval.resource}: ${settled[0].state.replace(/_/g, ' ')}.`
          : ''}
      </div>

      {error ? (
        <div className="card mb">
          <p className="stat-sub">The owner API answered with an error: {error}</p>
        </div>
      ) : null}

      {settled.length ? (
        <ul className="approval-list mb">
          {settled.map((s) => (
            <li key={s.approval.requestId} className={`approval is-settled ${s.state}`}>
              <div className="approval-line">
                <div className="approval-ident">
                  <h2 className="approval-resource">{s.approval.resource}</h2>
                  <p className="approval-origin">
                    <span className="approval-state">{s.state.replace(/_/g, ' ')}</span>
                    {' · recorded as '}
                    {s.decidedBy}
                  </p>
                </div>
                <div className="approval-amount">
                  <span className="approval-figure">{formatUsdcExact(s.approval.amountUsdc)}</span>
                  <span className="approval-unit">USDC</span>
                </div>
              </div>
              <p className="approval-note">
                {s.reason
                  ? `The worker was given your reason: “${s.reason}”`
                  : s.state === 'approved'
                    ? 'Recorded. The worker may now complete this purchase.'
                    : `No reason was typed, so the worker was told: “${DEFAULT_REFUSAL}”.`}
              </p>
            </li>
          ))}
        </ul>
      ) : null}

      {approvals === null ? (
        <div className="card">
          <p className="stat-sub">Reading the inbox…</p>
        </div>
      ) : noDaemon ? (
        // The route returns { approvals: [], source: 'mock' } when no daemon is configured. Saying
        // "0 pending" there is a positive claim about a workforce nobody observed.
        <div className="card">
          <p className="card-title">No daemon connected</p>
          <p>
            Approvals come from the owner’s daemon, which does not run on this public site. Nothing
            can reach this inbox, so it is left empty rather than filled with examples. This is not
            the same as a quiet queue, and the difference is the point.
          </p>
        </div>
      ) : list.length === 0 ? (
        <div className="card">
          <p className="stat-sub">
            Nothing awaiting a decision. Escalations from the workforce appear here within a few
            seconds of being raised.
          </p>
        </div>
      ) : (
        <ul className="approval-list">
          {list.map((a) => {
            const failure = failures[a.requestId];
            const busy = inFlight.has(a.requestId);
            const focused = a.requestId === focusId;
            const countdown = timeUntil(a.expiresAt, now);
            const typed = reasons[a.requestId] ?? '';
            return (
              <li
                key={a.requestId}
                ref={focused ? focusRef : undefined}
                tabIndex={focused ? -1 : undefined}
                className={`approval${focused ? ' is-linked' : ''}${
                  countdown.expired ? ' is-expired' : ''
                }`}
                aria-labelledby={`ap-${a.requestId}`}
              >
                <div className="approval-line">
                  <div className="approval-ident">
                    <h2 className="approval-resource" id={`ap-${a.requestId}`}>
                      {a.resource}
                    </h2>
                    <p className="approval-origin">
                      {a.merchant} · job {a.jobId}
                    </p>
                  </div>
                  <div className="approval-amount">
                    <span className="approval-figure">{formatUsdcExact(a.amountUsdc)}</span>
                    <span className="approval-unit">USDC</span>
                  </div>
                </div>

                <p className="approval-source">
                  raised by the workforce · held by the daemon until you answer
                </p>

                <p className="approval-purpose">{a.purpose}</p>

                <dl className="approval-facts">
                  <div>
                    <dt>window</dt>
                    <dd className={countdown.expired ? 'is-expired' : undefined}>
                      {countdown.text || 'no deadline given'}
                    </dd>
                  </div>
                  <div>
                    <dt>bound to intent</dt>
                    <dd className="approval-hash">{a.intentHash.slice(0, 14)}…</dd>
                  </div>
                  {a.alternativeTo ? (
                    <div>
                      <dt>cheaper alternative to</dt>
                      <dd>{a.alternativeTo}</dd>
                    </div>
                  ) : null}
                </dl>

                <label className="approval-label" htmlFor={`reason-${a.requestId}`}>
                  Reason for the worker
                </label>
                <textarea
                  id={`reason-${a.requestId}`}
                  className="approval-reason"
                  placeholder="e.g. too expensive, find a cheaper source"
                  value={typed}
                  onChange={(e) => setReasons((r) => ({ ...r, [a.requestId]: e.target.value }))}
                />
                <p className="approval-hint">
                  {typed.trim()
                    ? 'The worker adapts on this text, verbatim.'
                    : `Optional. Left empty, a refusal tells the worker only “${DEFAULT_REFUSAL}”.`}
                </p>

                {countdown.expired ? (
                  <p className="approval-note is-expired">
                    This window has closed. The daemon will refuse a decision now, and the worker
                    has already stopped waiting.
                  </p>
                ) : null}

                <div className="approval-actions">
                  {(['approve', 'reject'] as Decision[]).map((kind) => (
                    <button
                      key={kind}
                      type="button"
                      className={`approval-action ${kind}`}
                      disabled={busy || countdown.expired}
                      aria-busy={busy || undefined}
                      // Every card renders the same words, so the label has to carry which
                      // purchase this button spends on.
                      aria-label={`${ACTION_LABEL[kind]} ${a.resource}, ${formatUsdcExact(
                        a.amountUsdc,
                      )} USDC to ${a.merchant}`}
                      data-suggested={focusDecision === kind && focused ? 'true' : undefined}
                      onClick={() => void decide(a, kind)}
                    >
                      {ACTION_LABEL[kind]}
                    </button>
                  ))}
                  <button
                    type="button"
                    className="approval-action alternative"
                    disabled={busy || countdown.expired}
                    aria-busy={busy || undefined}
                    aria-label={`Request a cheaper alternative to ${a.resource} from ${a.merchant}`}
                    data-suggested={
                      focusDecision === 'request_alternative' && focused ? 'true' : undefined
                    }
                    onClick={() => void decide(a, 'request_alternative')}
                  >
                    {ACTION_LABEL.request_alternative}
                  </button>
                </div>

                {failure ? (
                  <p className="approval-note is-failure" role="status">
                    {failure.message} <span className="approval-code">{failure.code}</span>
                  </p>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}

export default function ApprovalsPage() {
  return (
    <Suspense
      fallback={
        <div className="card">
          <p className="stat-sub">Reading the inbox…</p>
        </div>
      }
    >
      <ApprovalsInner />
    </Suspense>
  );
}
