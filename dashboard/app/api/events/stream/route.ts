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
import type { FinancialEvent, StreamEvent } from '@/lib/types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const REQUEST_ID = 'apr_demo_premium';
const INTENT_HASH = `0x${'ab'.repeat(32)}`;

function h2Event(event: FinancialEvent): { source: 'daemon' | 'chain'; event: StreamEvent } {
  const daemonBase = { jobId: event.jobId, at: event.ts };
  const chainBase = { entityId: event.jobId, at: event.ts };
  const amount = event.amountUsdc;
  switch (event.type) {
    case 'job.funded':
      return {
        source: 'chain',
        event: {
          ...chainBase,
          kind: 'JobFunded',
          actor: 'funding',
          payload: { amountAtomic: amount, explorerUrl: event.explorerUrl },
        },
      };
    case 'advance.issued':
      return {
        source: 'chain',
        event: {
          ...chainBase,
          kind: 'AdvanceIssued',
          actor: 'funding',
          payload: {
            org: '0x0000000000000000000000000000000000000000',
            principalAtomic: amount,
            feeAtomic: '250000',
            rateBps: 5000,
            explorerUrl: event.explorerUrl,
          },
        },
      };
    case 'payment.delivered':
      return {
        source: 'daemon',
        event: { ...daemonBase, kind: 'payment.executed', actor: 'approval', payload: { amountUsdc: amount } },
      };
    case 'approval.requested':
      return {
        source: 'daemon',
        event: {
          ...daemonBase,
          kind: 'approval.requested',
          actor: 'approval',
          payload: {
            request_id: REQUEST_ID,
            intent_hash: INTENT_HASH,
            state: 'PENDING',
            intent: {
              Merchant: 'api.research-data.example',
              Resource: 'GET /v1/premium-dataset',
              AmountMicros: 4_000_000,
              Purpose: 'premium market dataset',
              AlternativeTo: '',
            },
          },
        },
      };
    case 'approval.request_alternative':
      return {
        source: 'daemon',
        event: {
          ...daemonBase,
          kind: 'approval.request_alternative',
          actor: 'approval',
          payload: { request_id: REQUEST_ID, by: 'anandan', reason: 'Too expensive — find a cheaper source.' },
        },
      };
    case 'approval.alternative_found':
      return {
        source: 'daemon',
        event: {
          ...daemonBase,
          kind: 'approval.requested',
          actor: 'worker:due-diligence',
          payload: {
            request_id: 'apr_demo_benchmark',
            intent_hash: `0x${'cd'.repeat(32)}`,
            state: 'APPROVED',
            intent: {
              Merchant: 'api.research-data.example',
              Resource: 'GET /v1/benchmark',
              AmountMicros: 60_000,
              Purpose: 'benchmark summary',
              AlternativeTo: REQUEST_ID,
            },
          },
        },
      };
    case 'job.accepted':
      return {
        source: 'chain',
        event: {
          ...chainBase,
          kind: 'JobSettled',
          actor: 'funding',
          payload: {
            advanceRepaidAtomic: amount,
            operatorNetAtomic: '12250000',
            explorerUrl: event.explorerUrl,
          },
        },
      };
    case 'rate.updated':
      return {
        source: 'chain',
        event: {
          entityId: '0x0000000000000000000000000000000000000000',
          at: event.ts,
          kind: 'RateChanged',
          actor: 'funding',
          payload: { org: '0x0000000000000000000000000000000000000000', rateBps: 5500 },
        },
      };
    default:
      return {
        source: 'daemon',
        event: {
          ...daemonBase,
          kind: 'brain.msg.brain.job_update',
          actor: 'brain',
          payload: { payload: { message: event.summary } },
        },
      };
  }
}

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
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache, no-transform',
        connection: 'keep-alive',
        'x-accel-buffering': 'no',
      },
    });
  } catch {
    if (req.signal.aborted) return new Response(null, { status: 499 });
    return new Response('Owner API event stream unavailable', { status: 502 });
  }
}

export async function GET(req: Request): Promise<Response> {
  const upstream = await daemonStream(req);
  if (upstream) return upstream;

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
        snapshot: { ...snapshot, recentEvents: (snapshot.recentEvents ?? []).map((e) => ({ ...e, ts: now })) },
      });

      const tick = () => {
        if (closed) return;
        const step = timeline[i % timeline.length]!;
        seq += 1;
        const stamped = { ...step.event, seq, ts: new Date().toISOString() };
        const wire = h2Event(stamped);
        send({
          kind: 'event',
          source: wire.source,
          seq,
          event: wire.event,
          aggregates: {
            treasuryUsdc: step.treasuryUsdc,
            pool: step.pool,
            openAdvances: step.openAdvances,
            ...(step.activeJobs ? { activeJobs: step.activeJobs } : {}),
            ...(step.pendingApprovals !== undefined ? { pendingApprovals: step.pendingApprovals } : {}),
          },
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
