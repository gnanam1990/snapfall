import assert from 'node:assert/strict';
import test from 'node:test';

import { OWNER_STREAM_HANDOVER_LEAD_MS, OWNER_WINDOW_EVENT, ownerWindowFrame } from './ownerStream';
import type { OwnerSessionStatus } from './ownerSessionClient';
import {
  SESSION_READ_TIMEOUT_MS,
  STREAM_RETRY_MS,
  STREAM_UNAVAILABLE_MS,
  createFrameDedup,
  createStreamClient,
  frameKey,
  handoverDelay,
  ownerActionsBlocked,
  ownerDataHidden,
  resumeUrl,
  statusAfterClosed,
  type EventSourceLike,
  type StreamStatus,
} from './streamClient';
import type { StreamMessage } from './types';
import { createOwnerActionGate } from './ownerActionGate';

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

function harness(
  session: OwnerSessionStatus | null = { ownerApi: true, accessConfigured: true, signedIn: true },
  observe?: (status: StreamStatus) => void,
) {
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
    onStatus: (s) => {
      statuses.push(s);
      observe?.(s);
    },
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

test('after a session change, a signed-in read for a failed fresh connection still hides owner data', async () => {
  const h = harness(); // the session read answers signed in
  h.sources[0].open();
  h.client.sessionChanged(); // a sign-in elsewhere: a new session nobody has verified here
  const from = h.statuses.length - 1;
  h.sources[1].fail(); // the fresh connection fails before it ever opened
  await h.settle();
  for (const s of h.statuses.slice(from)) assert.equal(ownerDataHidden(s), true, `status ${s} showed owner data`);
  h.advance(STREAM_RETRY_MS);
  assert.equal(h.sources.length, 3, 'it retries');
  h.sources[2].open();
  assert.equal(h.statuses.at(-1), 'live', 'shown again once a connection passes the guard');
});

test('a verified stream the browser gave up on, with a signed-in read, rides it out as reconnecting', async () => {
  const h = harness();
  h.sources[0].open();
  h.sources[0].fail(); // e.g. a 502 from a proxy: the session is still good
  await h.settle();
  assert.equal(h.statuses.at(-1), 'reconnecting');
});

/** Fail the newest connection the way a broken stream route does, then let the session read answer
 *  and the retry timer run, `rounds` times. Returns the time that took. */
async function failRounds(h: ReturnType<typeof harness>, rounds: number): Promise<number> {
  let spent = 0;
  for (let i = 0; i < rounds; i++) {
    h.sources.at(-1)!.fail();
    await h.settle();
    h.advance(STREAM_RETRY_MS);
    spent += STREAM_RETRY_MS;
  }
  return spent;
}

test('owner actions are blocked only while signed out or unverified; unavailable hides data but allows them', () => {
  assert.equal(ownerDataHidden('unavailable'), true, 'stream data stays hidden: nothing verified this visitor');
  assert.equal(ownerActionsBlocked('unavailable'), false, 'the action route checks the session itself');
  assert.equal(ownerActionsBlocked('unverified'), true);
  assert.equal(ownerActionsBlocked('signed-out'), true);
  for (const s of ['connecting', 'live', 'reconnecting'] as const) assert.equal(ownerActionsBlocked(s), false, s);
});

test('a stream route that keeps failing after a session change is reported unavailable, not unverified forever', async () => {
  const h = harness(); // the session read answers signed in: the stream route alone is broken
  h.sources[0].open();
  h.client.sessionChanged();
  assert.equal(h.statuses.at(-1), 'unverified');
  const spent = await failRounds(h, 3);
  assert.ok(spent < STREAM_UNAVAILABLE_MS, 'a few failed rounds are still only unverified');
  assert.equal(h.statuses.at(-1), 'unverified');
  h.advance(STREAM_UNAVAILABLE_MS - spent);
  assert.equal(h.statuses.at(-1), 'unavailable');
  assert.equal(ownerDataHidden(h.statuses.at(-1)!), true, 'fails closed: stream data stays hidden');
  const before = h.sources.length;
  await failRounds(h, 2);
  assert.ok(h.sources.length > before, 'reconnection continues');
  assert.equal(h.statuses.at(-1), 'unavailable', 'later failures do not fall back to an endless unverified');
  h.sources.at(-1)!.open();
  assert.equal(h.statuses.at(-1), 'live', 'a connection that opens ends it');
});

test('unavailable also covers an unreadable session check, and a later sign-out read still says signed-out', async () => {
  const h = harness(null); // /api/owner/session keeps failing too
  h.sources[0].fail();
  await h.settle();
  assert.equal(h.statuses.at(-1), 'unverified');
  h.advance(STREAM_UNAVAILABLE_MS);
  assert.equal(h.statuses.at(-1), 'unavailable', 'the page can now offer the sign-in prompt');
  h.state.session = { ownerApi: true, accessConfigured: true, signedIn: false };
  await failRounds(h, 1);
  assert.equal(h.statuses.at(-1), 'signed-out', 'a read that shows a sign-out is reported as one');
  assert.equal(h.pending(), 0, 'signed-out waits for a session change, with no timer left running');
});

test('a first connection that never answers is reported unavailable', () => {
  const h = harness();
  assert.equal(h.statuses.length, 0, 'still connecting');
  h.advance(STREAM_UNAVAILABLE_MS);
  assert.equal(h.statuses.at(-1), 'unavailable');
  h.sources[0].open();
  assert.equal(h.statuses.at(-1), 'live');
});

test('a session change from unavailable passes through unverified, so actions are blocked before anything else', async () => {
  const h = harness();
  h.sources[0].fail();
  await h.settle();
  h.advance(STREAM_UNAVAILABLE_MS);
  assert.equal(h.statuses.at(-1), 'unavailable');
  h.client.sessionChanged(); // a sign-out in another tab
  assert.equal(h.statuses.at(-1), 'unverified', 'reported synchronously, before any reconnect');
  assert.equal(ownerActionsBlocked(h.statuses.at(-1)!), true);
  // The deadline starts again for the new session.
  await failRounds(h, 1);
  assert.equal(h.statuses.at(-1), 'unverified');
  h.advance(STREAM_UNAVAILABLE_MS);
  assert.equal(h.statuses.at(-1), 'unavailable');
});

test('a verified stream never becomes unavailable while it rides out drops', async () => {
  const h = harness();
  h.sources[0].open();
  h.sources[0].drop();
  h.advance(STREAM_UNAVAILABLE_MS * 2);
  assert.equal(h.statuses.at(-1), 'reconnecting');
  h.sources[0].fail(); // a 502 from a proxy, with a signed-in read
  await h.settle();
  h.advance(STREAM_UNAVAILABLE_MS * 2);
  assert.ok(!h.statuses.includes('unavailable'), h.statuses.join(','));
});

test('dispose cancels the unavailable deadline', () => {
  const h = harness();
  h.client.dispose();
  assert.equal(h.pending(), 0);
});

// ── The owner-action gate (lib/ownerActionGate), fed by the stream client as useEventStream feeds it.

test('a mint in flight is fenced the moment the session changes, with no render in between', () => {
  const gate = createOwnerActionGate();
  const h = harness(undefined, gate.observe);
  h.sources[0].open();
  const current = gate.begin();
  assert.ok(current, 'a live owner may mint');
  assert.equal(current(), true);
  h.client.sessionChanged(); // a sign-out in another tab, while the mint is in flight
  // The page's old fence was a React effect, run only after the commit that hid owner data; a
  // response landing before it still stored the link and copied it. The gate needs no commit.
  assert.equal(current(), false, 'the response that lands next must not be stored or copied');
  assert.equal(gate.begin(), null, 'and no new mint may start while the session is unsettled');
});

test('a session change that settles before the response arrives still voids the earlier mint', () => {
  const gate = createOwnerActionGate();
  const h = harness(undefined, gate.observe);
  h.sources[0].open();
  const current = gate.begin()!;
  h.client.sessionChanged(); // signed out and back in elsewhere: a different session
  h.sources[1].open(); // the new session passes the guard before the old mint answers
  assert.equal(h.statuses.at(-1), 'live');
  assert.equal(current(), false, 'a link minted under the old session is not shown under the new one');
  assert.ok(gate.begin(), 'the new session may mint');
});

test('a signed-out stream blocks minting; an unavailable one lets the mint route decide', async () => {
  const gate = createOwnerActionGate();
  const h = harness({ ownerApi: true, accessConfigured: true, signedIn: false }, gate.observe);
  h.sources[0].fail();
  await h.settle();
  assert.equal(h.statuses.at(-1), 'signed-out');
  assert.equal(gate.begin(), null);

  const g2 = createOwnerActionGate();
  const h2 = harness(null, g2.observe); // stream and session read both broken
  h2.sources[0].fail();
  await h2.settle();
  assert.equal(g2.begin(), null, 'unverified: blocked');
  h2.advance(STREAM_UNAVAILABLE_MS);
  assert.equal(h2.statuses.at(-1), 'unavailable');
  const current = g2.begin();
  assert.ok(current, 'unavailable: the mint goes out and its route checks the session');
  h2.client.sessionChanged();
  assert.equal(current!(), false, 'a session change from unavailable still voids it');
});

test('a newer mint supersedes an older one still in flight', () => {
  const gate = createOwnerActionGate();
  const h = harness(undefined, gate.observe);
  h.sources[0].open();
  const first = gate.begin()!;
  const second = gate.begin()!;
  assert.equal(first(), false);
  assert.equal(second(), true);
});
