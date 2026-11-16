import { describe, expect, spyOn, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { normalizeQuotaHistory, quotaHistoryApi } from '@/services/api/quotaHistory';

const credential = (overrides: Record<string, unknown> = {}) => ({
  auth_index: 'idx-a',
  id: 'claude-a.json',
  name: 'claude-a.json',
  provider: 'claude',
  windows: [
    {
      kind: 'weekly',
      period_seconds: 604800,
      points: [
        [1793010000, 0.3595, 1793120400],
        [1792998000, 0.304, 1793120400],
        [1793010000, 0.9, 0],
        [1793020000, 1.4, 0],
        [1793030000, 'bad', 0],
        [1793040000, Number.NaN, 0],
        [1793050000, 0.5],
        'not-a-point',
      ],
      scope: '',
    },
  ],
  ...overrides,
});

const response = (credentials: unknown[]) => ({
  credentials,
  enabled: true,
  now: '2026-10-26T10:52:00Z',
  retention_hours: 720,
  since: '2026-10-23T00:00:00Z',
});

describe('normalizeQuotaHistory', () => {
  test('converts seconds to ms, maps an unknown reset to null, sorts, dedupes and drops bad points', () => {
    const result = normalizeQuotaHistory(response([credential()]));

    expect(result).toEqual({
      credentials: [
        {
          authIndex: 'idx-a',
          id: 'claude-a.json',
          name: 'claude-a.json',
          provider: 'claude',
          windows: [
            {
              kind: 'weekly',
              periodMs: 604_800_000,
              points: [
                { resetAt: 1_793_120_400_000, t: 1_792_998_000_000, used: 0.304 },
                { resetAt: 1_793_120_400_000, t: 1_793_010_000_000, used: 0.3595 },
                { resetAt: null, t: 1_793_020_000_000, used: 1 },
              ],
              scope: '',
            },
          ],
        },
      ],
      retentionHours: 720,
      serverNow: Date.UTC(2026, 9, 26, 10, 52),
      since: Date.UTC(2026, 9, 23),
      status: 'ok',
    });
  });

  test('maps a zero period and an empty auth index to null and drops malformed entries', () => {
    const result = normalizeQuotaHistory(
      response([
        credential({
          auth_index: '',
          windows: [
            { kind: 'weekly', period_seconds: 0, points: [], scope: '' },
            { kind: 'monthly', points: [] },
            { kind: 'weekly' },
          ],
        }),
        credential({ id: '' }),
        credential({ windows: 'none' }),
        { name: 'no-id.json', windows: [] },
        null,
      ])
    );

    expect(result.status).toBe('ok');
    if (result.status !== 'ok') return;

    expect(result.credentials).toEqual([
      {
        authIndex: null,
        id: 'claude-a.json',
        name: 'claude-a.json',
        provider: 'claude',
        windows: [{ kind: 'weekly', periodMs: null, points: [], scope: '' }],
      },
    ]);
  });

  test('turns a numeric auth index into a string', () => {
    const result = normalizeQuotaHistory(response([credential({ auth_index: 7 })]));

    expect(result.status === 'ok' && result.credentials[0].authIndex).toBe('7');
  });

  test('maps enabled:false to disabled', () => {
    expect(
      normalizeQuotaHistory({ credentials: [], enabled: false, now: '2026-10-26T10:52:00Z' })
    ).toEqual({ status: 'disabled' });
  });

  test('reports a malformed envelope as an error', () => {
    expect(normalizeQuotaHistory('nope').status).toBe('error');
    expect(normalizeQuotaHistory({ ...response([]), now: 'later' }).status).toBe('error');
    expect(normalizeQuotaHistory({ ...response([]), credentials: null }).status).toBe('error');
  });
});

describe('quotaHistoryApi.get', () => {
  test('requests the since instant in unix seconds', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue(response([]));

    try {
      const result = await quotaHistoryApi.get({ sinceMs: 1_792_998_000_999 });

      expect(get).toHaveBeenCalledWith('/credentials/quota-history', {
        params: { since: 1_792_998_000 },
      });
      expect(result.status).toBe('ok');
    } finally {
      get.mockRestore();
    }
  });

  test('maps a 404 to unsupported', async () => {
    const error = Object.assign(new Error('Not Found'), { status: 404 });
    const get = spyOn(apiClient, 'get').mockRejectedValue(error);

    try {
      await expect(quotaHistoryApi.get({ sinceMs: 0 })).resolves.toEqual({ status: 'unsupported' });
    } finally {
      get.mockRestore();
    }
  });

  test('maps any other failure to error with its message', async () => {
    const error = Object.assign(new Error('boom'), { status: 500 });
    const get = spyOn(apiClient, 'get').mockRejectedValue(error);

    try {
      await expect(quotaHistoryApi.get({ sinceMs: 0 })).resolves.toEqual({
        message: 'boom',
        status: 'error',
      });
    } finally {
      get.mockRestore();
    }
  });
});
