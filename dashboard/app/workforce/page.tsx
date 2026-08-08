'use client';

import { useEffect, useMemo, useState } from 'react';

import {
  BUILD_MONITOR_MANIFEST,
  CATALOG_MANIFESTS,
  COMING_SOON_WORKERS,
  activationLabel,
  validHireInput,
  type HireWorkerResult,
  type WorkerActivation,
  type WorkerManifest,
} from '@/lib/workforce';

/**
 * The recorded actor on an activation.
 *
 * See the comment at the hire call. This is a label the daemon stores, not an authenticated
 * identity, so it says what is true: someone operating the local console.
 */
const HIRED_BY = 'local-console';

/**
 * The bounded roles the runtime defines, and the authority each one is held to.
 *
 * NOT a reading of anything. There is no endpoint behind this: /api/workforce serves manifests
 * and activations only. It used to be titled "Current workforce / Active team" with a "5 bounded
 * roles" counter, which asserted five agents were running -- on the public deploy, where none
 * are, and with no source anywhere. It is a statement about how authority is partitioned, which
 * is true whether or not anything is running, and it is now labelled as one.
 *
 * Set as a schedule: the role name and its duty are on the row in words, so there is no icon to
 * decode and nothing a glyph could say that the sentence does not.
 */
const BOUNDED_ROLES = [
  { role: 'Brain', detail: 'Routes work. Cannot spend.' },
  { role: 'Research', detail: 'Reads sources. Cannot submit.' },
  { role: 'Delivery', detail: 'Submits deliverables.' },
  { role: 'QA', detail: 'Verifies. Cannot deliver.' },
  { role: 'Funding', detail: 'Policy-gated. Escalates to you.' },
] as const;

/** A permission is a stated bound: plain text inside a hairline, not a glyph. */
function PermissionChip({ label }: { label: string }) {
  return <span className="permission-chip">{label}</span>;
}

function ActiveTeam() {
  return (
    <section className="workforce-active" aria-labelledby="active-team-title">
      <div className="workforce-panel-head">
        <div>
          <h2 id="active-team-title">Bounded roles</h2>
          <p className="workforce-panel-sub">How authority is partitioned.</p>
        </div>
        <span className="workforce-safe">{BOUNDED_ROLES.length} roles</span>
      </div>
      <ul className="workforce-roles">
        {BOUNDED_ROLES.map((agent) => (
          <li key={agent.role}>
            <strong>{agent.role}</strong>
            <span>{agent.detail}</span>
          </li>
        ))}
      </ul>
      <p className="workforce-caveat">
        This is the authority model, not a live roster. Nothing on this page reads which agents are
        currently running.
      </p>
    </section>
  );
}

