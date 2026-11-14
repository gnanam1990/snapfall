'use client';

import { useEffect, useRef, useState } from 'react';
import { OWNER_SESSION_EVENT, listenForOwnerSessionChanges, readOwnerSession } from './ownerSessionClient';
import { createStreamClient, type StreamStatus } from './streamClient';
import type { StreamMessage } from './types';

export type { StreamStatus } from './streamClient';

/**
 * Subscribes a page to an SSE stream and reports its status (review: PR #8 HIGH).
 *
 * The connection logic is lib/streamClient (createStreamClient): reconnects, the owner-session
 * check when the browser gives up, the 'unverified' state in which pages hide owner data
 * (ownerDataHidden), and the overlapping handover that loses no chain event when the owner stream
 * route closes a stream to re-check the session.
 *
 * Any session change, in this tab or another (lib/ownerSessionClient relays them), closes the open
 * source and reconnects through the owner guard. After a sign-out anywhere that reconnect is
 * refused, so the stream stops at once instead of running on with the old session.
 *
 * `onStatusNow` hears every status as the client reports it, before React renders it: for code
 * that must act on a status change at once rather than after the next commit (lib/ownerActionGate).
 */
export function useEventStream(
  url: string,
  onMessage: (msg: StreamMessage) => void,
  onStatusNow?: (status: StreamStatus) => void,
): StreamStatus {
  const [status, setStatus] = useState<StreamStatus>('connecting');
  const handler = useRef(onMessage);
  handler.current = onMessage;
  const statusHandler = useRef(onStatusNow);
  statusHandler.current = onStatusNow;

  useEffect(() => {
    const client = createStreamClient({
      url,
      onMessage: (msg) => handler.current(msg),
      onStatus: (next) => {
        statusHandler.current?.(next);
        setStatus(next);
      },
      open: (u) => new EventSource(u),
      readSession: readOwnerSession,
    });
    const onSessionChange = () => client.sessionChanged();
    listenForOwnerSessionChanges();
    window.addEventListener(OWNER_SESSION_EVENT, onSessionChange);
    return () => {
      window.removeEventListener(OWNER_SESSION_EVENT, onSessionChange);
      client.dispose();
    };
  }, [url]);

  return status;
}
