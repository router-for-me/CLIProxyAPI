/**
 * prox JSON API (/prox/api/*). Same server and management key as the rest of
 * the panel, but outside the /v8/management prefix the shared client adds.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuthStore } from '@/stores';
import { normalizeApiBase } from '@/utils/connection';
import type { PoolCharts } from './types';

export const PROX_API_PREFIX = '/prox/api';

export async function proxFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const { apiBase, managementKey } = useAuthStore.getState();
  const base = normalizeApiBase(apiBase || window.location.origin);
  const res = await fetch(`${base}${PROX_API_PREFIX}${path}`, {
    ...init,
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(managementKey ? { Authorization: `Bearer ${managementKey}` } : {}),
      ...init?.headers,
    },
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      message = body?.detail || body?.error || body?.message || message;
    } catch {
      // keep status text
    }
    throw new Error(message);
  }
  return (await res.json()) as T;
}

export const proxPost = <T>(path: string, body: unknown = {}) =>
  proxFetch<T>(path, { method: 'POST', body: JSON.stringify(body) });

/** Polls a prox endpoint; `error` is set when prox is disabled or unreachable. */
export function useProx<T>(path: string | null, intervalMs = 30_000) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(Boolean(path));
  const seq = useRef(0);

  const load = useCallback(async () => {
    if (!path) return;
    const id = ++seq.current;
    try {
      const next = await proxFetch<T>(path);
      if (id !== seq.current) return;
      setData(next);
      setError('');
    } catch (err) {
      if (id !== seq.current) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (id === seq.current) setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    setLoading(Boolean(path));
    void load();
    if (!path || intervalMs <= 0) return;
    const timer = window.setInterval(() => void load(), intervalMs);
    return () => {
      window.clearInterval(timer);
      seq.current += 1;
    };
  }, [load, path, intervalMs]);

  return { data, error, loading, reload: load };
}

/** Pool charts for one window kind, refreshed every minute. */
export const usePoolCharts = (window: string) =>
  useProx<PoolCharts>(`/pool/chart?window=${window}`, 60_000);
