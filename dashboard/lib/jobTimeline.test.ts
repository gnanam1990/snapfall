import assert from 'node:assert/strict';
import test from 'node:test';

import { belongsToJob, expenseRows, type JobIdentities } from './jobTimeline';
import type { ActivityMessage } from './activity';

const VAULT = '0x583fdd5f61e34c44abdeacd33c300d9c1fce1ad22c56eeb2a196ce61eb6fb3fc';
const DAEMON = 'job_spine_20260801T160513Z';

const ids: JobIdentities = { vaultJobId: VAULT, daemonJobId: DAEMON };

function msg(over: Partial<ActivityMessage>): ActivityMessage {
  return {
    id: 'e1',
    actor: 'funding',
    role: 'agent',
    initials: 'FA',
    tone: 'neutral',
    text: 'something happened',
    at: '2026-08-02T09:00:00Z',
    kind: 'policy.evaluated',
    filter: 'money',
    ...over,
  } as ActivityMessage;
}

// ── the bug, both directions ────────────────────────────────────────────────

test('an event that names no job is NOT shown on a job timeline', () => {
  // The old filter was `if (item.jobId && item.jobId !== jobId) return;` — the `item.jobId &&`
  // guard let an unattributed event through, so a global RateChanged rendered inside one job's
  // history as though it were part of that job's story.
  assert.equal(
    belongsToJob(msg({ jobId: undefined }), ids),
    false,
    'an event with no jobId was admitted; the timeline claims everything on it is about this job',
  );
});

test('a daemon event keyed by the business id IS shown', () => {
  // The whole daemon vocabulary — approvals, policy decisions, payments — is keyed by the
  // business id, which never equals the bytes32 in the route. Every one of them was dropped
  // from the page that exists to explain them.
  assert.equal(
    belongsToJob(msg({ jobId: DAEMON, kind: 'approval.requested' }), ids),
    true,
    'a daemon event for this job was dropped: the timeline shows chain movement with no reason for it',
  );
});

test('a chain event keyed by the bytes32 vault id IS shown', () => {
  assert.equal(belongsToJob(msg({ jobId: VAULT, kind: 'AdvanceIssued' }), ids), true);
});

test('another job\'s events are never shown, on either identity', () => {
  const otherVault = `0x${'ab'.repeat(32)}`;
  assert.equal(belongsToJob(msg({ jobId: otherVault }), ids), false);
  assert.equal(belongsToJob(msg({ jobId: 'job_spine_SOMETHING_ELSE' }), ids), false);
});

test('hex identity comparison is case-insensitive', () => {
  // The chain writes checksum-cased addresses and ids; a route param pasted from an explorer or
  // a log can differ in case for the very same job. A case mismatch would blank the timeline
  // while every id "looked right" on screen — the hardest kind of empty to debug.
  assert.equal(belongsToJob(msg({ jobId: VAULT.toUpperCase().replace('0X', '0x') }), ids), true);
});

test('without a known daemon id, chain events still show and daemon events do not', () => {
  // The honest intermediate state: until the page can learn the business id, it must not guess.
  // Showing chain events is correct; silently showing another job's daemon events would not be.
  const chainOnly: JobIdentities = { vaultJobId: VAULT, daemonJobId: null };
  assert.equal(belongsToJob(msg({ jobId: VAULT }), chainOnly), true);
  assert.equal(belongsToJob(msg({ jobId: DAEMON }), chainOnly), false);
});

// ── expense rows ────────────────────────────────────────────────────────────

test('expense rows carry the amount and the explorer link the event supplied', () => {
  // The page showed only a SUM read from the contract, so "0.10 spent" had nothing behind it.
  // ExpenseRecorded reaches the browser only because of the H2 chain relay; before that these
  // rows were not merely unrendered, they were unobtainable.
  const rows = expenseRows([
    msg({ id: 'x1', kind: 'ExpenseRecorded', amountUsdc: '40000', explorerUrl: 'https://ex/tx/0xaaa', text: 'company profile' }),
    msg({ id: 'x2', kind: 'approval.requested' }),
    msg({ id: 'x3', kind: 'ExpenseRecorded', amountUsdc: '60000', text: 'benchmark summary' }),
  ]);

  assert.equal(rows.length, 2, 'non-expense events leaked into the expense rows');
  assert.equal(rows[0]?.amountUsdc, '40000');
  assert.equal(rows[0]?.explorerUrl, 'https://ex/tx/0xaaa');
  assert.equal(
    rows[1]?.explorerUrl,
    undefined,
    'an absent explorer link must stay absent so the row renders as text, never as a dead link',
  );
});

test('expense rows preserve the order they were given', () => {
  // The timeline is newest-first and the rows are a projection of it, not a re-sort: two lists
  // built from one source cannot disagree, which is the point of deriving rather than refetching.
  const rows = expenseRows([
    msg({ id: 'newer', kind: 'ExpenseRecorded', at: '2026-08-02T10:00:00Z' }),
    msg({ id: 'older', kind: 'ExpenseRecorded', at: '2026-08-02T09:00:00Z' }),
  ]);
  assert.deepEqual(rows.map((r) => r.id), ['newer', 'older']);
});
