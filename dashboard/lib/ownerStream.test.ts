import assert from 'node:assert/strict';
import test from 'node:test';

import {
  OWNER_STREAM_REVERIFY_MS,
  ownerStreamCloseAt,
  ownerStreamHandoverIn,
  ownerStreamResumeId,
  ownerWindowFrame,
  parseOwnerWindow,
  streamUntil,
} from './ownerStream';
import { OWNER_SESSION_TTL_MS, issueSessionValue, validSessionExpiry } from './ownerSession';

const FAKE_SECRET = 'fake-dashboard-owner-secret-for-tests-0000';
const enc = new TextEncoder();
const dec = new TextDecoder();

/** An upstream that sends `first` and then stays open forever, recording whether it was cancelled. */
function openUpstream(first: string) {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(enc.encode(first));
    },
    cancel() {
      cancelled = true;
    },
  });
  return { body, cancelled: () => cancelled };
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<string> {
  const reader = stream.getReader();
  let out = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return out;
    out += dec.decode(value);
  }
}

test('validSessionExpiry is the valid session expiry, the latest of several, or null', () => {
  const now = 1_000_000;
  const a = issueSessionValue(FAKE_SECRET, now);
  const b = issueSessionValue(FAKE_SECRET, now + 5_000);
  assert.equal(validSessionExpiry(FAKE_SECRET, [a.value], now + 1), a.expiresAt);
  assert.equal(validSessionExpiry(FAKE_SECRET, [a.value, b.value], now + 5_001), b.expiresAt);
  assert.equal(validSessionExpiry(FAKE_SECRET, [b.value, a.value], now + 5_001), b.expiresAt);
  assert.equal(validSessionExpiry(FAKE_SECRET, ['garbage', a.value], now + 1), a.expiresAt, 'a junk value does not hide a valid one');
  assert.equal(validSessionExpiry(FAKE_SECRET, [], now), null);
  assert.equal(validSessionExpiry(FAKE_SECRET, [a.value], now + OWNER_SESSION_TTL_MS), null, 'expired');
  assert.equal(validSessionExpiry('fake-rotated-secret-for-tests-000000000000', [a.value], now + 1), null, 'foreign');
});

test('a stream closes at the session expiry or the re-verify interval, whichever is first', () => {
  const now = 10_000_000;
  assert.equal(ownerStreamCloseAt(now + 60_000, now), now + 60_000, 'a session ending soon closes the stream then');
  assert.equal(ownerStreamCloseAt(now + OWNER_SESSION_TTL_MS, now), now + OWNER_STREAM_REVERIFY_MS, 'a long session is re-checked');
  assert.ok(OWNER_STREAM_REVERIFY_MS < OWNER_SESSION_TTL_MS);
});

test('streamUntil passes data through, then closes cleanly and cancels the source when stopped', async () => {
  const upstream = openUpstream('data: {"kind":"snapshot"}\n\n');
  const stop = new AbortController();
  let done = 0;
  const out = streamUntil(upstream.body, stop.signal, () => done++);
  const reader = out.getReader();
  const first = await reader.read();
  assert.equal(dec.decode(first.value), 'data: {"kind":"snapshot"}\n\n');

  const pending = reader.read(); // the upstream has nothing more: this waits
  stop.abort(new Error('window ended'));
  const end = await pending;
  assert.equal(end.done, true, 'a clean end, not an error, so EventSource reconnects');
  assert.equal(upstream.cancelled(), true, 'the upstream connection is released');
  assert.equal(done, 1);
});

test('streamUntil ends when the source ends, and when the client cancels, running onDone once', async () => {
  const stop = new AbortController();
  let done = 0;
  const finite = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(enc.encode('a'));
      c.close();
    },
  });
  assert.equal(await readAll(streamUntil(finite, stop.signal, () => done++)), 'a');
  assert.equal(done, 1);
  stop.abort();
  assert.equal(done, 1, 'a stop after the end does nothing');

  const upstream = openUpstream('x');
  let cancelledDone = 0;
  const out = streamUntil(upstream.body, new AbortController().signal, () => cancelledDone++);
  await out.cancel('client went away');
  assert.equal(upstream.cancelled(), true);
  assert.equal(cancelledDone, 1);
});

