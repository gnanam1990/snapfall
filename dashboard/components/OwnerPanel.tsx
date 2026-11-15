'use client';

import Link from 'next/link';
import type React from 'react';
import { useCallback, useEffect, useState, type ReactNode } from 'react';
import type { AgentCard, StreamMessage } from '@/lib/types';
import { useEventStream } from '@/lib/useEventStream';
import { ownerDataHidden } from '@/lib/streamClient';
import OwnerSignIn from '@/components/OwnerSignIn';

/**
 * The owner's agents: approvals waiting and the workforce, from the daemon stream.
 *
 * Owner data is shown only while the stream is live for the current session (ownerDataHidden):
 * signed out or unverified, nothing delivered under an earlier session is rendered. With no daemon
 * configured the section says so plainly instead of showing sample agents.
 */
export default function OwnerPanel({ className = '', style, compact = false }: { className?: string; style?: React.CSSProperties; compact?: boolean }) {
  const [connected, setConnected] = useState<boolean | null>(null);
  const [pending, setPending] = useState<number | null>(null);
  const [agents, setAgents] = useState<AgentCard[]>([]);

  const onMessage = useCallback((msg: StreamMessage) => {
    if (msg.kind === 'snapshot') {
      setConnected(msg.snapshot.daemonConnected !== false);
      setPending(msg.snapshot.pendingApprovals ?? 0);
      setAgents(msg.snapshot.workforce ?? []);
    } else if (msg.aggregates?.pendingApprovals !== undefined) {
      setPending(msg.aggregates.pendingApprovals);
    }
  }, []);

  const status = useEventStream('/api/events/stream', onMessage);
  const hidden = ownerDataHidden(status);

  useEffect(() => {
    if (status !== 'signed-out') return;
    setPending(null);
    setAgents([]);
  }, [status]);

  const card = (sub: string, body?: ReactNode) => (
    <section className={`card-x ${className}`.trim()} style={style} aria-labelledby="agents-h">
      <div className="card-x-head">
        <div>
          <h2 id="agents-h">Your agents</h2>
          <p>{sub}</p>
        </div>
      </div>
      {body ? <div className="card-x-body">{body}</div> : <div className="card-x-body" />}
    </section>
  );

  if (status === 'signed-out') {
    // In a small tile the full form would set the row's height; link to a page that has room for it.
    return compact
      ? card('Owner sign-in needed.', <><p style={{ margin: 0, color: 'var(--ink-2)', fontSize: 14 }}>Purchases waiting for you and what each agent is doing.</p><Link className="btn-solid" style={{ marginTop: 'auto', alignSelf: 'flex-start' }} href="/approvals">Sign in</Link></>)
      : card('Sign in as the owner to see purchases waiting for you and what each agent is doing.', <OwnerSignIn />);
  }

  if (connected === false) {
    return card(
      'Approvals and agent activity come from your Snapfall daemon.',
      <p style={{ margin: 0, color: 'var(--ink-2)', fontSize: 14 }}>
        No daemon is connected to this dashboard. Everything else on this page is read from the chain and needs none.{' '}
        <Link className="link" href="/settings">How to connect one</Link>
      </p>,
    );
  }

  if (hidden || connected === null) {
    return card(status === 'unavailable' ? 'Your daemon is not answering. Retrying.' : 'Connecting to your daemon…');
  }

  const waiting = pending ?? 0;
  return card(
    'Live from your daemon.',
    <>
      <div className={waiting ? 'notice' : undefined} style={waiting ? undefined : { color: 'var(--ink-2)', fontSize: 14 }}>
        <p style={{ margin: 0 }}>
          {waiting
            ? `${waiting} purchase${waiting === 1 ? ' is' : 's are'} waiting for your decision.`
            : 'Nothing is waiting for you. Agents bring any spend above their limit here.'}
        </p>
        {waiting ? <Link className="btn btn-accent" href="/approvals">Review purchases</Link> : null}
      </div>
      {agents.length ? (
        <ul style={{ listStyle: 'none', padding: 0, margin: '18px 0 0', display: 'flex', flexDirection: 'column', gap: 10 }}>
          {agents.map((a) => (
            <li key={a.id} className={`state ${a.status === 'working' ? 'is-live' : a.status === 'approval-required' || a.status === 'waiting' ? 'is-wait' : a.status === 'failed' || a.status === 'frozen' ? 'is-bad' : ''}`}>
              {a.role}, {a.status.replace('-', ' ')}
            </li>
          ))}
        </ul>
      ) : null}
      <p className="card-x-foot"><Link className="link" href="/workforce">Manage the workforce</Link></p>
    </>,
  );
}
