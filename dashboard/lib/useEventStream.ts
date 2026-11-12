'use client';

import { useEffect, useRef, useState } from 'react';
import { OWNER_SESSION_EVENT, readOwnerSession } from './ownerSessionClient';
import type { StreamMessage } from './types';

/** 'signed-out': a daemon is connected but this visitor has no owner session, so the stream
 *  route answered 401 (or owner access is not configured). Pages show the sign-in prompt. */
export type StreamStatus = 'connecting' | 'live' | 'reconnecting' | 'signed-out';

/**
 * Resilient SSE client (review: PR #8 HIGH).
 *
 * EventSource auto-reconnects on transient drops (dev-server reload, network blip), so we
 * never close it from onerror - we only surface a "reconnecting" status. If the browser
 * gives up entirely (readyState CLOSED), we recreate the source with a short backoff, so
 * the dashboard can never silently freeze while claiming to be live. One malformed frame
 * is skipped rather than crashing the page.
 *
 * EventSource cannot see a status code, so when the source closes the hook asks
 * /api/owner/session whether the cause is a missing owner session. If it is, it stops retrying
 * and reports 'signed-out' until a sign-in is announced (OWNER_SESSION_EVENT), then reconnects.
 */
export function useEventStream(url: string, onMessage: (msg: StreamMessage) => void): StreamStatus {
  const [status, setStatus] = useState<StreamStatus>('connecting');
  const handler = useRef(onMessage);
  handler.current = onMessage;

  useEffect(() => {
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | null = null;
    let disposed = false;

    const connect = () => {
      if (disposed) return;
      es?.close();
      const source = new EventSource(url);
      es = source;
      source.onopen = () => setStatus('live');
      source.onmessage = (m) => {
        let msg: StreamMessage;
        try {
          msg = JSON.parse(m.data) as StreamMessage;
        } catch {
          return; // bad frame: skip it, keep the stream alive
        }
        handler.current(msg);
      };
      source.onerror = () => {
        if (es !== source) return; // superseded by a newer connection
        setStatus('reconnecting');
        if (source.readyState === EventSource.CLOSED) {
          source.close();
          void readOwnerSession().then((session) => {
            if (disposed || es !== source) return;
            if (session?.ownerApi && !session.signedIn) {
              setStatus('signed-out');
              return; // resumed by the OWNER_SESSION_EVENT listener below
            }
            retry = setTimeout(connect, 2000);
          });
        }
      };
    };

    const onSessionChange = () => {
      if (retry) clearTimeout(retry);
      setStatus('connecting');
      connect();
    };

    connect();
    window.addEventListener(OWNER_SESSION_EVENT, onSessionChange);
    return () => {
      disposed = true;
      window.removeEventListener(OWNER_SESSION_EVENT, onSessionChange);
      if (retry) clearTimeout(retry);
      es?.close();
    };
  }, [url]);

  return status;
}
