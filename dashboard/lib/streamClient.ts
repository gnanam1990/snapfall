/**
 * The owner event stream's client: every connection decision, kept out of the React hook
 * (lib/useEventStream, which only wires it to a page) so it can be tested without a browser.
 */

import { OWNER_RESUME_PARAM, OWNER_STREAM_HANDOVER_LEAD_MS, OWNER_WINDOW_EVENT, parseOwnerWindow } from './ownerStream';
import type { OwnerSessionStatus } from './ownerSessionClient';
import type { StreamMessage } from './types';

/**
 * - 'connecting': the first connection has not opened yet.
 * - 'live': the stream is open, so its connection passed the owner guard.
 * - 'reconnecting': the stream dropped after the session was verified; EventSource is retrying.
 * - 'unverified': the session changed (a sign-in or sign-out here or in another tab), or the stream
 *   closed and /api/owner/session could not be read, so nobody knows whether this visitor is the
 *   owner. Reconnection continues, and owner data stays hidden until the stream is live again.
 * - 'signed-out': a daemon is connected but this visitor has no owner session, so the stream route
 *   answered 401 (or owner access is not configured). Pages show the sign-in prompt.
 */
export type StreamStatus = 'connecting' | 'live' | 'reconnecting' | 'unverified' | 'signed-out';

/** Whether a page must keep what the stream delivered under an owner session off the screen. */
export function ownerDataHidden(status: StreamStatus): boolean {
  return status === 'signed-out' || status === 'unverified';
}

/** What a stream that closed for good means, given what /api/owner/session said (null: unreadable). */
export function statusAfterClosed(session: OwnerSessionStatus | null): 'signed-out' | 'unverified' | 'reconnecting' {
  if (session === null) return 'unverified';
  if (session.ownerApi && !session.signedIn) return 'signed-out';
  return 'reconnecting';
}

/** How long to wait, after a window of `closesInMs` was announced, before opening the replacement. */
export function handoverDelay(closesInMs: number, leadMs: number = OWNER_STREAM_HANDOVER_LEAD_MS): number {
  return Math.max(0, closesInMs - leadMs);
}

/** The stream URL for a handover connection that resumes daemon events after `lastEventId`. */
export function resumeUrl(base: string, lastEventId: string): string {
  return `${base}${base.includes('?') ? '&' : '?'}${OWNER_RESUME_PARAM}=${encodeURIComponent(lastEventId)}`;
}

/** The identity of a frame both overlapping connections may deliver, or null for one that is never
 *  deduplicated (snapshots, the local demo replay, frames without a sequence). */
export function frameKey(msg: StreamMessage): string | null {
  if (msg.kind !== 'event' || msg.demo === true) return null;
  const seq = msg.seq as unknown;
  if ((typeof seq !== 'number' && typeof seq !== 'string') || seq === '') return null;
  return `${msg.source}:${String(seq)}`;
}

/**
 * Drops a frame already delivered, so the overlap of a handover (two connections delivering the
 * same chain and daemon events for a few seconds) reaches a page once. Remembers the last `cap`
 * keys: the overlap is seconds long, and an unbounded set would grow for as long as a tab is open.
 */
export function createFrameDedup(cap = 1024): (msg: StreamMessage) => boolean {
  const seen = new Set<string>();
  return (msg) => {
    const key = frameKey(msg);
    if (key === null) return true;
    if (seen.has(key)) return false;
    seen.add(key);
    if (seen.size > cap) {
      const oldest = seen.values().next().value;
      if (oldest !== undefined) seen.delete(oldest);
    }
    return true;
  };
}

/** The part of EventSource the client uses, so a test can drive a fake one. */
export interface EventSourceLike {
  readonly readyState: number;
  onopen: ((ev: Event) => unknown) | null;
  onmessage: ((ev: MessageEvent) => unknown) | null;
  onerror: ((ev: Event) => unknown) | null;
  addEventListener(type: string, listener: (ev: Event) => void): void;
  close(): void;
}

/** EventSource.CLOSED: the browser has given up on the connection and will not retry it. */
const CLOSED = 2;

/** Pause before a fresh connection replaces one the browser gave up on. */
export const STREAM_RETRY_MS = 2000;
/** How long a session read may take before it counts as unreadable ('unverified'). */
export const SESSION_READ_TIMEOUT_MS = 5000;

export interface StreamClientOptions {
  url: string;
  onMessage: (msg: StreamMessage) => void;
  onStatus: (status: StreamStatus) => void;
  open: (url: string) => EventSourceLike;
  readSession: () => Promise<OwnerSessionStatus | null>;
  setTimer?: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
  clearTimer?: (timer: ReturnType<typeof setTimeout>) => void;
}

