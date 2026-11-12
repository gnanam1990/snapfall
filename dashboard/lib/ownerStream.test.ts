import assert from 'node:assert/strict';
import test from 'node:test';

import { OWNER_STREAM_REVERIFY_MS, ownerStreamCloseAt, streamUntil } from './ownerStream';
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
