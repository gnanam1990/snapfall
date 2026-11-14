'use client';

/**
 * What a page shows in place of its owner data when the event stream is 'unavailable': no
 * connection has opened for a while, so the page cannot tell whether this visitor is the owner.
 *
 * Without this the page said "checking owner session…" for as long as the tab stayed open, and a
 * visitor who was in fact signed out never saw the sign-in prompt, which only a stream 401 brought
 * up. The owner data stays hidden (nothing has shown this visitor is the owner); the prompt is
 * offered because signing in is the one fix this page can offer, and the note says that a signed-in
 * owner is waiting on the stream, not on themselves.
 */

import OwnerSignIn from '@/components/OwnerSignIn';

export default function StreamUnavailable({ what, className = '' }: { what: string; className?: string }) {
  return (
    <>
      <div className={`card ${className}`} role="status">
        <p className="card-title">Live stream unavailable</p>
        <p className="stat-sub">
          The dashboard has not been able to open its live event stream, so it cannot confirm the
          owner session and keeps {what} hidden. It keeps retrying. If you are signed out, sign in
          below; if you are signed in, the stream route or the daemon behind it needs attention.
        </p>
      </div>
      <OwnerSignIn className="mt" />
    </>
  );
}
