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