/**
 * One page's subscription to the event stream.
 *
 * EventSource auto-reconnects on transient drops (dev-server reload, network blip), so a source is
 * never closed from onerror: the status says "reconnecting". If the browser gives up entirely
 * (readyState CLOSED), the client asks /api/owner/session why. No owner session: 'signed-out',
 * and it waits for a session change. The check failed: 'unverified', and it retries. Otherwise it
 * retries as 'reconnecting'. One malformed frame is skipped rather than crashing the page.
 *
 * `sessionChanged()` (a sign-in or sign-out in this tab or another) drops the connection, reports
 * 'unverified', and reconnects through the owner guard, which refuses it after a sign-out.
 *
 * HANDOVER. The owner stream route closes a healthy stream every few minutes so the session is
 * checked again, and announces the close first (OWNER_WINDOW_EVENT). A plain reconnect would lose
 * chain events: the daemon starts every new connection at its latest chain cursor and Last-Event-ID
 * resumes only daemon events. So shortly before the close the client opens the replacement,
 * resuming daemon events after the last id it saw, and keeps the old connection until its own
 * window ends. Chain events indexed after the new connection's cursor arrive on it, earlier ones
 * on the old one, and what both deliver reaches the page once.
 */
export function createStreamClient(options: StreamClientOptions): { sessionChanged(): void; dispose(): void } {
  const { url, onMessage, onStatus, open, readSession } = options;
  const setTimer = options.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = options.clearTimer ?? ((t) => clearTimeout(t));

  let es: EventSourceLike | null = null;
  /** The connection being handed over from: it delivers until its own window ends. */
  let draining: EventSourceLike | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let handover: ReturnType<typeof setTimeout> | null = null;
  let disposed = false;
  /** The last daemon SSE id delivered: the resume point for a handover. */
  let lastEventId: string | null = null;
  let admit = createFrameDedup();
  /** Whether a connection has passed the owner guard since the last session change. Until one has,
   *  a drop must not read as 'reconnecting', which shows owner data again. */
  let verified = false;

  /** The session read, bounded: a read that does not answer counts as unreadable ('unverified'). */
  const readSessionBounded = (): Promise<OwnerSessionStatus | null> =>
    new Promise((resolve) => {
      let settled = false;
      const timer = setTimer(() => {
        if (settled) return;
        settled = true;
        resolve(null);
      }, SESSION_READ_TIMEOUT_MS);
      readSession().then(
        (session) => {
          if (settled) return;
          settled = true;
          clearTimer(timer);
          resolve(session);
        },
        () => {
          if (settled) return;
          settled = true;
          clearTimer(timer);
          resolve(null);
        },
      );
    });

  const closeDraining = () => {
    draining?.close();
    draining = null;
  };
  const clearTimers = () => {
    if (retry !== null) clearTimer(retry);
    if (handover !== null) clearTimer(handover);
    retry = null;
    handover = null;
  };

  /** 'fresh' starts over, as on first load: the daemon replays its events from the start. 'handover'
   *  keeps the current connection open while the new one resumes after the last daemon id. */
  const connect = (mode: 'fresh' | 'handover') => {
    if (disposed) return;
    closeDraining();
    if (mode === 'handover') {
      draining = es;
    } else {
      es?.close();
      lastEventId = null;
      admit = createFrameDedup();
    }
    const source = open(mode === 'handover' && lastEventId ? resumeUrl(url, lastEventId) : url);
    es = source;
    source.onopen = () => {
      if (es !== source) return;
      verified = true;
      onStatus('live');
    };
    source.onmessage = (m) => {
      let msg: StreamMessage;
      try {
        msg = JSON.parse(m.data) as StreamMessage;
      } catch {
        return; // bad frame: skip it, keep the stream alive
      }
      if (!admit(msg)) return; // already delivered by the other connection of a handover
      if (msg.kind === 'event' && msg.source === 'daemon' && m.lastEventId) lastEventId = m.lastEventId;
      onMessage(msg);
    };
    source.addEventListener(OWNER_WINDOW_EVENT, (e) => {
      if (es !== source) return;
      const closesIn = parseOwnerWindow((e as MessageEvent).data);
      if (closesIn === null) return;
      if (handover !== null) clearTimer(handover);
      handover = setTimer(() => {
        handover = null;
        if (es === source) connect('handover');
      }, handoverDelay(closesIn));
    });
    source.onerror = () => {
      if (source === draining) {
        // Its window ended (or it dropped) after its replacement was opened: let it go rather than
        // have EventSource reconnect it alongside the replacement.
        closeDraining();
        return;
      }
      if (es !== source) return; // superseded by a newer connection
      // Only a session this client has seen pass the guard may ride out a drop with its data shown.
      onStatus(verified ? 'reconnecting' : 'unverified');
      if (source.readyState !== CLOSED) return;
      source.close();
      // A handover the guard refused: the old connection must not go on delivering into a page
      // that is about to show the sign-in prompt.
      closeDraining();
      void readSessionBounded().then((session) => {
        if (disposed || es !== source) return;
        const next = statusAfterClosed(session);
        if (next !== 'reconnecting') verified = false;
        onStatus(next);
        if (next === 'signed-out') return; // resumed by sessionChanged()
        retry = setTimer(() => {
          retry = null;
          connect('fresh');
        }, STREAM_RETRY_MS);
      });
    };
  };

  connect('fresh');
  return {
    sessionChanged() {
      if (disposed) return;
      clearTimers();
      // Whatever was shown belonged to the session before this change; until a connection passes
      // the guard with the new one, nobody has shown this visitor is the owner.
      verified = false;
      onStatus('unverified');
      connect('fresh');
    },
    dispose() {
      disposed = true;
      clearTimers();
      es?.close();
      closeDraining();
    },
  };
}