function BuildMonitorCard({ manifest, activation, catalogOnly }: { manifest: WorkerManifest; activation: WorkerActivation | null; catalogOnly: boolean }) {
  const [repository, setRepository] = useState(activation?.repository ?? '');
  const [quoteUsdc, setQuoteUsdc] = useState(activation?.quoteUsdc ?? '25.00');
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<HireWorkerResult | null>(null);
  const [error, setError] = useState('');
  const activeResult = result ?? activation;
  const valid = validHireInput(repository, quoteUsdc);

  useEffect(() => {
    if (!activation) return;
    setRepository(activation.repository);
    setQuoteUsdc(activation.quoteUsdc);
  }, [activation]);

  async function hire() {
    if (catalogOnly || !valid || submitting) return; // no daemon → the POST would 502; don't fire it
    setSubmitting(true);
    setError('');
    const fallback = 'Build Monitor could not be activated.';
    try {
      const response = await fetch(`/api/workforce/${encodeURIComponent(manifest.id)}/hire`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        // The daemon requires `by` and calls it the owner identity (ownerapi.go:189-191), then
        // records it. Nothing here authenticates a person -- there is no owner login in this
        // dashboard -- so naming a real teammate made every activation assert that a specific
        // human authorised it. Records what is actually established, matching the same fix made
        // to the approvals decider.
        body: JSON.stringify({
          repository: repository.trim(),
          quoteUsdc: quoteUsdc.trim(),
          by: HIRED_BY,
        }),
      });
      let body: HireWorkerResult & { error?: { message?: string } };
      try {
        body = await response.json() as HireWorkerResult & { error?: { message?: string } };
      } catch {
        setError(fallback);
        return;
      }
      if (!response.ok) {
        setError(body.error?.message ?? fallback);
        return;
      }
      setResult(body);
    } catch {
      setError(fallback);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <article className="manifest-card manifest-featured">
      <div className="manifest-card-head">
        <div className="manifest-identity">
          <div>
            <h3>{manifest.name}</h3>
            <p>{manifest.category}</p>
          </div>
        </div>
        <span className={`manifest-status${activeResult ? ' is-active' : ''}`}>
          {activeResult ? activationLabel(activeResult.state) : 'Ready to hire'}
        </span>
      </div>

      <p className="manifest-description">{manifest.description}</p>
      <div className="permission-row">
        {manifest.permissions.map((permission) => <PermissionChip key={permission} label={permission} />)}
      </div>

      {/* The evidence flow as one line of text: the arrows are operators, not icons. */}
      <p className="watcher-flow">Repository → Brain → Milestone evidence</p>

      <div className="watcher-config">
        <label>
          <span>Repository path</span>
          <input
            value={repository}
            onChange={(event) => setRepository(event.target.value)}
            placeholder="/path/to/repository"
            autoComplete="off"
            disabled={catalogOnly || Boolean(activeResult)}
          />
        </label>
        <div className="watcher-readonly">
          <span>Checklist</span>
          <code>{manifest.checklistPath ?? '.snapfall/milestone.json'}</code>
        </div>
        <label>
          <span>Quote</span>
          <div className="quote-input">
            <input
              inputMode="decimal"
              value={quoteUsdc}
              onChange={(event) => setQuoteUsdc(event.target.value)}
              aria-label="Milestone quote in USDC"
              disabled={catalogOnly || Boolean(activeResult)}
            />
            <b>USDC</b>
          </div>
        </label>
        <button
          className="watcher-activate"
          type="button"
          onClick={hire}
          disabled={catalogOnly || !valid || submitting || Boolean(activeResult)}
        >
          {catalogOnly
            ? 'Needs a connected daemon'
            : activeResult
              ? `✓ ${activationLabel(activeResult.state)}`
              : submitting
                ? 'Activating…'
                : 'Activate watcher'}
        </button>
        <div className="watcher-feedback" aria-live="polite">
          {error ? <p className="is-error">{error}</p> : null}
          {catalogOnly ? (
            // Same honesty as the missing operator-tool cards: a visitor clicking Activate and
            // getting a 502 reads as "broken", not "quiet". Say why it is disabled instead.
            <p>Activation needs a connected daemon. This public catalogue shows what Build Monitor is, not a running instance.</p>
          ) : activeResult ? (
            <p className="is-success">
              Build Monitor: {activationLabel(activeResult.state).toLowerCase()}. <code>{activeResult.jobId}</code>
            </p>
          ) : (
            <p>Activation opens milestone 1 and records the owner-confirmed assignment.</p>
          )}
        </div>
      </div>
    </article>
  );
}

/**
 * An operator tool: a real, registered worker with NO payment flow. It is not a job in the
 * money-spine sense — there is no customer, no quote, and no escrow, because it reads the
 * operator's own repo/configs/services and reports to the operator's own Brain. That is why it
 * has no hire form: Build Monitor is hireable because it is a job (a repository AND a quote,
 * through the settlement path); these have no payer. The label says "operator tool · no payment
 * flow" plainly so the absent hire button reads as correct, not unfinished — the same honesty
 * the "Coming soon" badge removal is about. Dispatch is real (TestRouter_DispatchesReleaseScribeByKind).
 */
function OperatorToolCard({ manifest, catalogOnly }: { manifest: WorkerManifest; catalogOnly: boolean }) {
  return (
    <article className="manifest-card">
      <div className="manifest-card-head">
        <div className="manifest-identity">
          <div>
            <h3>{manifest.name}</h3>
            <p>{manifest.category}</p>
          </div>
        </div>
        {/* The status describes the tool's nature (no payment flow), which is true with or without a
            daemon, so it stays. The NOTE below is where honesty about a running instance lives. */}
        <span className="manifest-status is-active"><i />Operator tool · no payment flow</span>
      </div>
      <p className="manifest-description">{manifest.description}</p>
      <div className="permission-row">
        {manifest.permissions.map((permission) => <PermissionChip key={permission} label={permission} />)}
      </div>
      {/* "Invoked by Brain" would imply a running Brain. On the public deploy there is no daemon, so
          say what is actually true there instead of implying something is executing. */}
      <p className="manifest-note">
        {catalogOnly
          ? 'Catalogue entry — registered with Brain when a daemon runs; nothing is executing on this page.'
          : 'No customer, quote, or escrow — invoked by Brain, reports evidence, authorizes nothing.'}
      </p>
    </article>
  );
}

