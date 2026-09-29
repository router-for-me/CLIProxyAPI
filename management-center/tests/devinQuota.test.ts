import { describe, expect, test } from 'bun:test';
import { hasDevinQuotaObservation, readDevinQuotaResponse } from '@/services/api/devinQuota';

const payload = {
  userStatus: {
    name: 'developer',
    email: 'user@example.test',
    planStatus: {
      planInfo: { planName: 'Pro', accountDisplayName: 'Example' },
      planStart: '2026-09-11T00:42:44Z',
      planEnd: '2026-10-11T00:42:44Z',
      dailyQuotaRemainingPercent: 54,
      weeklyQuotaRemainingPercent: 77,
      dailyQuotaResetAtUnix: '1789372800',
      weeklyQuotaResetAtUnix: '1789891200',
    },
  },
};

const fromStatus = (status: Record<string, unknown>) =>
  readDevinQuotaResponse({ userStatus: { planStatus: status } }, 1234);

describe('Devin live quota normalization', () => {
  test('maps the latest issue 429 response and converts Unix seconds to milliseconds', () => {
    const result = readDevinQuotaResponse(payload, 1234);
    expect(result).toEqual({
      windows: [
        { id: 'daily', remainingPercent: 54, resetAtMs: 1789372800000, periodHours: 24 },
        { id: 'weekly', remainingPercent: 77, resetAtMs: 1789891200000, periodHours: 168 },
      ],
      plan: 'Pro',
      planStartMs: Date.parse('2026-09-11T00:42:44Z'),
      planEndMs: Date.parse('2026-10-11T00:42:44Z'),
      observedAtMs: 1234,
    });
    expect(readDevinQuotaResponse(JSON.stringify(payload), 1234)).toEqual(result);
    expect(JSON.stringify(result)).not.toContain('user@example.test');
  });

  test('preserves zero, full, fractional and numeric-string percentages without inferring missing values', () => {
    for (const value of [0, 100, 0.5, '54']) {
      const result = fromStatus({ dailyQuotaRemainingPercent: value });
      expect(result.windows[0].remainingPercent).toBe(Number(value));
      expect(result.windows[1].remainingPercent).toBeNull();
      expect(result.observedAtMs).toBe(1234);
    }
  });

  test('rejects malformed percentages and reset timestamps instead of showing full quota', () => {
    for (const value of [null, undefined, '', ' ', '54%', -1, 101, NaN, Infinity, true, {}]) {
      expect(
        fromStatus({ dailyQuotaRemainingPercent: value }).windows[0].remainingPercent
      ).toBeNull();
    }
    for (const value of [null, '', ' ', 0, -1, 1.5, Infinity, 'bad', true, 1e20]) {
      expect(fromStatus({ dailyQuotaResetAtUnix: value }).windows[0].resetAtMs).toBeNull();
    }
    expect(fromStatus({ dailyQuotaResetAtUnix: 1789372800 }).windows[0].resetAtMs).toBe(
      1789372800000
    );
  });

  test('ignores old quota signals, metadata and invalid envelopes', () => {
    for (const value of [
      null,
      [],
      'not json',
      {},
      { userStatus: [] },
      { quota: { signals: { daily_quota_remaining_percent: '100%' } } },
      { metadata: { dailyQuotaRemainingPercent: 100 } },
    ]) {
      const result = readDevinQuotaResponse(value);
      expect(hasDevinQuotaObservation(result)).toBeFalse();
      expect(result.observedAtMs).toBeNull();
    }
  });

  test('keeps partial resets and plan data while rejecting invalid plan times', () => {
    const result = fromStatus({
      weeklyQuotaResetAtUnix: '1789891200',
      planInfo: { planName: ' Pro ' },
      planStart: '0001-01-01T00:00:00Z',
      planEnd: 'not a date',
    });
    expect(hasDevinQuotaObservation(result)).toBeTrue();
    expect(result.plan).toBe('Pro');
    expect(result.planStartMs).toBeNull();
    expect(result.planEndMs).toBeNull();
    expect(result.windows[0].resetAtMs).toBeNull();
  });
});
