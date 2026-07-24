'use client';

import { useEffect, useMemo, useState } from 'react';

import {
  BUILD_MONITOR_MANIFEST,
  COMING_SOON_WORKERS,
  activationLabel,
  validHireInput,
  type HireWorkerResult,
  type WorkerActivation,
  type WorkerManifest,
} from '@/lib/workforce';

const ACTIVE_TEAM = [
  { role: 'Brain', detail: 'Routes only', glyph: '◎', tone: 'teal' },
  { role: 'Research', detail: 'Read-only', glyph: '⌕', tone: 'sky' },
  { role: 'Delivery', detail: 'Can submit', glyph: '↗', tone: 'teal' },
  { role: 'QA', detail: 'Can verify', glyph: '◇', tone: 'sky' },
  { role: 'Funding', detail: 'Policy-gated', glyph: '▱', tone: 'teal' },
] as const;

function PermissionChip({ label }: { label: string }) {
  const symbol = label === 'Read-only repo' ? '▣' : label === 'No payments' ? '⊘' : '›_';
  return <span className="permission-chip"><i aria-hidden="true">{symbol}</i>{label}</span>;
}

function ActiveTeam() {
  return (
    <section className="workforce-active" aria-labelledby="active-team-title">
      <div className="workforce-panel-head">
        <div>
          <p className="workforce-eyebrow">Current workforce</p>
          <h2 id="active-team-title">Active team</h2>
        </div>
        <span className="workforce-safe"><i />5 bounded roles</span>
      </div>
      <div className="active-team-line">
        {ACTIVE_TEAM.map((agent, index) => (
          <div className="active-team-step" key={agent.role}>
            <article className="active-agent">
              <span className={`active-agent-icon ${agent.tone}`} aria-hidden="true">{agent.glyph}</span>
              <div>
                <strong>{agent.role}</strong>
                <small><i />{agent.detail}</small>
              </div>
            </article>
            {index < ACTIVE_TEAM.length - 1 ? <span className="team-connector" aria-hidden="true" /> : null}
          </div>
        ))}
      </div>
    </section>
  );
}

function BuildMonitorCard({ manifest, activation }: { manifest: WorkerManifest; activation: WorkerActivation | null }) {
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
    if (!valid || submitting) return;
    setSubmitting(true);
    setError('');
    try {
      const response = await fetch(`/api/workforce/${encodeURIComponent(manifest.id)}/hire`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ repository: repository.trim(), quoteUsdc: quoteUsdc.trim(), by: 'anandan' }),
      });
      const body = await response.json() as HireWorkerResult & { error?: { message?: string } };
      if (!response.ok) throw new Error(body.error?.message ?? 'Build Monitor could not be activated.');
      setResult(body);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Build Monitor could not be activated.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <article className="manifest-card manifest-featured">
      <div className="manifest-card-head">
        <div className="manifest-identity">
          <span className="manifest-icon is-featured" aria-hidden="true">⌘</span>
          <div>
            <h3>{manifest.name}</h3>
            <p>{manifest.category}</p>
          </div>
        </div>
        <span className={`manifest-status${activeResult ? ' is-active' : ''}`}>
          <i />{activeResult ? activationLabel(activeResult.state) : 'Ready to hire'}
        </span>
      </div>

      <p className="manifest-description">{manifest.description}</p>
      <div className="permission-row">
        {manifest.permissions.map((permission) => <PermissionChip key={permission} label={permission} />)}
      </div>

      <div className="watcher-flow" aria-label="Repository evidence flow">
        <div><span aria-hidden="true">⑂</span><small>Repository</small></div>
        <i aria-hidden="true">→</i>
        <div><span aria-hidden="true">◎</span><small>Brain</small></div>
        <i aria-hidden="true">→</i>
        <div><span aria-hidden="true">▤</span><small>Milestone evidence</small></div>
      </div>

      <div className="watcher-config">
        <label>
          <span>Repository path</span>
          <input
            value={repository}
            onChange={(event) => setRepository(event.target.value)}
            placeholder="/path/to/repository"
            autoComplete="off"
            disabled={Boolean(activeResult)}
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
              disabled={Boolean(activeResult)}
            />
            <b>USDC</b>
          </div>
        </label>
        <button className="watcher-activate" type="button" onClick={hire} disabled={!valid || submitting || Boolean(activeResult)}>
          {activeResult ? `✓ ${activationLabel(activeResult.state)}` : submitting ? 'Activating…' : 'Activate watcher'}
        </button>
        <div className="watcher-feedback" aria-live="polite">
          {error ? <p className="is-error">{error}</p> : null}
          {activeResult ? (
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

function ComingSoonCard({ worker, index }: { worker: (typeof COMING_SOON_WORKERS)[number]; index: number }) {
  const glyphs = ['✎', '⌕', '◉'];
  return (
    <article className="manifest-card manifest-soon">
      <span className="manifest-icon" aria-hidden="true">{glyphs[index]}</span>
      <h3>{worker.name}</h3>
      <p className="manifest-category">{worker.category}</p>
      <span className="coming-soon"><i />Coming soon</span>
      <p className="manifest-description">{worker.description}</p>
      <div className="soon-divider" />
      <div className="permission-row">
        <PermissionChip label="Read-only repo" />
        <PermissionChip label="No payments" />
        <PermissionChip label="No shell" />
      </div>
    </article>
  );
}

export default function WorkforcePage() {
  const [manifests, setManifests] = useState<WorkerManifest[]>([BUILD_MONITOR_MANIFEST]);
  const [activations, setActivations] = useState<WorkerActivation[]>([]);

  useEffect(() => {
    let active = true;
    fetch('/api/workforce', { cache: 'no-store' })
      .then(async (response) => {
        if (!response.ok) return;
        const body = await response.json() as { manifests?: WorkerManifest[]; activations?: WorkerActivation[] };
        if (active && body.manifests?.length) setManifests(body.manifests);
        if (active && body.activations) setActivations(body.activations);
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

  return (
    <div className="workforce-page">
      <div className="topbar workforce-topbar">
        <div>
          <h1 className="page-title">Workforce</h1>
          <p className="page-sub">Deploy bounded specialists without expanding their authority.</p>
        </div>
        <span className="workforce-policy">Capabilities stay explicit</span>
      </div>

      <ActiveTeam />

      <section className="manifest-gallery" aria-labelledby="manifest-gallery-title">
        <div className="workforce-panel-head manifest-gallery-head">
          <div>
            <p className="workforce-eyebrow">Manifest gallery</p>
            <h2 id="manifest-gallery-title">Grow your team</h2>
            <p>Hire from reviewed manifests. Permissions stay explicit.</p>
          </div>
          <span className="gallery-count">1 available · 3 upcoming</span>
        </div>
        <div className="manifest-grid">
          <BuildMonitorCard manifest={buildMonitor} activation={buildMonitorActivation} />
          {COMING_SOON_WORKERS.map((worker, index) => (
            <ComingSoonCard key={worker.id} worker={worker} index={index} />
          ))}
        </div>
      </section>
    </div>
  );
}
