/**
 * Token usage distribution exposed by the v8 management API.
 *
 * The backend aggregates proxy runtime usage records in memory and returns the
 * input/cache/output/reasoning token split overall and per credential.
 */

import { apiClient } from './client';

const TOKEN_USAGE_TIMEOUT_MS = 15 * 1000;

/** Raw, un-normalized snapshot; see `@/features/quota/tokenUsage` for parsing. */
export interface TokenUsageApiSnapshot {
  requests?: unknown;
  breakdown?: unknown;
  providers?: unknown;
  credentials?: unknown;
}

export const tokenUsageApi = {
  get: () =>
    apiClient.get<TokenUsageApiSnapshot>('/observability/usage/tokens', {
      timeout: TOKEN_USAGE_TIMEOUT_MS,
    }),
};
