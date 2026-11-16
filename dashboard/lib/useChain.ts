'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ChainHistory } from './chainHistory';
import type { FloatSnapshot } from './types';

/** A polled public read: the last good value, its error if the latest refresh failed, and a
 *  manual refresh. A failed refresh keeps the last value on screen rather than blanking it. */
export interface Polled<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  updatedAt: number | null;
  refresh: () => void;
}

function usePolled<T>(url: string, everyMs: number): Polled<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const alive = useRef(true);

  const load = useCallback(async () => {
    try {
      const res = await fetch(url, { cache: 'no-store' });
      const body = (await res.json().catch(() => null)) as (T & { error?: unknown; stale?: boolean }) | null;
      if (!alive.current) return;
      if (!res.ok || !body) {
        const e = body?.error as { message?: string } | string | undefined;
        setError(typeof e === 'string' ? e : e?.message ?? `HTTP ${res.status}`);
      } else {
        setData(body);
        setUpdatedAt(Date.now());
        setError(body.stale && typeof body.error === 'string' ? body.error : null);
      }
    } catch (e) {
      if (alive.current) setError(e instanceof Error ? e.message : 'network error');
    } finally {
      if (alive.current) setLoading(false);
    }
  }, [url]);

  useEffect(() => {
    alive.current = true;
    void load();
    const t = setInterval(load, everyMs);
    return () => {
      alive.current = false;
      clearInterval(t);
    };
  }, [load, everyMs]);

  return { data, error, loading, updatedAt, refresh: () => void load() };
}

/** Jobs, activity and totals decoded from the contracts' events (app/api/chain/history). */
export function useChainHistory(): Polled<ChainHistory> {
  return usePolled<ChainHistory>('/api/chain/history', 20_000);
}

/** The pool's current state, read from FloatPool by eth_call (app/api/float). */
export function useFloat(): Polled<FloatSnapshot> {
  return usePolled<FloatSnapshot>('/api/float', 15_000);
}
