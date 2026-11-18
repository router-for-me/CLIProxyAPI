/**
 * Per-credential prompt-cache counters from papa-proxy's
 * GET /observability/cache, keyed by credential auth index.
 */

import { apiClient } from './client';

export interface CacheTokens {
  requests: number;
  uncachedInputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
}

export interface CacheStats {
  windowSeconds: number;
  byAuthIndex: Record<string, { recent: CacheTokens; total: CacheTokens }>;
}

const num = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : 0;

const tokens = (raw: unknown): CacheTokens => {
  const record = (raw ?? {}) as Record<string, unknown>;
  return {
    requests: num(record.requests),
    uncachedInputTokens: num(record.uncached_input_tokens),
    cacheReadTokens: num(record.cache_read_tokens),
    cacheWriteTokens: num(record.cache_write_tokens),
  };
};

export const cacheStatsApi = {
  get: async (): Promise<CacheStats> => {
    const response = await apiClient.get<Record<string, unknown>>('/observability/cache');
    const credentials = (response?.credentials ?? {}) as Record<string, Record<string, unknown>>;
    const byAuthIndex: CacheStats['byAuthIndex'] = {};
    for (const [authIndex, value] of Object.entries(credentials)) {
      byAuthIndex[authIndex] = { recent: tokens(value?.recent), total: tokens(value?.total) };
    }
    return { windowSeconds: num(response?.window_seconds), byAuthIndex };
  },
};
