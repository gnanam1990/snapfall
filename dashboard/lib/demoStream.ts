/**
 * Local scripted demo replay for the Overview SSE stream.
 *
 * This is the ONLY module that pulls in `lib/mockData` (the fabricated demo spine). It is
 * dynamically imported by `app/api/events/stream/route.ts` and ONLY when
 * `SNAPFALL_DEMO_STREAM === '1'`. When the flag is off the import branch is dead, so a
 * production build tree-shakes this file and mockData out entirely — a public deployment
 * ships no fabricated content, reachable or not.
 *
 * Every message it emits is tagged `demo: true` so the client marks its contents —
 * explorer links above all — as placeholders, never as real chain proof.
 */

import { snapshot, timeline } from '@/lib/mockData';
import type { FinancialEvent, StreamEvent } from '@/lib/types';

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
    case 'job.draft.created':
      // Preserved rather than falling through to the default rewrite below. The default turns
      // every unrecognized kind into `brain.msg.brain.job_update`, which silently made the money
      // graph's 'reset' beat dead code: the demo loop's closing "waiting for the next job" state
      // could never be reached, and the graph relied on the NEXT funding event to clear itself.
      return {
        source: 'daemon',
        event: { ...daemonBase, kind: 'job.draft.created', actor: 'brain', payload: {} },
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

/** Build the scripted replay stream. Emits a demo-tagged snapshot, then loops the demo
 *  timeline on a ticker. Identical behaviour to the original inline mock, minus nothing. */
export function demoReplayStream(req: Request): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();

  return new ReadableStream<Uint8Array>({
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
        demo: true,
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
          demo: true,
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
}
