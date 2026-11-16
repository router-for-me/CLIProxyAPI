/**
 * Loads the recorded quota history for the pool cards.
 *
 * Fetches once per refresh token (and again only for an earlier `sinceMs`),
 * never polls, keeps the last result while refetching, and drops responses
 * that a newer request or a new connection session has superseded.
 */

import { useEffect, useRef, useState } from 'react';
import { quotaHistoryApi, type QuotaHistoryResult } from '@/services/api/quotaHistory';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { MINUTE_MS } from '@/utils/time/durations';

export interface PoolHistoryState {
  /** Date.now() when the last result committed. */
  fetchedAt: number | null;
  loadedSinceMs: number | null;
  loading: boolean;
  /** 'ok' results have point times shifted onto the browser clock. */
  result: QuotaHistoryResult | null;
  /** serverNow - fetchedAt when larger than a minute either way, else 0. */
  skewMs: number;
}

export interface PoolHistoryArgs {
  /** Connected, list settled for this session, and at least one pool-provider file. */
  enabled: boolean;
  /** Bumped by Refresh all and by the header refresh. */
  refreshToken: number;
  /** useQuotaStore cacheGeneration at render. */
  sessionGeneration: number;
  sinceMs: number;
}

export interface HistoryRequestKey {
  generation: number;
  sinceMs: number;
  token: number;
}

interface CommittedHistory extends Omit<PoolHistoryState, 'loading'> {
  generation: number;
}

/** Fetch once per (token, session) and again only when the range needs an earlier start. */
export const shouldFetchHistory = (
  enabled: boolean,
  request: HistoryRequestKey,
  lastRequest: HistoryRequestKey | null
): boolean => {
  if (!enabled) return false;
  if (lastRequest === null) return true;

  return (
    request.token !== lastRequest.token ||
    request.generation !== lastRequest.generation ||
    request.sinceMs < lastRequest.sinceMs
  );
};

export const historySkewMs = (serverNow: number, fetchedAt: number): number => {
  const skew = serverNow - fetchedAt;

  return Math.abs(skew) > MINUTE_MS ? skew : 0;
};

/** Moves every point of an 'ok' result from the server clock onto the browser clock. */
export const shiftHistoryResult = (
  result: QuotaHistoryResult,
  skewMs: number
): QuotaHistoryResult => {
  if (result.status !== 'ok' || skewMs === 0) return result;

  return {
    ...result,
    credentials: result.credentials.map((credential) => ({
      ...credential,
      windows: credential.windows.map((window) => ({
        ...window,
        points: window.points.map((point) => ({
          ...point,
          resetAt: point.resetAt === null ? null : point.resetAt - skewMs,
          t: point.t - skewMs,
        })),
      })),
    })),
  };
};

export const usePoolHistory = ({
  enabled,
  refreshToken,
  sessionGeneration,
  sinceMs,
}: PoolHistoryArgs): PoolHistoryState => {
  const [committed, setCommitted] = useState<CommittedHistory | null>(null);
  const [loading, setLoading] = useState(false);
  const requestIdRef = useRef(0);
  const lastRequestRef = useRef<HistoryRequestKey | null>(null);

  useEffect(() => {
    const request = { generation: sessionGeneration, sinceMs, token: refreshToken };

    if (!shouldFetchHistory(enabled, request, lastRequestRef.current)) return;

    const requestId = ++requestIdRef.current;

    lastRequestRef.current = request;
    setLoading(true);

    void quotaHistoryApi.get({ sinceMs }).then((result) => {
      if (requestId !== requestIdRef.current) return;

      setLoading(false);

      if (sessionGeneration !== useQuotaStore.getState().cacheGeneration) return;

      const fetchedAt = Date.now();
      const skewMs = result.status === 'ok' ? historySkewMs(result.serverNow, fetchedAt) : 0;

      setCommitted({
        fetchedAt,
        generation: sessionGeneration,
        loadedSinceMs: sinceMs,
        result: shiftHistoryResult(result, skewMs),
        skewMs,
      });
    });
  }, [enabled, refreshToken, sessionGeneration, sinceMs]);

  useEffect(
    () => () => {
      requestIdRef.current += 1;
      lastRequestRef.current = null;
    },
    []
  );

  if (committed === null || committed.generation !== sessionGeneration) {
    return { fetchedAt: null, loadedSinceMs: null, loading, result: null, skewMs: 0 };
  }

  const { fetchedAt, loadedSinceMs, result, skewMs } = committed;

  return { fetchedAt, loadedSinceMs, loading, result, skewMs };
};
