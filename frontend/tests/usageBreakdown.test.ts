import { describe, expect, test } from 'bun:test';
import {
  buildTrend,
  deriveKpis,
  rankMetric,
  rankUsage,
} from '../src/features/sharing/usageBreakdown';
import type { Summary, SummaryBucket } from '../src/features/sharing/types';

const group = (overrides: Partial<Summary>): Summary => ({
  calls: 1,
  executions: 1,
  failures: 0,
  input_tokens: 10,
  output_tokens: 5,
  reasoning_tokens: 2,
  cache_read_tokens: 3,
  cache_creation_tokens: 1,
  total_tokens: 15,
  unknown_token_records: 0,
  latency_ms: 1000,
  latency_count: 1,
  ...overrides,
});

describe('usage ranking', () => {
  test('folds days, owners and channels using additive metrics only', () => {
    const rows = rankUsage(
      [
        group({
          day: '2026-11-01',
          consumer: 'alice',
          channel: 'a#1',
          model: 'kimi-k3',
          executions: 2,
          failures: 1,
          total_tokens: 140,
        }),
        group({
          day: '2026-11-02',
          consumer: 'bob',
          channel: 'b#1',
          model: 'kimi-k3',
          executions: 3,
          failures: 1,
          total_tokens: 260,
          latency_ms: 4000,
          latency_count: 2,
        }),
      ],
      'model'
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      key: 'kimi-k3',
      executions: 5,
      failures: 2,
      total_tokens: 400,
      latency_ms: 5000,
      latency_count: 3,
    });
    // Distinct-trace request counts are not additive across groups.
    expect(rows[0]).not.toHaveProperty('calls');
  });

  test('ranks by Tokens, then calls, then name', () => {
    const rows = rankUsage(
      [
        group({ consumer: 'bob', total_tokens: 20, executions: 1 }),
        group({ consumer: 'alice', total_tokens: 20, executions: 1 }),
        group({ consumer: 'carol', total_tokens: 20, executions: 5 }),
        group({ consumer: 'dave', total_tokens: 90, executions: 1 }),
      ],
      'consumer'
    );
    expect(rows.map((row) => row.key)).toEqual(['dave', 'carol', 'alice', 'bob']);
    expect(rankMetric(rows)).toBe('tokens');
    expect(rankMetric(rankUsage([group({ total_tokens: 0 })], 'model'))).toBe('calls');
  });

  test('credential ranking keys by ID and labels by pinned prefix', () => {
    const rows = rankUsage(
      [
        group({ credential_id: 'c1.json', channel: '123-kimi#1', total_tokens: 10 }),
        group({ credential_id: 'c1.json', channel: '123-kimi#1', total_tokens: 5 }),
        group({ credential_id: 'c2.json', channel: '123-kimi#2', total_tokens: 40 }),
      ],
      'credential'
    );
    expect(rows.map((row) => [row.key, row.label, row.total_tokens])).toEqual([
      ['c2.json', '123-kimi#2', 40],
      ['c1.json', '123-kimi#1', 15],
    ]);
  });
});

describe('usage trend', () => {
  const hour = 3600;
  const base = Date.UTC(2026, 10, 5, 0, 0, 0) / 1000;
  const bucket = (offsetHours: number, calls: number, tokens: number): SummaryBucket => ({
    start: base + offsetHours * hour,
    calls,
    executions: calls,
    failures: 0,
    total_tokens: tokens,
    input_tokens: tokens,
    output_tokens: 0,
  });

  test('keeps hourly points for short ranges and fills gaps with zeros', () => {
    const { granularity, points } = buildTrend([bucket(0, 2, 10), bucket(3, 1, 5)], {
      from: base * 1000,
      to: (base + 24 * hour) * 1000,
    });
    expect(granularity).toBe('hour');
    // Without a fetch time the whole bounded day is drawn.
    expect(points).toHaveLength(24);
    expect(points.slice(0, 4).map((point) => point.calls)).toEqual([2, 0, 0, 1]);
    expect(points[1].start - points[0].start).toBe(hour * 1000);
  });

  test('folds into days for long ranges without double counting', () => {
    const { granularity, points } = buildTrend(
      [bucket(0, 2, 10), bucket(1, 3, 20), bucket(72, 1, 5)],
      {}
    );
    expect(granularity).toBe('day');
    const totalCalls = points.reduce((sum, point) => sum + point.calls, 0);
    const totalTokens = points.reduce((sum, point) => sum + point.tokens, 0);
    expect(totalCalls).toBe(6);
    expect(totalTokens).toBe(35);
    expect(points.length).toBeGreaterThanOrEqual(3);
  });

  test('bounded ranges keep empty leading hours and stop at the fetch time', () => {
    const { points } = buildTrend([bucket(5, 1, 5)], {
      from: base * 1000,
      to: (base + 24 * hour) * 1000,
      now: (base + 8 * hour + 60) * 1000,
    });
    expect(points.length).toBe(9);
    expect(points[0].start).toBe(base * 1000);
    expect(points[5].calls).toBe(1);
  });

  test('returns no points without buckets', () => {
    expect(buildTrend([], {}).points).toEqual([]);
  });
});

describe('usage KPIs', () => {
  test('derives rates and averages from additive totals', () => {
    const kpis = deriveKpis(
      group({
        calls: 4,
        executions: 5,
        failures: 1,
        input_tokens: 200,
        cache_read_tokens: 50,
        latency_ms: 6000,
        latency_count: 3,
        ttft_ms: 900,
        ttft_count: 3,
        failovers: 1,
      })
    );
    expect(kpis).toMatchObject({
      requests: 4,
      executions: 5,
      successRate: 80,
      cacheRate: 25,
      avgLatency: 2000,
      avgTtft: 300,
      failovers: 1,
    });
  });

  test('reports missing averages as null instead of zero', () => {
    const kpis = deriveKpis(
      group({ executions: 0, failures: 0, latency_ms: 0, latency_count: 0, cache_read_tokens: 0 })
    );
    expect(kpis?.successRate).toBeNull();
    expect(kpis?.avgLatency).toBeNull();
    expect(kpis?.avgTtft).toBeNull();
    expect(kpis?.cacheRate).toBeNull();
    expect(deriveKpis(undefined)).toBeNull();
  });
});
