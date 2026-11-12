/**
 * Bounds a proxied owner event stream to the session that opened it.
 *
 * The owner guard (lib/ownerApi requireOwnerRequest) runs once, when the stream opens, and a
 * stream can stay open for hours. Without a bound, a stream opened a minute before the session
 * expires keeps delivering owner events after it, and one opened before a sign-out keeps going
 * after the cookie is cleared. So the stream ends cleanly at the earlier of
 *   - the session's own expiry, and
 *   - OWNER_STREAM_REVERIFY_MS after it opened,
 * and EventSource's automatic reconnect then has to pass the guard again with whatever cookie the
 * browser holds by then. An expired or cleared session gets 401 there, which the client turns into
 * a sign-in prompt. The reconnect sends Last-Event-ID, so a live owner loses no events.
 */

/** Longest an owner stream runs before its next reconnect must pass the guard again. */
export const OWNER_STREAM_REVERIFY_MS = 5 * 60 * 1000;

/** When a stream opened at `now` under a session ending at `sessionExpiresAt` must close. */
export function ownerStreamCloseAt(sessionExpiresAt: number, now: number, reverifyMs: number = OWNER_STREAM_REVERIFY_MS): number {
  return Math.min(sessionExpiresAt, now + reverifyMs);
}

/**
 * `source`, re-emitted until `stop` aborts, then closed cleanly (not errored) with the source
 * cancelled. An error the source raises because `stop` aborted its fetch is a clean close too.
 * `onDone` runs exactly once, however the stream ends: stop, source end, source error, or the
 * client going away.
 */
export function streamUntil(
  source: ReadableStream<Uint8Array>,
  stop: AbortSignal,
  onDone?: () => void,
): ReadableStream<Uint8Array> {
  const reader = source.getReader();
  let finished = false;
  let controller!: ReadableStreamDefaultController<Uint8Array>;

  const finish = (): boolean => {
    if (finished) return false;
    finished = true;
    stop.removeEventListener('abort', onStop);
    onDone?.();
    return true;
  };

  function onStop() {
    if (!finish()) return;
    reader.cancel(stop.reason).catch(() => {});
    try {
      controller.close();
    } catch {
      /* already closed */
    }
  }

  return new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
      if (stop.aborted) onStop();
      else stop.addEventListener('abort', onStop, { once: true });
    },
    async pull(c) {
      if (finished) return;
      let chunk: ReadableStreamReadResult<Uint8Array>;
      try {
        chunk = await reader.read();
      } catch (error) {
        if (!finish()) return;
        if (stop.aborted) c.close();
        else c.error(error);
        return;
      }
      if (finished) return;
      if (chunk.done) {
        finish();
        c.close();
        return;
      }
      c.enqueue(chunk.value);
    },
    cancel(reason) {
      if (finish()) return reader.cancel(reason).catch(() => {});
    },
  });
}
