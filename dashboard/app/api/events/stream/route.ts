/**
 * SSE stream the Overview subscribes to (FR-UI-006: UI updates within 2s of an event).
 *
 * Three modes, in priority order:
 *   1. A real daemon is wired (SNAPFALL_OWNER_API_URL set) → proxy its event bus verbatim.
 *   2. No daemon, and the local demo replay is explicitly enabled (SNAPFALL_DEMO_STREAM=1)
 *      → replay the scripted demo spine. Every frame is tagged `demo: true`; the module that
 *      holds the fabricated fixtures is dynamically imported ONLY here, so a production build
 *      (flag off) tree-shakes it and lib/mockData out of the bundle entirely.
 *   3. No daemon, demo off (the public-deployment default) → emit a single honest snapshot
 *      marked `daemonConnected: false` and close the response. The Overview renders a
 *      plain "no daemon connected" banner. A public visitor never sees fabricated activity.
 *
 * The real feed carries owner data, so mode 1 runs the owner guard (lib/ownerApi
 * requireOwnerRequest) before the owner token is attached: an unauthenticated visitor gets 401,
 * which EventSource surfaces as an error and lib/useEventStream turns into a sign-in prompt.
 * The demo mode serves scripted fixtures only and binds to the local dev app.
 *
 * The guard runs once per connection, so a proxied stream is bounded by the session that opened
 * it (lib/ownerStream): it ends cleanly when that session expires, or after
 * OWNER_STREAM_REVERIFY_MS, whichever is first, and the browser's reconnect must pass the guard
 * again. A stream never outlives its session, and a sign-out stops it within that interval.
 *
 * A re-verify close is announced at the start of the stream (OWNER_WINDOW_EVENT), so the client can
 * open its replacement before this one ends and lose no chain event across the handover; the
 * replacement's daemon resume point arrives as a query parameter (lib/ownerStream).
 */

import { ownerApiBase, ownerAuthHeaders, requireOwnerRequest } from '@/lib/ownerApi';
import {
  ownerStreamCloseAt,
  ownerStreamHandoverIn,
  ownerStreamResumeId,
  ownerWindowFrame,
  streamUntil,
} from '@/lib/ownerStream';
import type { OverviewSnapshot } from '@/lib/types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

// Read once at module load. In a production build with the flag unset this is `false`, and
// the `import('@/lib/demoStream')` below sits in a dead branch that webpack eliminates.
const DEMO_ENABLED = process.env.SNAPFALL_DEMO_STREAM === '1';

const SSE_HEADERS = {
  'content-type': 'text/event-stream; charset=utf-8',
  'cache-control': 'no-cache, no-transform',
  connection: 'keep-alive',
} as const;

/** The honest empty snapshot for a deployment with no daemon. No fabricated numbers, no
 *  activity, no explorer links — just an explicit "not connected" so the UI can say so. */
const DISCONNECTED_SNAPSHOT: OverviewSnapshot = {
  treasuryUsdc: null,
  pool: null,
  activeJobs: [],
  pendingApprovals: 0,
  workforce: [],
  openAdvances: [],
  recentEvents: [],
  daemonConnected: false,
};

async function daemonStream(req: Request): Promise<Response | null> {
  if (!ownerApiBase()) return null;
  const gate = requireOwnerRequest(req);
  if (!gate.ok) return gate.response;
  const now = Date.now();

  const headers = ownerAuthHeaders(gate.grant, { accept: 'text/event-stream' });
  const lastEventId = ownerStreamResumeId(req);
  if (lastEventId) headers.set('last-event-id', lastEventId);

  // Armed before the upstream fetch, so a daemon slow to answer cannot stretch the window either.
  const stop = new AbortController();
  const timer = setTimeout(
    () => stop.abort(new Error('owner session window ended')),
    Math.max(0, ownerStreamCloseAt(gate.grant.sessionExpiresAt, now) - now),
  );
  const release = () => clearTimeout(timer);

  try {
    const upstream = await fetch(`${gate.grant.base}/events/stream`, {
      headers,
      cache: 'no-store',
      signal: AbortSignal.any([req.signal, stop.signal]),
    });
    if (!upstream.ok || !upstream.body) {
      release();
      await upstream.body?.cancel().catch(() => {});
      return new Response('Owner API event stream unavailable', { status: 502 });
    }
    const handoverIn = ownerStreamHandoverIn(gate.grant.sessionExpiresAt, now);
    const preamble =
      handoverIn === null ? undefined : new TextEncoder().encode(ownerWindowFrame(handoverIn - (Date.now() - now)));
    return new Response(
      streamUntil(
        upstream.body,
        stop.signal,
        () => {
          release();
          stop.abort(); // end the upstream connection with the downstream one
        },
        preamble,
      ),
      {
        status: upstream.status,
        headers: {
          ...SSE_HEADERS,
          'x-accel-buffering': 'no',
        },
      },
    );
  } catch {
    release();
    if (req.signal.aborted) return new Response(null, { status: 499 });
    // The window closed before the daemon answered: end an empty stream (not a 204, which would
    // stop EventSource for good), so the browser reconnects through the guard.
    if (stop.signal.aborted) return new Response('', { headers: SSE_HEADERS });
    return new Response('Owner API event stream unavailable', { status: 502 });
  }
}

/** Mode 3: finish immediately so serverless hosts do not bill an idle heartbeat loop.
 * EventSource observes retry and reconnects once a minute to discover daemon configuration. */
function disconnectedStream(): Response {
  return new Response(
    `retry: 60000\ndata: ${JSON.stringify({ kind: 'snapshot', snapshot: DISCONNECTED_SNAPSHOT })}\n\n`,
    { headers: SSE_HEADERS },
  );
}

export async function GET(req: Request): Promise<Response> {
  const upstream = await daemonStream(req);
  if (upstream) return upstream;

  if (DEMO_ENABLED) {
    const { demoReplayStream } = await import('@/lib/demoStream');
    return new Response(demoReplayStream(req), { headers: SSE_HEADERS });
  }

  return disconnectedStream();
}
