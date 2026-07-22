/**
 * SSE stream the Overview subscribes to (FR-UI-006: UI updates within 2s of an event).
 *
 * Emits a snapshot, then replays the demo timeline on a ticker and loops. This is the mock
 * behind the H2 `/api/v1/events/stream` shape — swapped for the daemon's real event bus later.
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
      let timer: ReturnType<typeof setTimeout>;

      const send = (obj: unknown) => {
        if (closed) return;
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(obj)}\n\n`));
      };

      const stop = () => {
        if (closed) return;
        closed = true;
        clearTimeout(timer);
        try {
          controller.close();
        } catch {
          /* already closed */
        }
      };

      send({ kind: 'snapshot', snapshot });

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
        });
        i += 1;
        timer = setTimeout(tick, 2500);
      };

      timer = setTimeout(tick, 1200);
      req.signal.addEventListener('abort', stop);
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
