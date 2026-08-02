import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';

import { beatFor } from '../components/MoneyGraph';
import type { ActivityMessage } from './activity';

/**
 * The money graph is the demo's centrepiece, and its beats were mapped against a vocabulary the
 * daemon does not speak.
 *
 * `advance.issued` was mapped; the daemon writes `advance.executed`. `payment.delivered` was
 * mapped; no daemon code emits it. `settlement.executed` -- the waterfall, the moment the whole
 * product is named after -- was mapped to `job.accepted`, the customer's click, which is a
 * different moment. Against the scripted mock everything animated, because the mock emits
 * whatever the mapping expects. Against a live daemon the graph sat still.
 *
 * So these tests read the DAEMON SOURCE for the kinds it actually writes, rather than trusting a
 * list maintained by hand. Adding an event kind in Go and forgetting the graph now goes red here.
 */

function msg(kind: string, over: Partial<ActivityMessage> = {}): ActivityMessage {
  return {
    id: 'e', actor: 'a', role: 'agent', initials: 'A', tone: 'neutral',
    text: 't', at: '2026-08-02T00:00:00Z', kind, filter: 'money', ...over,
  } as ActivityMessage;
}

/** Every `"some.kind"` string literal the daemon writes as an event kind. */
function daemonEventKinds(): Set<string> {
  const root = path.resolve(process.cwd(), '..', 'daemon');
  const kinds = new Set<string>();
  const re = /"((?:advance|settlement|payment|purchase|approval|policy|job|freeze|task)\.[a-z_.]+)"/g;

  const walk = (dir: string) => {
    for (const entry of readdirSafe(dir)) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
        for (const m of readFileSync(full, 'utf8').matchAll(re)) kinds.add(m[1]!);
      }
    }
  };
  walk(path.join(root, 'internal'));
  walk(path.join(root, 'cmd'));
  return kinds;
}

function readdirSafe(dir: string) {
  try {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    return require('node:fs').readdirSync(dir, { withFileTypes: true }) as Array<{
      name: string;
      isDirectory: () => boolean;
    }>;
  } catch {
    return [];
  }
}

// ── the specific regressions ────────────────────────────────────────────────

test('the snap beat fires on the kind the daemon actually writes', () => {
  assert.equal(
    beatFor(msg('advance.executed')),
    'snap',
    'advance.executed is what the daemon emits when the snap lands; unmapped, the graph is still ' +
      'through the most important moment of the demo',
  );
});

test('the waterfall fires on settlement, not only on the customer click', () => {
  assert.equal(
    beatFor(msg('settlement.executed')),
    'fall',
    'settlement.executed is when the money actually divides; job.accepted is the click before it',
  );
});

test('a completed purchase animates a spend', () => {
  assert.equal(beatFor(msg('purchase.delivered')), 'spend');
});

// ── the beats that were already right, so a fix cannot quietly break them ───

test('the previously-working mappings still hold', () => {
  assert.equal(beatFor(msg('JobFunded')), 'fund');
  assert.equal(beatFor(msg('AdvanceIssued')), 'snap');
  assert.equal(beatFor(msg('ExpenseRecorded')), 'spend');
  assert.equal(beatFor(msg('AdvanceRepaid')), 'repay');
  assert.equal(beatFor(msg('JobSettled')), 'fall');
  assert.equal(beatFor(msg('RateChanged')), 'flywheel');
  assert.equal(beatFor(msg('approval.reject')), 'reject');
});

test('a pending escalation is not a spend, an approved one is', () => {
  // The stream normalises both to approval.requested, so the STATE is the discriminator. If the
  // pending 4.00 animated a spend, the graph would show money leaving for a payment the owner is
  // still being asked about.
  assert.equal(beatFor(msg('approval.requested', { approvalState: 'pending' })), null);
  assert.equal(beatFor(msg('approval.requested', { approvalState: 'approved' })), 'spend');
});

// ── the drift guard ─────────────────────────────────────────────────────────

/**
 * Kinds the daemon emits that deliberately drive NO beat. Each needs a reason, because the cost
 * of wrongly listing one here is a beat that silently never fires again.
 */
const INTENTIONALLY_SILENT: Record<string, string> = {
  'policy.evaluated': 'a decision, not a movement of money',
  'approval.requested': 'handled above; state discriminates, kind alone cannot',
  'approval.approve': 'the approval itself moves nothing; the purchase that follows does',
  'approval.expired': 'nothing moved',
  'payment.executing': 'in flight; the graph animates completion, not intent',
  'payment.failed': 'no money moved',
  'purchase.pending_settlement': 'not yet settled',
  'purchase.unresolved': 'terminal-unknown; must not animate a completed spend',
  'purchase.payee_divergence': 'a refusal, not a movement',
  'advance.pending_chain': 'submitted, not landed',
  'advance.halted': 'no money moved',
  'advance.interrupted': 'no money moved',
  'advance.reverted': 'reverted on chain; nothing moved',
  'settlement.pending_chain': 'submitted, not landed',
  'settlement.reverted': 'reverted on chain; the waterfall did not divide anything',
  'task.withheld': 'a QA hold on delivery, not a movement of money',
  'freeze.engaged': 'a control-plane state, not a money beat',
  'freeze.lifted': 'a control-plane state, not a money beat',
  'freeze.engaged.duplicate': 'idempotency detail',
  'freeze.lifted.duplicate': 'idempotency detail',
};

test('every daemon event kind is either mapped to a beat or explicitly silent', () => {
  const kinds = daemonEventKinds();
  assert.ok(kinds.size > 10, `only found ${kinds.size} daemon kinds; the source scan is broken`);

  const unaccounted = [...kinds].filter(
    (k) => beatFor(msg(k)) === null && !(k in INTENTIONALLY_SILENT),
  );

  assert.deepEqual(
    unaccounted,
    [],
    'The daemon emits these kinds and the money graph neither animates them nor declares them ' +
      'silent:\n  ' + unaccounted.join('\n  ') +
      '\nDecide for each: add it to beatFor, or add it to INTENTIONALLY_SILENT with a reason. ' +
      'Leaving it unlisted is how advance.executed went unmapped while the mock kept animating.',
  );
});

test('the silent list does not name kinds the daemon no longer emits', () => {
  // A stale entry here is dead weight that also hides a real gap: if a kind is renamed, its old
  // name keeps excusing the new one's absence.
  const kinds = daemonEventKinds();
  const stale = Object.keys(INTENTIONALLY_SILENT).filter((k) => !kinds.has(k));
  assert.deepEqual(stale, [], `INTENTIONALLY_SILENT names kinds the daemon no longer emits: ${stale.join(', ')}`);
});
