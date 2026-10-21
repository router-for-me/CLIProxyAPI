import { describe, expect, spyOn, test } from 'bun:test';
import { KIMI_CONFIG } from '@/features/quota/providers/kimi/data';
import { apiCallApi, authFilesApi } from '@/services/api';
import { KIMI_AI_USAGE_URL, KIMI_USAGE_URL, buildKimiQuotaRows } from '@/utils/quota';
import type { AuthFileItem } from '@/types';

describe('Kimi monthly quota', () => {
  test('plans without a weekly limit show the monthly total from usages', () => {
    const rows = buildKimiQuotaRows({
      limits: [
        {
          detail: { limit: '100', used: '5', remaining: '95', resetTime: '2099-09-27T00:30:34Z' },
          window: { duration: 300, timeUnit: 'TIME_UNIT_MINUTE' },
        },
      ],
      usages: { limit_month_total: { used_ratio: 0.2529, reset_time: '2099-10-22T00:00:00Z' } },
    });

    expect(rows.map(({ id }) => id)).toEqual(['limit-0', 'monthly']);
    expect(rows[1]).toMatchObject({
      labelKey: 'kimi_quota.monthly_limit',
      used: 25,
      limit: 100,
      resetAtMs: Date.parse('2099-10-22T00:00:00Z'),
      periodHours: 720,
    });
  });

  test('weekly plans without a monthly total keep their rows unchanged', () => {
    const rows = buildKimiQuotaRows({
      usage: { limit: '100', used: '97', resetTime: '2099-09-30T16:48:39Z' },
      usages: {},
    });
    expect(rows.map(({ id }) => id)).toEqual(['summary']);
  });
});

describe('Kimi International auth files', () => {
  test('kimi-ai credentials are listed on the Kimi quota page', () => {
    const file = { name: 'kimi-ai-1.json', provider: 'kimi-ai', type: 'kimi-ai' } as AuthFileItem;
    expect(KIMI_CONFIG.filterFn(file)).toBe(true);
  });

  test('kimi-ai credentials query the kimi.ai host', async () => {
    const urls: string[] = [];
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue('{}');
    const request = apiCallApi.request;
    apiCallApi.request = (async ({ url }: { url: string }) => {
      urls.push(url);
      return { statusCode: 200, body: { limits: [] } };
    }) as unknown as typeof apiCallApi.request;
    try {
      for (const provider of ['kimi', 'kimi-ai', 'kimi_ai']) {
        await KIMI_CONFIG.fetchQuota(
          { name: 'k.json', provider, auth_index: 'a1' } as AuthFileItem,
          ((key: string) => key) as never
        );
      }
    } finally {
      apiCallApi.request = request;
      download.mockRestore();
    }
    expect(urls).toEqual([KIMI_USAGE_URL, KIMI_AI_USAGE_URL, KIMI_AI_USAGE_URL]);
  });
});
