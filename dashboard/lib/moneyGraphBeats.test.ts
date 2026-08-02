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

/**
 * Extracts the contents of every Go string literal, skipping comments.
 *
 * A regex cannot do this correctly in either direction. Matching every quoted run counts kinds
 * named in prose; stripping `//` with a regex first truncates any line whose STRING contains
 * `//`, silently dropping a real emitted kind. The second is the dangerous one: it makes the
 * drift guard pass over an unmapped event.
 *
 * So this walks the source once, tracking whether it is inside an interpreted string, a raw
 * string, a line comment or a block comment. Small, but the only version that is right.
 *
 * SCOPE, stated so this is not mistaken for total coverage: daemonEventKinds keeps only the
 * prefixes the money graph could plausibly animate. billing.*, budget.*, brain.msg.* and agent.*
 * are not money beats and are deliberately out of scope.
 */
function goStringLiterals(src: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++;
    } else if (c === '/' && src[i + 1] === '*') {
      i += 2;
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++;
      i += 2;
    } else if (c === '`') {
      i++;
      const start = i;
      while (i < src.length && src[i] !== '`') i++;
      out.push(src.slice(start, i));
      i++;
    } else if (c === '"') {
      i++;
      let lit = '';
      while (i < src.length && src[i] !== '"') {
        // A backslash escapes the next byte, so an escaped quote does not end the literal.
        if (src[i] === '\\') {
          lit += src[i]! + (src[i + 1] ?? '');
          i += 2;
          continue;
        }
        if (src[i] === '\n') break; // unterminated; Go would not compile, so stop cleanly
        lit += src[i];
        i++;
      }
      out.push(lit);
      i++;
    } else {
      i++;
    }
  }
  return out;
}

function daemonEventKinds(): Set<string> {
  const root = path.resolve(process.cwd(), '..', 'daemon');
  const kinds = new Set<string>();
  const KIND = /^(?:advance|settlement|payment|purchase|approval|policy|job|freeze|task)\.[a-z_.]+$/;

  const walk = (dir: string) => {
    for (const entry of readdirSafe(dir)) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
        // Anchored: the whole literal must BE a kind. A sentence mentioning one is not a literal
        // equal to it, so prose cannot register even when it is inside a string.
        for (const lit of goStringLiterals(readFileSync(full, 'utf8'))) {
          if (KIND.test(lit)) kinds.add(lit);
        }
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

test('a legless settlement must NOT drive the waterfall', () => {
  // I mapped settlement.executed to 'fall' and it was wrong. Its payload is {tx_hash, block,
  // gas_used} -- no legs, no amount -- and the 'fall' handler DERIVES the operator's share when
  // the legs are absent. That publishes a guess as fact and then lets the real JobSettled
  // animate the waterfall a second time, which is the defect PR #40 fixed. Only JobSettled
  // reports both legs, so only JobSettled may drain the escrow.
  assert.equal(
    beatFor(msg('settlement.executed')),
    null,
    'settlement.executed drives the waterfall with no legs to divide',
  );
  assert.equal(beatFor(msg('JobSettled')), 'fall');

  // Same rule, same reason: job.accepted carries no legs either. It was safe only by accident,
  // since nothing in production emits it and demoStream rewrites it to JobSettled before it
  // reaches beatFor. Only the event that reports both legs may divide the money.
  assert.equal(
    beatFor(msg('job.accepted')),
    null,
    'job.accepted drives the waterfall with no legs, contradicting the rule the file states',
  );
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
  'settlement.executed': 'the tx landed, but the payload carries no legs; JobSettled divides the money',
  'purchase.delivered': 'its amount is amount_micros, unread by eventAmount; approval.requested(approved) already counts this purchase',
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