export default function WorkforcePage() {
  const [manifests, setManifests] = useState<WorkerManifest[]>(CATALOG_MANIFESTS);
  const [activations, setActivations] = useState<WorkerActivation[]>([]);
  /**
   * True while nothing has been read from the daemon. The catch below deliberately keeps the
   * committed catalogue on screen, which is right -- a manifest is a repo artefact, not a live
   * reading -- but showing it under no label at all made a daemon-less deploy indistinguishable
   * from a connected one, and the Activate button below it will not work.
   */
  const [catalogOnly, setCatalogOnly] = useState(true);

  useEffect(() => {
    let active = true;
    fetch('/api/workforce', { cache: 'no-store' })
      .then(async (response) => {
        if (!response.ok) return;
        const body = (await response.json()) as {
          manifests?: WorkerManifest[];
          activations?: WorkerActivation[];
          source?: string;
        };
        if (!active) return;
        if (body.manifests?.length) setManifests(body.manifests);
        if (body.activations) setActivations(body.activations);
        // With no daemon configured the route still answers 200, carrying the committed catalogue
        // and source: 'local-catalog'. A 200 is therefore NOT evidence of a daemon, and treating
        // it as one printed "daemon connected" over a deployment that has none.
        setCatalogOnly(body.source === 'local-catalog');
      })
      .catch(() => {
        // The committed catalog remains visible; activation reports daemon availability.
      });
    return () => { active = false; };
  }, []);

  const buildMonitor = useMemo(
    () => manifests.find((manifest) => manifest.id === 'build-monitor') ?? BUILD_MONITOR_MANIFEST,
    [manifests],
  );
  const buildMonitorActivation = useMemo(
    () => activations.find((activation) => activation.manifestId === 'build-monitor') ?? null,
    [activations],
  );
  // Real manifests other than Build Monitor are operator tools: no customer, quote, or escrow, so
  // they render as no-payment-flow cards, not the money-spine hire form Build Monitor uses.
  const operatorTools = useMemo(
    () => manifests.filter((manifest) => manifest.id !== 'build-monitor'),
    [manifests],
  );

  return (
    <div className="workforce-page">
      <div className="page-header">
        <div className="page-header-text">
          <h1>Workforce</h1>
          <p className="page-header-sub">
            Deploy bounded specialists without expanding their authority.
          </p>
        </div>
        <span className="page-header-aside">
          {catalogOnly ? (
            <span className="status-badge Cancelled">committed catalogue</span>
          ) : (
            <span className="status-badge Funded">daemon connected</span>
          )}
        </span>
      </div>

      <ActiveTeam />

      <section className="manifest-gallery" aria-labelledby="manifest-gallery-title">
        <div className="workforce-panel-head">
          <div>
            <h2 id="manifest-gallery-title">Manifest gallery</h2>
            <p className="workforce-panel-sub">Hire from reviewed manifests. Permissions stay explicit.</p>
          </div>
          <span className="gallery-count">
            {manifests.length} available
            {COMING_SOON_WORKERS.length > 0 ? ` · ${COMING_SOON_WORKERS.length} upcoming` : ''}
          </span>
        </div>
        {/* Build Monitor is the one hireable job (it has a form); the three operator tools have no
            form. They no longer share a grid row — the form's height stretched the tools into 144px
            columns padded to match. Build Monitor keeps its full-width block; the tools get their
            own content-height row below. */}
        <BuildMonitorCard manifest={buildMonitor} activation={buildMonitorActivation} catalogOnly={catalogOnly} />
        {operatorTools.length > 0 ? (
          <div className="operator-tools">
            {operatorTools.map((manifest) => (
              <OperatorToolCard key={manifest.id} manifest={manifest} catalogOnly={catalogOnly} />
            ))}
          </div>
        ) : null}
      </section>
    </div>
  );
}
