import { number } from './hooks';
import type { UsageEntry } from './types';

export type UsageStatusKind = 'ok' | 'fail' | 'interrupted';

export const statusKind = (status: number, failed: boolean): UsageStatusKind =>
  status === 499 ? 'interrupted' : failed || status >= 400 ? 'fail' : 'ok';

export const entryStatus = (entry: Pick<UsageEntry, 'status' | 'failed'>): UsageStatusKind =>
  statusKind(entry.status, entry.failed);

// Upstream error bodies are usually JSON envelopes; surface the human message
// and keep the raw body available separately.
export function errorMessage(body: string | undefined): string {
  const text = (body || '').trim();
  if (!text) return '';
  const pick = (value: unknown): string => {
    if (typeof value === 'string') return value;
    if (!value || typeof value !== 'object') return '';
    const record = value as Record<string, unknown>;
    return pick(record.message) || pick(record.error) || pick(record.detail);
  };
  try {
    const message = pick(JSON.parse(text)).trim();
    if (message) return message;
  } catch {
    // Not JSON; fall back to the raw text.
  }
  return text;
}

// Bare base-model calls (keys with even load balancing) are served by one
// channel's pinned model ID; surface it next to the requested name.
export const servingModelId = (entry: Partial<Pick<UsageEntry, 'model' | 'channel'>>): string =>
  entry.model && entry.channel && !entry.model.startsWith(`${entry.channel}/`)
    ? `${entry.channel}/${entry.model}`
    : '';

// Channel-mounted credential files are named "<channel>-<type>#<n>-<internal>.json";
// the pinned prefix alone identifies the credential, the rest is internal.
export const credentialDisplayName = (name: string) =>
  name.replace(/\.json$/i, '').replace(/^(.+#\d+)-[^#]+$/, '$1');

export const tokensPerSecond = (entry: UsageEntry) =>
  entry.tokens_known && entry.latency_ms > 0 && entry.output_tokens > 0
    ? Math.round(entry.output_tokens / (entry.latency_ms / 1000))
    : null;

// Cache hit rate: the share of total input tokens served from the upstream cache.
export const cacheHitRate = (
  entry: Pick<UsageEntry, 'input_tokens' | 'cache_read_tokens'>
): number | null =>
  entry.input_tokens > 0 && entry.cache_read_tokens > 0
    ? (entry.cache_read_tokens / entry.input_tokens) * 100
    : null;

/** `45,268（44,416） / 430（304）`: cache and reasoning are bare-number subsets. */
export const tokenLine = (entry: UsageEntry) =>
  `${number(entry.input_tokens)}${
    entry.cache_read_tokens > 0 ? `（${number(entry.cache_read_tokens)}）` : ''
  } / ${number(entry.output_tokens)}${
    entry.reasoning_tokens > 0 ? `（${number(entry.reasoning_tokens)}）` : ''
  }`;

export const percent = (value: number | null, digits = 1) =>
  value === null ? '—' : `${value.toFixed(digits)}%`;
