import assert from 'node:assert/strict';
import test from 'node:test';

import { OWNER_STREAM_HANDOVER_LEAD_MS, OWNER_WINDOW_EVENT, ownerWindowFrame } from './ownerStream';
import type { OwnerSessionStatus } from './ownerSessionClient';
import {
  SESSION_READ_TIMEOUT_MS,
  STREAM_RETRY_MS,
  createFrameDedup,
  createStreamClient,
  frameKey,
  handoverDelay,
  ownerDataHidden,
  resumeUrl,
  statusAfterClosed,
  type EventSourceLike,
  type StreamStatus,
} from './streamClient';
import type { StreamMessage } from './types';

/** A scriptable EventSource: the test plays the server. */
class FakeSource implements EventSourceLike {
  readyState = 0;
  closed = false;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  private listeners = new Map<string, ((ev: Event) => void)[]>();
  private lastId = '';
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (ev: Event) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  open() {
    this.readyState = 1;
    this.onopen?.(new Event('open'));
  }
  /** One SSE frame; `id` moves this source's Last-Event-ID as a browser's would. */
  send(msg: StreamMessage, id?: number) {
    if (this.closed) return;
    if (id !== undefined) this.lastId = String(id);
    this.onmessage?.({ data: JSON.stringify(msg), lastEventId: this.lastId } as MessageEvent);
  }
  /** The route's window announcement, parsed from the exact frame it writes. */
  announce(closesInMs: number) {
    const data = ownerWindowFrame(closesInMs).split('\n').find((l) => l.startsWith('data: '))!.slice(6);
    for (const l of this.listeners.get(OWNER_WINDOW_EVENT) ?? []) l({ data } as MessageEvent);
  }
  /** The server ended the response: the browser will reconnect this source by itself. */
  drop() {
    this.readyState = 0;
    this.onerror?.(new Event('error'));
  }
  /** The browser gave up (a non-200 such as the guard's 401). */
  fail() {
    this.readyState = 2;
    this.onerror?.(new Event('error'));
  }
}

function harness(session: OwnerSessionStatus | null = { ownerApi: true, accessConfigured: true, signedIn: true }) {
  const sources: FakeSource[] = [];
  const delivered: StreamMessage[] = [];
  const statuses: StreamStatus[] = [];
  let timers: { fn: () => void; at: number; id: number }[] = [];
  let clock = 0;
  let nextId = 1;
  const state: { session: OwnerSessionStatus | null; read?: () => Promise<OwnerSessionStatus | null> } = { session };
  const client = createStreamClient({
    url: '/api/events/stream',
    onMessage: (m) => delivered.push(m),
    onStatus: (s) => statuses.push(s),
    open: (u) => {
      const s = new FakeSource(u);
      sources.push(s);
      return s;
    },
    readSession: () => (state.read ? state.read() : Promise.resolve(state.session)),
    setTimer: (fn, ms) => {
      const id = nextId++;
      timers.push({ fn, at: clock + ms, id });
      return id as unknown as ReturnType<typeof setTimeout>;
    },
    clearTimer: (t) => {
      timers = timers.filter((x) => x.id !== (t as unknown as number));
    },
  });
  const advance = (ms: number) => {
    clock += ms;
    for (;;) {
      const due = timers.filter((t) => t.at <= clock).sort((a, b) => a.at - b.at)[0];
      if (!due) return;
      timers = timers.filter((t) => t !== due);
      due.fn();
    }
  };
  const settle = () => new Promise((r) => setImmediate(r));
  return { client, sources, delivered, statuses, advance, settle, state, pending: () => timers.length };
}

const chain = (block: number, logIndex: number): StreamMessage => ({
  kind: 'event',
  source: 'chain',
  seq: `${block}:${logIndex}`,
  event: { kind: 'AdvanceIssued' } as never,
});
const daemon = (seq: number): StreamMessage => ({
  kind: 'event',
  source: 'daemon',
  seq,
  event: { kind: 'job.created' } as never,
});
const snapshot = { kind: 'snapshot', snapshot: {} } as unknown as StreamMessage;
const seqs = (ms: StreamMessage[]) => ms.map((m) => (m.kind === 'event' ? `${m.source}:${m.seq}` : 'snapshot'));

test('owner data is hidden while signed out or unverified, and shown otherwise', () => {
  assert.equal(ownerDataHidden('signed-out'), true);
  assert.equal(ownerDataHidden('unverified'), true);
  assert.equal(ownerDataHidden('live'), false);
  assert.equal(ownerDataHidden('reconnecting'), false, 'a verified session riding out a network blip');
  assert.equal(ownerDataHidden('connecting'), false, 'nothing has arrived yet to hide');
});

