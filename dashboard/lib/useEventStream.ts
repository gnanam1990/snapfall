'use client';

import { useEffect, useRef, useState } from 'react';
import type { StreamMessage } from './types';

export type StreamStatus = 'connecting' | 'live' | 'reconnecting';

/**
 * Resilient SSE client (review: PR #8 HIGH).
 *
 * EventSource auto-reconnects on transient drops (dev-server reload, network blip), so we
 * never close it from onerror - we only surface a "reconnecting" status. If the browser
 * gives up entirely (readyState CLOSED), we recreate the source with a short backoff, so
 * the dashboard can never silently freeze while claiming to be live. One malformed frame
 * is skipped rather than crashing the page.
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
      es = new EventSource(url);
      es.onopen = () => setStatus('live');
      es.onmessage = (m) => {
        let msg: StreamMessage;
        try {
          msg = JSON.parse(m.data) as StreamMessage;
        } catch {
          return; // bad frame: skip it, keep the stream alive
        }
        handler.current(msg);
      };
      es.onerror = () => {
        setStatus('reconnecting');
        if (es && es.readyState === EventSource.CLOSED) {
          es.close();
          retry = setTimeout(connect, 2000);
        }
      };
    };

    connect();
    return () => {
      disposed = true;
      if (retry) clearTimeout(retry);
      es?.close();
    };
  }, [url]);

  return status;
}
