/**
 * SSE stream the Overview subscribes to (FR-UI-006: UI updates within 2s of an event).
 *
 * Emits a snapshot, then replays the demo timeline on a ticker and loops. This is the mock
 * behind the H2 `/api/v1/events/stream` shape - swapped for the daemon's real event bus later.
 * The real feed carries owner data and MUST enforce owner auth; this mock serves scripted
 * demo fixtures only and binds to the local dev app.
 *
 * Review notes applied (PR #8): snapshot event timestamps are stamped per connection so the
 * feed never opens showing hours-old entries, and an already-aborted request is handled
 * before any timer is scheduled so disconnected clients cannot leak the replay loop.
 */

import { snapshot, timeline } from '@/lib/mockData';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

export function GET(req: Request): Response {
  const encoder = new TextEncoder();

  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      let closed = false;
      let seq = 100;
      let i = 0;
      let timer: ReturnType<typeof setTimeout> | undefined;

      const send = (obj: unknown) => {
        if (closed) return;
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(obj)}\n\n`));
      };

      const stop = () => {
        if (closed) return;
        closed = true;
        if (timer) clearTimeout(timer);
        try {
          controller.close();
        } catch {
          /* already closed */
        }
      };

      // A request can arrive already aborted (client gone before start ran); bail out
      // before scheduling anything so no timer or controller is retained.
      if (req.signal.aborted) {
        stop();
        return;
      }
      req.signal.addEventListener('abort', stop);

      // Stamp the snapshot's seed events with fresh timestamps for THIS connection.
      const now = new Date().toISOString();
      send({
        kind: 'snapshot',
        snapshot: { ...snapshot, recentEvents: snapshot.recentEvents.map((e) => ({ ...e, ts: now })) },
      });

      const tick = () => {
        if (closed) return;
        const step = timeline[i % timeline.length]!;
        seq += 1;
        send({
          kind: 'event',
          event: { ...step.event, seq, ts: new Date().toISOString() },
          treasuryUsdc: step.treasuryUsdc,
          pool: step.pool,
          openAdvances: step.openAdvances,
          ...(step.activeJobs ? { activeJobs: step.activeJobs } : {}),
          ...(step.pendingApprovals !== undefined ? { pendingApprovals: step.pendingApprovals } : {}),
        });
        i += 1;
        timer = setTimeout(tick, 2500);
      };

      timer = setTimeout(tick, 1200);
    },
  });

  return new Response(stream, {
    headers: {
      'content-type': 'text/event-stream; charset=utf-8',
      'cache-control': 'no-cache, no-transform',
      connection: 'keep-alive',
    },
  });
}