test('a closed stream: no session is signed-out, an unreadable check is unverified, else reconnecting', () => {
  assert.equal(statusAfterClosed({ ownerApi: true, accessConfigured: true, signedIn: false }), 'signed-out');
  assert.equal(statusAfterClosed(null), 'unverified');
  assert.equal(statusAfterClosed({ ownerApi: true, accessConfigured: true, signedIn: true }), 'reconnecting');
  assert.equal(statusAfterClosed({ ownerApi: false, accessConfigured: false, signedIn: false }), 'reconnecting');
});

test('handover timing and the resume URL', () => {
  assert.equal(handoverDelay(5 * 60_000), 5 * 60_000 - OWNER_STREAM_HANDOVER_LEAD_MS);
  assert.equal(handoverDelay(1_000), 0, 'a window shorter than the lead hands over at once');
  assert.equal(resumeUrl('/api/events/stream', '42'), '/api/events/stream?lastEventId=42');
  assert.equal(resumeUrl('/api/events/stream?x=1', '42'), '/api/events/stream?x=1&lastEventId=42');
});

test('frame dedup: chain and daemon sequences are separate namespaces; snapshots and demo frames pass', () => {
  const admit = createFrameDedup(3);
  assert.equal(admit(chain(10, 0)), true);
  assert.equal(admit(chain(10, 0)), false);
  assert.equal(admit(daemon(7)), true);
  assert.equal(admit({ ...chain(7, 0), seq: 7 } as StreamMessage), true, 'chain 7 is not daemon 7');
  assert.equal(admit(snapshot), true);
  assert.equal(admit(snapshot), true);
  assert.equal(admit({ ...daemon(7), demo: true } as StreamMessage), true, 'the demo replay is never deduplicated');
  assert.equal(frameKey(snapshot), null);
  // Bounded: the oldest key is forgotten once the cap is passed.
  admit(daemon(8));
  assert.equal(admit(chain(10, 0)), true);
});

test('handover: chain events in the gap arrive, daemon events resume, and nothing arrives twice', async () => {
  const h = harness();
  const [a] = h.sources;
  a.open();
  a.send(snapshot);
  a.send(daemon(1), 1);
  a.send(chain(100, 0));
  a.announce(5 * 60_000);

  h.advance(5 * 60_000 - OWNER_STREAM_HANDOVER_LEAD_MS);
  assert.equal(h.sources.length, 2, 'the replacement opens before the old window ends');
  const b = h.sources[1];
  assert.equal(b.url, '/api/events/stream?lastEventId=1', 'it resumes daemon events after the last id seen');
  assert.equal(a.closed, false, 'the old connection keeps delivering through the overlap');

  // The daemon starts B at its chain head. Events landing before B read its cursor come on A only;
  // later ones on both.
  a.send(chain(100, 1)); // only A: indexed before B's cursor
  b.open();
  b.send(snapshot);
  a.send(daemon(2), 2);
  b.send(daemon(2), 2); // resumed after 1: the same event on both
  a.send(chain(101, 0));
  b.send(chain(101, 0));
  a.drop(); // A's window ends
  assert.equal(a.closed, true, 'the old connection is not reconnected alongside the new one');
  b.send(chain(102, 0));

  assert.deepEqual(seqs(h.delivered), [
    'snapshot',
    'daemon:1',
    'chain:100:0',
    'chain:100:1',
    'snapshot',
    'daemon:2',
    'chain:101:0',
    'chain:102:0',
  ]);
  assert.deepEqual(h.statuses, ['live', 'live'], 'a handover never reports the stream down');
});

test('the next handover resumes from the newest daemon id, whichever connection delivered it', () => {
  const h = harness();
  const [a] = h.sources;
  a.open();
  a.send(daemon(5), 5);
  a.announce(60_000);
  h.advance(60_000);
  const b = h.sources[1];
  b.open();
  b.send(daemon(6), 6);
  a.drop();
  b.announce(5 * 60_000);
  h.advance(5 * 60_000);
  assert.equal(h.sources[2].url, '/api/events/stream?lastEventId=6');
});

test('a stream without a window announcement (no daemon, demo, session expiry) never hands over', () => {
  const h = harness();
  h.sources[0].open();
  h.sources[0].send(daemon(1), 1);
  h.advance(60 * 60_000);
  assert.equal(h.sources.length, 1);
  assert.equal(h.pending(), 0);
});

