import type { Summary, SummaryBucket } from './types';

/** Summary dimensions the usage page can rank by. */
export type UsageDimension = 'model' | 'consumer' | 'credential';

export interface UsageRankEntry {
  /** Grouping value: model ID, consumer username or credential ID. */
  key: string;
  /** Display name: the credential's pinned prefix; otherwise the key. */
  label: string;
  executions: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  total_tokens: number;
  unknown_token_records: number;
  latency_ms: number;
  latency_count: number;
}

const emptyRank = (key: string, label: string): UsageRankEntry => ({
  key,
  label,
  executions: 0,
  failures: 0,
  input_tokens: 0,
  output_tokens: 0,
  reasoning_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 0,
  unknown_token_records: 0,
  latency_ms: 0,
  latency_count: 0,
});

/**
 * Fold day/consumer/owner/channel/model summary groups into one dimension.
 * Only additive fields are summed: per-group `calls` count distinct traces,
 * which are not additive across groups, so they are deliberately excluded.
 */
export function rankUsage(groups: Summary[], dimension: UsageDimension): UsageRankEntry[] {
  const byKey = new Map<string, UsageRankEntry>();
  for (const group of groups) {
    const key = (dimension === 'credential' ? group.credential_id : group[dimension]) ?? '';
    let entry = byKey.get(key);
    if (!entry) {
      entry = emptyRank(key, dimension === 'credential' ? group.channel || key : key);
      byKey.set(key, entry);
    }
    entry.executions += group.executions;
    entry.failures += group.failures;
    entry.input_tokens += group.input_tokens;
    entry.output_tokens += group.output_tokens;
    entry.reasoning_tokens += group.reasoning_tokens;
    entry.cache_read_tokens += group.cache_read_tokens;
    entry.total_tokens += group.total_tokens;
    entry.unknown_token_records += group.unknown_token_records;
    entry.latency_ms += group.latency_ms ?? 0;
    entry.latency_count += group.latency_count ?? 0;
  }
  return [...byKey.values()].sort(
    (a, b) =>
      b.total_tokens - a.total_tokens ||
      b.executions - a.executions ||
      a.label.localeCompare(b.label)
  );
}

/** Ranking value used for bar length: Tokens, or calls when no Token data exists. */
export const rankMetric = (entries: UsageRankEntry[]): 'tokens' | 'calls' =>
  entries.some((entry) => entry.total_tokens > 0) ? 'tokens' : 'calls';

export interface TrendPoint {
  /** Bucket start, epoch milliseconds (local hour or local midnight). */
  start: number;
  calls: number;
  failures: number;
  tokens: number;
}

export type TrendGranularity = 'hour' | 'day';

const HOUR_MS = 3_600_000;
const DAY_MS = 86_400_000;

const localMidnight = (ms: number) => {
  const date = new Date(ms);
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
};

/**
 * Turn hourly server buckets into a continuous series for the chart.
 * Ranges up to two days stay hourly; longer ranges fold into local days.
 * Gaps are filled with zero points so the time axis stays linear.
 */
export function buildTrend(
  buckets: SummaryBucket[],
  range: { from?: number; to?: number; now?: number }
): { granularity: TrendGranularity; points: TrendPoint[] } {
  if (!buckets.length) return { granularity: 'hour', points: [] };
  const first = buckets[0].start * 1000;
  const last = buckets[buckets.length - 1].start * 1000;
  const start = range.from ?? first;
  const end = range.to ?? last + HOUR_MS;
  const granularity: TrendGranularity = end - start <= 2 * DAY_MS ? 'hour' : 'day';
  const align = granularity === 'hour' ? (ms: number) => ms - (ms % HOUR_MS) : localMidnight;
  const step = (ms: number) =>
    granularity === 'hour'
      ? ms + HOUR_MS
      : // Advance by calendar day so DST shifts never skip or repeat a day.
        new Date(
          new Date(ms).getFullYear(),
          new Date(ms).getMonth(),
          new Date(ms).getDate() + 1
        ).getTime();

  const folded = new Map<number, TrendPoint>();
  for (const bucket of buckets) {
    const key = align(bucket.start * 1000);
    const point = folded.get(key) ?? { start: key, calls: 0, failures: 0, tokens: 0 };
    point.calls += bucket.calls;
    point.failures += bucket.failures;
    point.tokens += bucket.total_tokens;
    folded.set(key, point);
  }

  const points: TrendPoint[] = [];
  // A bounded range shows its full span (a quiet morning stays visible as
  // zeros); the end stops at the latest data so an in-progress day is not
  // padded into the future. Open ranges shrink to the recorded data.
  const lastKey = align(
    range.to !== undefined ? Math.max(last, Math.min(end, range.now ?? end) - 1) : last
  );
  const firstKey = align(range.from !== undefined ? start : first);
  // Cap the series so an "all time" range over years cannot render thousands of bars.
  for (let cursor = firstKey; cursor <= lastKey && points.length < 400; cursor = step(cursor)) {
    points.push(folded.get(cursor) ?? { start: cursor, calls: 0, failures: 0, tokens: 0 });
  }
  return { granularity, points };
}

export interface UsageKpis {
  requests: number;
  executions: number;
  failures: number;
  successRate: number | null;
  tokens: number;
  inputTokens: number;
  outputTokens: number;
  cacheRate: number | null;
  avgLatency: number | null;
  avgTtft: number | null;
  unknownTokenRecords: number;
  failovers: number;
}

export function deriveKpis(total: Summary | undefined): UsageKpis | null {
  if (!total) return null;
  const latencyCount = total.latency_count ?? 0;
  const ttftCount = total.ttft_count ?? 0;
  return {
    requests: total.calls,
    executions: total.executions,
    failures: total.failures,
    successRate:
      total.executions > 0 ? ((total.executions - total.failures) / total.executions) * 100 : null,
    tokens: total.total_tokens,
    inputTokens: total.input_tokens,
    outputTokens: total.output_tokens,
    cacheRate:
      total.input_tokens > 0 && total.cache_read_tokens > 0
        ? (total.cache_read_tokens / total.input_tokens) * 100
        : null,
    avgLatency: latencyCount > 0 ? (total.latency_ms ?? 0) / latencyCount : null,
    avgTtft: ttftCount > 0 ? (total.ttft_ms ?? 0) / ttftCount : null,
    unknownTokenRecords: total.unknown_token_records,
    failovers: total.failovers ?? 0,
  };
}
