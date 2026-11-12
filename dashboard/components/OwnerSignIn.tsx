'use client';

/**
 * The owner sign-in prompt, shown wherever an owner route answered "sign in first".
 *
 * The secret is posted once to /api/owner/session; what comes back is an HttpOnly session cookie,
 * so nothing here stores it. If this deployment has a daemon but no dashboard secret, the owner
 * routes refuse outright, and this says which variable to set instead of offering a form that
 * cannot succeed.
 */

import { useEffect, useState, type FormEvent } from 'react';
import { announceOwnerSessionChange, readOwnerSession, type OwnerSessionStatus } from '@/lib/ownerSessionClient';

export default function OwnerSignIn({ onSignedIn, className = '' }: { onSignedIn?: () => void; className?: string }) {
  const [status, setStatus] = useState<OwnerSessionStatus | null>(null);
  const [secret, setSecret] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    void readOwnerSession().then((s) => {
      if (alive) setStatus(s);
    });
    return () => {
      alive = false;
    };
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || !secret) return;
    setBusy(true);
    setError(null);
    try {
      const res = await fetch('/api/owner/session', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ secret }),
      });
      if (res.ok) {
        setSecret('');
        announceOwnerSessionChange();
        onSignedIn?.();
        return;
      }
      const body = (await res.json().catch(() => ({}))) as { error?: { message?: string } };
      setError(body.error?.message ?? `Sign-in failed (${res.status}).`);
    } catch {
      setError('The dashboard could not reach its sign-in route.');
    } finally {
      setBusy(false);
    }
  }

  if (status && !status.accessConfigured) {
    return (
      <section className={`card owner-signin ${className}`} aria-labelledby="owner-signin-title">
        <h2 className="card-title" id="owner-signin-title">
          Owner access is not configured
        </h2>
        <p className="owner-signin-body">
          This dashboard is connected to a daemon but has no owner secret, so every owner control
          refuses. Set <code>SNAPFALL_DASHBOARD_OWNER_SECRET</code> (at least 32 characters,
          different from the daemon token) on the dashboard and restart it.
        </p>
      </section>
    );
  }

  return (
    <section className={`card owner-signin ${className}`} aria-labelledby="owner-signin-title">
      <h2 className="card-title" id="owner-signin-title">
        Owner sign-in
      </h2>
      <p className="owner-signin-body">
        Approvals, customer links, hiring and the live event stream act with the owner’s authority,
        so they need the dashboard owner secret. It is checked once and exchanged for a session
        cookie this page cannot read.
      </p>
      <form className="owner-signin-form" onSubmit={(e) => void submit(e)}>
        <label className="owner-signin-label" htmlFor="owner-signin-secret">
          Owner secret
        </label>
        <div className="owner-signin-row">
          <input
            id="owner-signin-secret"
            className="owner-signin-input"
            type="password"
            autoComplete="current-password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            disabled={busy}
            required
          />
          <button type="submit" className="owner-signin-submit" disabled={busy || !secret} aria-busy={busy || undefined}>
            {busy ? 'Checking…' : 'Sign in'}
          </button>
        </div>
      </form>
      <div aria-live="polite" role="status">
        {error ? <p className="owner-signin-error">{error}</p> : null}
      </div>
    </section>
  );
}

/** Sign-out control for the Settings page: shows the session state and clears the cookie. */
export function OwnerSessionPanel() {
  const [status, setStatus] = useState<OwnerSessionStatus | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = () => void readOwnerSession().then(setStatus);
  useEffect(refresh, []);

  async function signOut() {
    setBusy(true);
    try {
      await fetch('/api/owner/session', { method: 'DELETE', headers: { 'content-type': 'application/json' } });
      announceOwnerSessionChange();
    } finally {
      setBusy(false);
      refresh();
    }
  }

  if (!status) return <p className="settings-note">Reading the owner session…</p>;
  if (status.signedIn) {
    return (
      <div className="owner-signin-row">
        <p className="settings-body">Signed in as the owner in this browser.</p>
        <button type="button" className="owner-signin-submit" onClick={() => void signOut()} disabled={busy}>
          {busy ? 'Signing out…' : 'Sign out'}
        </button>
      </div>
    );
  }
  if (!status.ownerApi && !status.accessConfigured) {
    return <p className="settings-body">No daemon and no owner secret are configured, so there are no owner controls to sign in to.</p>;
  }
  return <OwnerSignIn onSignedIn={refresh} />;
}