test('a handover the guard refuses closes the old connection too and reports signed-out', async () => {
  const h = harness({ ownerApi: true, accessConfigured: true, signedIn: false });
  const [a] = h.sources;
  a.open();
  a.announce(OWNER_STREAM_HANDOVER_LEAD_MS);
  h.advance(0);
  const b = h.sources[1];
  b.fail(); // 401: the cookie was cleared and no other tab announced it
  assert.equal(a.closed, true, 'the old session stops delivering at once');
  await h.settle();
  assert.equal(h.statuses.at(-1), 'signed-out');
  a.send(daemon(9), 9);
  assert.equal(h.delivered.length, 0);
  h.advance(60_000);
  assert.equal(h.sources.length, 2, 'signed-out waits for a session change, it does not retry');
});

test('a session change hides owner data until the stream is live again, and starts fresh', () => {
  const h = harness();
  const [a] = h.sources;
  a.open();
  a.send(daemon(3), 3);
  h.client.sessionChanged();
  assert.equal(h.statuses.at(-1), 'unverified');
  assert.equal(ownerDataHidden(h.statuses.at(-1)!), true);
  assert.equal(a.closed, true);
  const b = h.sources[1];
  assert.equal(b.url, '/api/events/stream', 'a new session replays from the start, it does not resume');
  b.open();
  assert.equal(h.statuses.at(-1), 'live');
  b.send(daemon(3), 3);
  assert.equal(h.delivered.length, 2, 'the replay is not deduplicated against the old session');
});

test('a failed session check after a close keeps owner data hidden while it retries', async () => {
  const h = harness(null);
  const [a] = h.sources;
  a.open();
  h.client.sessionChanged(); // a sign-out in another tab
  const b = h.sources[1];
  b.fail();
  await h.settle();
  assert.equal(h.statuses.at(-1), 'unverified', 'not reconnecting: nothing verified this visitor');
  assert.equal(ownerDataHidden(h.statuses.at(-1)!), true);
  h.advance(STREAM_RETRY_MS);
  assert.equal(h.sources.length, 3, 'reconnection continues');
  h.sources[2].fail();
  await h.settle();
  assert.equal(h.statuses.at(-1), 'unverified');

  h.state.session = { ownerApi: true, accessConfigured: true, signedIn: true };
  h.advance(STREAM_RETRY_MS);
  h.sources[3].open();
  assert.equal(h.statuses.at(-1), 'live');
});

test('a transient drop of a verified stream stays visible as reconnecting', () => {
  const h = harness();
  h.sources[0].open();
  h.sources[0].drop();
  assert.equal(h.statuses.at(-1), 'reconnecting');
  assert.equal(ownerDataHidden('reconnecting'), false);
  assert.equal(h.sources.length, 1, 'the browser reconnects it, not the client');
});

test('dispose closes both connections of a handover and cancels its timers', () => {
  const h = harness();
  const [a] = h.sources;
  a.open();
  a.announce(OWNER_STREAM_HANDOVER_LEAD_MS + 1_000);
  h.advance(1_000);
  const b = h.sources[1];
  h.client.dispose();
  assert.equal(a.closed, true);
  assert.equal(b.closed, true);
  assert.equal(h.pending(), 0);
});

test('a sign-out in another tab never shows owner data again while the session read is pending', async () => {
  const h = harness();
  const [a] = h.sources;
  a.open();
  assert.equal(h.statuses.at(-1), 'live');
  let answer: (s: OwnerSessionStatus | null) => void = () => {};
  h.state.read = () => new Promise((r) => (answer = r));
  h.client.sessionChanged(); // signed out elsewhere
  const from = h.statuses.length - 1;
  h.sources[1].fail(); // the guard refuses the fresh connection
  await h.settle();
  for (const s of h.statuses.slice(from)) assert.equal(ownerDataHidden(s), true, `status ${s} showed owner data`);
  answer({ ownerApi: true, accessConfigured: true, signedIn: false });
  await h.settle();
  assert.equal(h.statuses.at(-1), 'signed-out');
  for (const s of h.statuses.slice(from)) assert.equal(ownerDataHidden(s), true, `status ${s} showed owner data`);
});

test('a session read that never answers counts as unreadable and keeps owner data hidden', async () => {
  const h = harness();
  h.sources[0].open();
  h.state.read = () => new Promise(() => {});
  h.client.sessionChanged();
  h.sources[1].fail();
  await h.settle();
  h.advance(SESSION_READ_TIMEOUT_MS);
  await h.settle();
  assert.equal(h.statuses.at(-1), 'unverified');
  h.advance(STREAM_RETRY_MS);
  assert.equal(h.sources.length, 3, 'it retries after the bounded read');
});

test('a fresh connection that drops before it ever opened is unverified, not reconnecting', () => {
  const h = harness();
  h.sources[0].drop(); // dropped before it ever opened
  assert.notEqual(h.statuses.at(-1), 'reconnecting');
  assert.equal(ownerDataHidden(h.statuses.at(-1)!), true);
});

