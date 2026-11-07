import type { Summary, SummaryBucket } from './types';

export type UsageDimension = 'model' | 'consumer' | 'credential';

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
