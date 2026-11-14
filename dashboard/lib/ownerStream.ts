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
 * a sign-in prompt.
 *
 * A plain reconnect would lose chain events. Last-Event-ID resumes the daemon's own events (their
 * SSE id is the daemon seq), but chain events carry no SSE id and the daemon starts every new
 * connection at its latest chain cursor (daemon/internal/ownerapi/chainrelay.go,
 * latestChainCursor), so anything indexed between the close and the reconnect never arrives. So a
 * stream closed by the re-verify interval announces its window first (OWNER_WINDOW_EVENT), and the
 * client opens its next connection OWNER_STREAM_HANDOVER_LEAD_MS before this one ends, resuming
 * daemon events from the last id it saw (OWNER_RESUME_PARAM). The two overlap: every chain event
 * after the new connection's cursor arrives on the new one, every one before it on the old one,
 * and the client drops what both delivered (lib/streamStatus createFrameDedup). A stream closed by
 * the session's own expiry announces nothing: its reconnect is refused anyway.
 */

/** Longest an owner stream runs before its next reconnect must pass the guard again. */
export const OWNER_STREAM_REVERIFY_MS = 5 * 60 * 1000;

/** How long before a re-verify close the client opens the connection that replaces it. The daemon
 *  polls every 200 ms, so this is a wide margin for the new connection to pass the guard and read
 *  its chain cursor while the old one is still delivering. */
export const OWNER_STREAM_HANDOVER_LEAD_MS = 30 * 1000;

/** The named SSE event that announces a stream's re-verify window. Named, so a page's onmessage
 *  never sees it. Its data is `{"closesInMs": n}`, relative so the two clocks need not agree. */
export const OWNER_WINDOW_EVENT = 'owner-window';

/** The query parameter a handover connection carries its daemon resume point in. A new
 *  EventSource cannot set Last-Event-ID itself; the route forwards this as that header. */
export const OWNER_RESUME_PARAM = 'lastEventId';

/** When a stream opened at `now` under a session ending at `sessionExpiresAt` must close. */
export function ownerStreamCloseAt(sessionExpiresAt: number, now: number, reverifyMs: number = OWNER_STREAM_REVERIFY_MS): number {
  return Math.min(sessionExpiresAt, now + reverifyMs);
}

/**
 * Milliseconds until a stream opened at `now` closes for re-verification, or null when it closes
 * because the session itself ends: then there is nothing to hand over to, and announcing a window
 * would only make the client open connections the guard refuses.
 */
export function ownerStreamHandoverIn(
  sessionExpiresAt: number,
  now: number,
  reverifyMs: number = OWNER_STREAM_REVERIFY_MS,
): number | null {
  const closeAt = ownerStreamCloseAt(sessionExpiresAt, now, reverifyMs);
  return closeAt < sessionExpiresAt ? Math.max(0, closeAt - now) : null;
}

/** The SSE frame announcing a re-verify window. No `id:` line, so it never moves Last-Event-ID. */
export function ownerWindowFrame(closesInMs: number): string {
  return `event: ${OWNER_WINDOW_EVENT}\ndata: ${JSON.stringify({ closesInMs: Math.max(0, Math.round(closesInMs)) })}\n\n`;
}

/** The window an OWNER_WINDOW_EVENT announced, or null for anything malformed or out of range. */
export function parseOwnerWindow(data: unknown, reverifyMs: number = OWNER_STREAM_REVERIFY_MS): number | null {
  if (typeof data !== 'string') return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return null;
  }
  const ms = (parsed as { closesInMs?: unknown } | null)?.closesInMs;
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0 || ms > reverifyMs) return null;
  return ms;
}

/** A daemon resume point from the browser: the Last-Event-ID header EventSource sends on its own
 *  reconnect, else the handover's query parameter. The query value is accepted only as a daemon
 *  seq (decimal digits), so nothing else reaches the daemon's header. */
export function ownerStreamResumeId(req: Request): string | null {
  const header = req.headers.get('last-event-id');
  if (header) return header;
  const param = new URL(req.url).searchParams.get(OWNER_RESUME_PARAM);
  return param && /^[0-9]{1,19}$/.test(param) ? param : null;
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
  /** Sent before anything from `source`, such as the OWNER_WINDOW_EVENT frame. */
  preamble?: Uint8Array,
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
      if (preamble) c.enqueue(preamble);
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
