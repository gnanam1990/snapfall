import assert from 'node:assert/strict';
import test from 'node:test';
import { humanizeStreamEvent } from './activity';
import type { StreamMessage } from './types';

function frame(
  seq: number,
  kind: string,
  payload: unknown,
  actor = 'approval',
): Extract<StreamMessage, { kind: 'event' }> {
  return {
    kind: 'event',
    source: 'daemon',
    seq,
    event: { kind, jobId: 'job_demo_1', actor, at: '2026-07-24T10:00:00Z', payload },
  };
}

test('turns a pending H2 approval into a Brain request with an inbox handoff', () => {
  const message = humanizeStreamEvent(
    frame(1, 'approval.requested', {
      request_id: 'apr_original',
      intent_hash: '0xabc',
      state: 'PENDING',
      intent: {
        Merchant: 'api.example',
        Resource: 'GET /premium',
        AmountMicros: 4_000_000,
        Purpose: 'premium dataset',
        AlternativeTo: '',
      },
    }),
  );

  assert.equal(message.actor, 'Brain');
  assert.equal(message.filter, 'approvals');
  assert.equal(message.amountUsdc, '4000000');
  assert.deepEqual(message.approval, { requestId: 'apr_original', intentHash: '0xabc' });
  assert.match(message.text, /approval.*premium dataset.*4\.00 USDC/i);
});

test('preserves the backend causal link when a worker finds a replacement', () => {
  const message = humanizeStreamEvent(
    frame(
      2,
      'approval.requested',
      {
        request_id: 'apr_replacement',
        intent_hash: '0xdef',
        state: 'APPROVED',
        intent: {
          Merchant: 'api.example',
          Resource: 'GET /benchmark',
          AmountMicros: 60_000,
          Purpose: 'benchmark summary',
          AlternativeTo: 'apr_original',
        },
      },
      'worker:due-diligence',
    ),
  );

  assert.equal(message.actor, 'Due Diligence Worker');
  assert.equal(message.threadKey, 'apr_original');
  assert.equal(message.approval, undefined);
  assert.match(message.text, /replacement.*0\.06 USDC/i);
});

test('renders request-alternative as an owner conversation beat', () => {
  const message = humanizeStreamEvent(
    frame(3, 'approval.request_alternative', {
      request_id: 'apr_original',
      by: 'anandan',
      reason: 'too expensive',
    }),
  );

  assert.equal(message.actor, 'Owner');
  assert.equal(message.threadKey, 'apr_original');
  assert.match(message.text, /cheaper alternative.*too expensive/i);
});

test('reads a QA verdict from the nested Brain envelope', () => {
  const message = humanizeStreamEvent(
    frame(
      4,
      'brain.msg.worker.qa_verdict',
      { payload: { passed: false, reasons: ['unsupported claim'] } },
      'worker:qa',
    ),
  );

  assert.equal(message.actor, 'QA Worker');
  assert.equal(message.filter, 'work');
  assert.match(message.text, /unsupported claim/i);
});

test('keeps unknown event kinds readable instead of exposing raw payloads', () => {
  const message = humanizeStreamEvent(frame(5, 'future.event_kind', { secret: '<script>' }, 'brain'));

  assert.equal(message.text, 'Recorded future event kind.');
  assert.doesNotMatch(message.text, /script/i);
});

test('carries both waterfall legs from a chain settlement so the money graph need not infer them', () => {
  const message = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 42,
    event: {
      kind: 'JobSettled',
      jobId: 'job_demo_1',
      actor: 'funding',
      at: '2026-07-24T10:00:00Z',
      payload: { advanceRepaidAtomic: '12750000', operatorNetAtomic: '12250000' },
    },
  });

  assert.deepEqual(message.settlement, {
    advanceRepaidUsdc: '12750000',
    operatorNetUsdc: '12250000',
  });
  // The repaid leg doubles as the event's headline amount.
  assert.equal(message.amountUsdc, '12750000');
});

test('accepts the snake_case settlement spelling and omits the split when neither leg is present', () => {
  const snake = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 43,
    event: {
      kind: 'JobSettled',
      jobId: 'job_demo_1',
      at: '2026-07-24T10:00:00Z',
      payload: { advance_repaid_atomic: '900', operator_net_atomic: '100' },
    },
  });
  assert.deepEqual(snake.settlement, { advanceRepaidUsdc: '900', operatorNetUsdc: '100' });

  const bare = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 44,
    event: { kind: 'RateChanged', at: '2026-07-24T10:00:00Z', payload: { rateBps: 5500 } },
  });
  assert.equal(bare.settlement, undefined);
});

test('a settlement reporting only one leg is not a split at all', () => {
  const onlyRepaid = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 45,
    event: {
      kind: 'JobSettled',
      jobId: 'job_demo_1',
      at: '2026-07-24T10:00:00Z',
      payload: { advanceRepaidAtomic: '12750000' },
    },
  });
  // Returning a split here would hand the money graph an empty string where an amount
  // belongs, and it would treat that partial report as authoritative.
  assert.equal(onlyRepaid.settlement, undefined);
  // The repaid leg is still usable as the headline amount.
  assert.equal(onlyRepaid.amountUsdc, '12750000');

  const onlyNet = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 46,
    event: {
      kind: 'JobSettled',
      jobId: 'job_demo_1',
      at: '2026-07-24T10:00:00Z',
      payload: { operatorNetAtomic: '12250000' },
    },
  });
  assert.equal(onlyNet.settlement, undefined);
});

test('AdvanceRepaid stays a repayment-leg event, distinct from a full settlement', () => {
  const repaid = humanizeStreamEvent({
    kind: 'event',
    source: 'chain',
    seq: 47,
    event: {
      kind: 'AdvanceRepaid',
      jobId: 'job_demo_1',
      at: '2026-07-24T10:00:00Z',
      payload: { amountAtomic: '12750000' },
    },
  });
  assert.equal(repaid.kind, 'AdvanceRepaid');
  // It reports no split, so nothing downstream may infer the operator's share from it.
  assert.equal(repaid.settlement, undefined);
  assert.match(repaid.text, /repaid first/i);
});
