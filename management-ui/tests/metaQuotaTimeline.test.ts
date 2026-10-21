import { describe, expect, test } from 'bun:test';
import { buildTimelineLane } from '@/features/quota/quotaTimelineModel';
import { collectQuotaRowInstants, nextRecoveryMs } from '@/features/quota/resetSchedule';
import type { MetaQuotaState } from '@/types';

const NOW_MS = Date.parse('2099-01-01T00:00:00Z');
const unixSeconds = (ms: number) => ms / 1000;

const state = (windows: NonNullable<MetaQuotaState['data']>['windows']): MetaQuotaState => ({
  status: 'success',
  data: {
    planName: 'Muse Pro',
    isSubscriptionActive: true,
    windows,
  },
});

describe('Meta quota reset scheduling', () => {
  test('only consumed windows block recovery sorting and Unix seconds become milliseconds', () => {
    const quota = state([
      {
        id: 'window',
        usedPercent: 0,
        resetAt: unixSeconds(NOW_MS + 60 * 60 * 1000),
        durationMinutes: 60,
      },
      {
        id: 'weekly',
        usedPercent: 25,
        resetAt: unixSeconds(NOW_MS + 7 * 24 * 60 * 60 * 1000),
        durationMinutes: 7 * 24 * 60,
      },
    ]);

    expect(collectQuotaRowInstants('meta', quota)).toEqual([
      {
        rowId: 'weekly',
        atMs: NOW_MS + 7 * 24 * 60 * 60 * 1000,
        kind: 'window',
      },
    ]);
    expect(nextRecoveryMs('meta', quota, NOW_MS)).toBe(NOW_MS + 7 * 24 * 60 * 60 * 1000);
  });

  test('ignores unknown usage and reset instants that are not in the future', () => {
    const quota = state([
      {
        id: 'window',
        usedPercent: null,
        resetAt: unixSeconds(NOW_MS + 60 * 60 * 1000),
        durationMinutes: 60,
      },
      {
        id: 'weekly',
        usedPercent: 50,
        resetAt: unixSeconds(NOW_MS - 1),
        durationMinutes: 7 * 24 * 60,
      },
    ]);

    expect(collectQuotaRowInstants('meta', quota).map((instant) => instant.rowId)).toEqual([
      'weekly',
    ]);
    expect(nextRecoveryMs('meta', quota, NOW_MS)).toBeNull();
  });
});

describe('Meta quota timeline lane', () => {
  test('prefers weekly scope and converts duration minutes to hours', () => {
    const quota = state([
      {
        id: 'window',
        usedPercent: 0,
        resetAt: unixSeconds(NOW_MS + 60 * 60 * 1000),
        durationMinutes: 60,
      },
      {
        id: 'weekly',
        usedPercent: 40,
        resetAt: unixSeconds(NOW_MS + 7 * 24 * 60 * 60 * 1000),
      },
    ]);

    const lane = buildTimelineLane({
      name: 'meta.json',
      displayName: 'Meta',
      provider: 'meta',
      quota,
      maxPeriodHours: 14 * 24,
    });

    expect(lane.anchorMs).toBe(NOW_MS + 7 * 24 * 60 * 60 * 1000);
    expect(lane.periodHours).toBe(168);
    expect(lane.remaining).toBe(60);
    expect(lane.limits).toEqual([
      { label: 'meta_quota.window', remaining: 100 },
      { label: 'meta_quota.weekly', remaining: 60 },
    ]);
  });

  test('does not turn null usage into either zero or 100 percent remaining', () => {
    const quota = state([
      {
        id: 'weekly',
        usedPercent: null,
        resetAt: unixSeconds(NOW_MS + 7 * 24 * 60 * 60 * 1000),
      },
    ]);

    const lane = buildTimelineLane({
      name: 'meta.json',
      displayName: 'Meta',
      provider: 'meta',
      quota,
      maxPeriodHours: 14 * 24,
    });

    expect(lane.remaining).toBeNull();
    expect(lane.limits).toEqual([]);
  });
});
