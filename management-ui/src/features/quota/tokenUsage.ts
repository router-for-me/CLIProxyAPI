/**
 * Token cost distribution: normalization and credential matching.
 *
 * The backend returns a canonical, non-overlapping breakdown. This module turns
 * the untrusted payload into numbers and indexes it so a quota card can look up
 * the distribution for its own credential.
 */

import { isRecord } from '@/utils/helpers';
import type { AuthFileItem } from '@/types';

export interface TokenCostBreakdown {
  inputUncached: number;
  inputCacheRead: number;
  inputCacheWrite: number;
  outputNonReasoning: number;
  outputReasoning: number;
  total: number;
  requests: number;
}

export interface TokenUsageIndex {
  overall: TokenCostBreakdown | null;
  lookup: (file: AuthFileItem) => TokenCostBreakdown | undefined;
}

export const EMPTY_TOKEN_USAGE_INDEX: TokenUsageIndex = {
  overall: null,
  lookup: () => undefined,
};

const toCount = (value: unknown): number => {
  const numberValue = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(numberValue) && numberValue > 0 ? numberValue : 0;
};

const toRecord = (value: unknown): Record<string, unknown> => (isRecord(value) ? value : {});

/** Normalize one backend breakdown (with optional request count) into a display model. */
export function normalizeTokenCostBreakdown(raw: unknown, requests: unknown): TokenCostBreakdown {
  const source = toRecord(raw);
  const input = toRecord(source.input);
  const output = toRecord(source.output);

  const inputUncached = toCount(input.uncached_tokens);
  const inputCacheRead = toCount(input.cache_read_tokens);
  const inputCacheWrite = toCount(input.cache_write_tokens);
  const outputNonReasoning = toCount(output.non_reasoning_tokens);
  const outputReasoning = toCount(output.reasoning_tokens);

  const inputTotal = Math.max(toCount(input.total_tokens), inputUncached + inputCacheRead + inputCacheWrite);
  const outputTotal = Math.max(toCount(output.total_tokens), outputNonReasoning + outputReasoning);
  const total = Math.max(toCount(source.total_tokens), inputTotal + outputTotal);

  return {
    inputUncached,
    inputCacheRead,
    inputCacheWrite,
    outputNonReasoning,
    outputReasoning,
    total,
    requests: toCount(requests),
  };
}

export const hasTokenCostData = (breakdown: TokenCostBreakdown | undefined): boolean =>
  Boolean(breakdown && breakdown.total > 0);

/**
 * Build the lookup index from a raw `/observability/usage/tokens` snapshot.
 * Credentials are keyed by auth_index first (the precise runtime identity) and
 * by auth_id second, so a card can match either identifier it has available.
 */
export function buildTokenUsageIndex(snapshot: unknown): TokenUsageIndex {
  const source = toRecord(snapshot);
  const overallBreakdown = normalizeTokenCostBreakdown(source.breakdown, source.requests);
  const overall = hasTokenCostData(overallBreakdown) ? overallBreakdown : null;

  const byAuthIndex = new Map<string, TokenCostBreakdown>();
  const byAuthID = new Map<string, TokenCostBreakdown>();

  const credentials = Array.isArray(source.credentials) ? source.credentials : [];
  credentials.forEach((entry) => {
    const credential = toRecord(entry);
    const breakdown = normalizeTokenCostBreakdown(credential.breakdown, credential.requests);
    if (!hasTokenCostData(breakdown)) return;

    const authIndex = typeof credential.auth_index === 'string' ? credential.auth_index.trim() : '';
    if (authIndex && !byAuthIndex.has(authIndex)) {
      byAuthIndex.set(authIndex, breakdown);
    }
    const authID = typeof credential.auth_id === 'string' ? credential.auth_id.trim() : '';
    if (authID && !byAuthID.has(authID)) {
      byAuthID.set(authID, breakdown);
    }
  });

  const lookup = (file: AuthFileItem): TokenCostBreakdown | undefined => {
    const authIndex = file.authIndex;
    if (authIndex !== undefined && authIndex !== null) {
      const match = byAuthIndex.get(String(authIndex).trim());
      if (match) return match;
    }
    const id = typeof file.id === 'string' ? file.id.trim() : '';
    if (id) {
      const match = byAuthID.get(id);
      if (match) return match;
    }
    const name = typeof file.name === 'string' ? file.name.trim() : '';
    if (name) {
      const match = byAuthID.get(name);
      if (match) return match;
    }
    return undefined;
  };

  return { overall, lookup };
}
