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
 * The real feed carries owner data and MUST enforce owner auth; the demo mode serves scripted
 * fixtures only and binds to the local dev app.
 */

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
  const base = process.env.SNAPFALL_OWNER_API_URL?.replace(/\/$/, '');
  if (!base) return null;

  const headers = new Headers({ accept: 'text/event-stream' });
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) headers.set('authorization', `Bearer ${token}`);
  const lastEventId = req.headers.get('last-event-id');
  if (lastEventId) headers.set('last-event-id', lastEventId);

  try {
    const upstream = await fetch(`${base}/events/stream`, {
      headers,
      cache: 'no-store',
      signal: req.signal,
    });
    if (!upstream.ok || !upstream.body) {
      return new Response('Owner API event stream unavailable', { status: 502 });
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: {
        ...SSE_HEADERS,
        'x-accel-buffering': 'no',
      },
    });
  } catch {
    if (req.signal.aborted) return new Response(null, { status: 499 });
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
