/**
 * Shared token usage index.
 *
 * Every quota card wants the same `/observability/usage/tokens` snapshot, so the
 * fetch is coalesced across mounts: a short TTL cache plus a shared in-flight
 * request keep a full grid of cards from issuing one request each.
 */

import { useEffect, useState } from 'react';
import { tokenUsageApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import {
  buildTokenUsageIndex,
  EMPTY_TOKEN_USAGE_INDEX,
  type TokenUsageIndex,
} from '../tokenUsage';

const CACHE_TTL_MS = 15 * 1000;

let cached: { apiBase: string; index: TokenUsageIndex; fetchedAt: number } | null = null;
let inflight: { apiBase: string; promise: Promise<TokenUsageIndex> } | null = null;

function freshIndexFor(apiBase: string): TokenUsageIndex | null {
  if (!cached || cached.apiBase !== apiBase) return null;
  if (Date.now() - cached.fetchedAt > CACHE_TTL_MS) return null;
  return cached.index;
}

function ensureTokenUsage(apiBase: string): Promise<TokenUsageIndex> {
  const fresh = freshIndexFor(apiBase);
  if (fresh) return Promise.resolve(fresh);
  if (inflight && inflight.apiBase === apiBase) return inflight.promise;

  const promise = tokenUsageApi
    .get()
    .then((snapshot) => {
      const index = buildTokenUsageIndex(snapshot);
      cached = { apiBase, index, fetchedAt: Date.now() };
      return index;
    })
    .catch(() => freshIndexFor(apiBase) ?? EMPTY_TOKEN_USAGE_INDEX);

  inflight = { apiBase, promise };
  void promise.finally(() => {
    if (inflight && inflight.promise === promise) inflight = null;
  });
  return promise;
}

export function useTokenUsage(): TokenUsageIndex {
  const apiBase = useAuthStore((state) => state.apiBase);
  const connected = useAuthStore((state) => state.connectionStatus) === 'connected';
  const [index, setIndex] = useState<TokenUsageIndex>(
    () => freshIndexFor(apiBase) ?? EMPTY_TOKEN_USAGE_INDEX
  );

  useEffect(() => {
    if (!connected || !apiBase) {
      setIndex(EMPTY_TOKEN_USAGE_INDEX);
      return;
    }

    let active = true;
    setIndex(freshIndexFor(apiBase) ?? EMPTY_TOKEN_USAGE_INDEX);
    void ensureTokenUsage(apiBase).then((value) => {
      if (active) setIndex(value);
    });
    return () => {
      active = false;
    };
  }, [apiBase, connected]);

  return index;
}