test('a source error caused by the stop is a clean close; any other source error is passed on', async () => {
  const stop = new AbortController();
  const failing = new ReadableStream<Uint8Array>({
    pull(c) {
      stop.abort();
      c.error(new Error('aborted fetch'));
    },
  });
  assert.equal(await readAll(streamUntil(failing, stop.signal)), '');

  const broken = new ReadableStream<Uint8Array>({
    pull(c) {
      c.error(new Error('daemon dropped'));
    },
  });
  await assert.rejects(readAll(streamUntil(broken, new AbortController().signal)), /daemon dropped/);
});

test('a stream already past its window closes at once', async () => {
  const upstream = openUpstream('late');
  const stop = new AbortController();
  stop.abort();
  assert.equal(await readAll(streamUntil(upstream.body, stop.signal)), '');
  assert.equal(upstream.cancelled(), true);
});

test('only a re-verify close is announced: a stream ending with its session has nothing to hand over to', () => {
  const now = 10_000_000;
  assert.equal(ownerStreamHandoverIn(now + OWNER_SESSION_TTL_MS, now), OWNER_STREAM_REVERIFY_MS);
  assert.equal(ownerStreamHandoverIn(now + 60_000, now), null, 'the session ends first');
  assert.equal(ownerStreamHandoverIn(now + OWNER_STREAM_REVERIFY_MS, now), null, 'both at once: the session ends');
  assert.equal(ownerStreamHandoverIn(now + OWNER_STREAM_REVERIFY_MS + 1, now), OWNER_STREAM_REVERIFY_MS);
});

test('the window frame is a named event with no id, and parses back; anything else is refused', () => {
  const frame = ownerWindowFrame(299_999.6);
  assert.equal(frame, 'event: owner-window\ndata: {"closesInMs":300000}\n\n');
  assert.doesNotMatch(frame, /^id:/m, 'it must never move Last-Event-ID');
  assert.equal(parseOwnerWindow('{"closesInMs":300000}'), 300_000);
  assert.equal(parseOwnerWindow('{"closesInMs":0}'), 0);
  for (const bad of ['', 'nope', '{}', '{"closesInMs":-1}', '{"closesInMs":"5"}', '{"closesInMs":300001}', 'null']) {
    assert.equal(parseOwnerWindow(bad), null, bad);
  }
  assert.equal(parseOwnerWindow(undefined), null);
});

test('the resume point: the browser header first, else a decimal query parameter, never anything else', () => {
  const req = (url: string, lastEventId?: string) =>
    new Request(url, { headers: lastEventId ? { 'last-event-id': lastEventId } : {} });
  const base = 'http://dashboard.local/api/events/stream';
  assert.equal(ownerStreamResumeId(req(base)), null);
  assert.equal(ownerStreamResumeId(req(`${base}?lastEventId=41`)), '41');
  assert.equal(ownerStreamResumeId(req(`${base}?lastEventId=41`, '57')), '57', 'EventSource\'s own reconnect is newer');
  for (const junk of ['abc', '-1', '1.5', '1%0d%0aX-Injected:%201', '12345678901234567890', '']) {
    assert.equal(ownerStreamResumeId(req(`${base}?lastEventId=${junk}`)), null, junk);
  }
});

test('streamUntil sends its preamble before anything from the source', async () => {
  const upstream = openUpstream('data: {"kind":"snapshot"}\n\n');
  const stop = new AbortController();
  const out = streamUntil(upstream.body, stop.signal, undefined, enc.encode(ownerWindowFrame(1_000)));
  const reader = out.getReader();
  const first = dec.decode((await reader.read()).value);
  const second = dec.decode((await reader.read()).value);
  assert.equal(first, ownerWindowFrame(1_000));
  assert.match(second, /"snapshot"/);
  stop.abort();
  assert.equal((await reader.read()).done, true);
  assert.equal(upstream.cancelled(), true);
});
