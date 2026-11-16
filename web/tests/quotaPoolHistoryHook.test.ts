import { describe, expect, test } from 'bun:test';
import {
  historySkewMs,
  shiftHistoryResult,
  shouldFetchHistory,
  type HistoryRequestKey,
} from '@/features/quota/hooks/usePoolHistory';
import { MINUTE_MS } from '@/utils/time/durations';
import { NOW, historyOk } from './fixtures/quotaPoolFixture';

const okPoints = (result: ReturnType<typeof historyOk>) =>
  result.status === 'ok'
    ? result.credentials.flatMap((credential) =>
        credential.windows.flatMap((window) => window.points)
      )
    : [];

describe('clock skew', () => {
  test('shifts every point time and reset by the skew when it exceeds a minute', () => {
    const skewMs = historySkewMs(NOW + 5 * MINUTE_MS, NOW);
    const original = historyOk();
    const shifted = shiftHistoryResult(original, skewMs);
    const before = okPoints(original);
    const after = okPoints(shifted);

    expect(skewMs).toBe(5 * MINUTE_MS);
    expect(after).toHaveLength(before.length);
    expect(
      after.every(
        (point, index) =>
          point.t === before[index].t - skewMs &&
          point.resetAt === (before[index].resetAt ?? 0) - skewMs
      )
    ).toBe(true);
  });

  test('ignores a skew of a minute or less', () => {
    const original = historyOk();

    expect(historySkewMs(NOW + 30_000, NOW)).toBe(0);
    expect(historySkewMs(NOW - MINUTE_MS, NOW)).toBe(0);
    expect(shiftHistoryResult(original, 0)).toBe(original);
  });

  test('keeps an unknown reset unknown and leaves non-ok results alone', () => {
    const shifted = shiftHistoryResult(
      {
        credentials: [
          {
            authIndex: null,
            id: 'a',
            name: 'a',
            provider: 'claude',
            windows: [
              {
                kind: 'weekly',
                periodMs: null,
                points: [{ resetAt: null, t: NOW, used: 0.1 }],
                scope: '',
              },
            ],
          },
        ],
        retentionHours: 720,
        serverNow: NOW,
        since: NOW,
        status: 'ok',
      },
      MINUTE_MS * 2
    );
    const disabled = { status: 'disabled' } as const;

    expect(okPoints(shifted)).toEqual([{ resetAt: null, t: NOW - 2 * MINUTE_MS, used: 0.1 }]);
    expect(shiftHistoryResult(disabled, MINUTE_MS * 2)).toBe(disabled);
  });
});

describe('shouldFetchHistory', () => {
  const request = (token: number, sinceMs = NOW, generation = 1): HistoryRequestKey => ({
    generation,
    sinceMs,
    token,
  });

  test('fetches on the first enabled render and never while disabled', () => {
    expect(shouldFetchHistory(false, request(0), null)).toBe(false);
    expect(shouldFetchHistory(true, request(0), null)).toBe(true);
  });

  test('fetches once for a token bump that coincides with an enabled rising edge', () => {
    const fetched = request(0);
    const bumpWhileLoading = shouldFetchHistory(false, request(1), fetched);
    const risingEdge = shouldFetchHistory(true, request(1), fetched);
    const nextRender = shouldFetchHistory(true, request(1), request(1));

    expect([bumpWhileLoading, risingEdge, nextRender]).toEqual([false, true, false]);
  });

  test('refetches for an earlier since or a new session, not for a later since', () => {
    expect(shouldFetchHistory(true, request(1, NOW - MINUTE_MS), request(1))).toBe(true);
    expect(shouldFetchHistory(true, request(1, NOW + MINUTE_MS), request(1))).toBe(false);
    expect(shouldFetchHistory(true, request(1, NOW, 2), request(1))).toBe(true);
  });
});
