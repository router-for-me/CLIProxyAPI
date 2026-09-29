import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import type { TFunction } from 'i18next';
import { XAI_CONFIG } from '@/features/quota/providers/xai/data';
import { apiCallApi, type ApiCallRequest, type ApiCallResult } from '@/services/api';
import {
  XAI_API_CHAT_URL,
  XAI_API_ME_URL,
  XAI_BILLING_MONTHLY_URL,
  XAI_BILLING_WEEKLY_URL,
  isPaidXaiAuthFile,
} from '@/utils/quota';

const t = ((key: string) => key) as unknown as TFunction;
const originalApiCallRequest = apiCallApi.request;

const result = (statusCode: number, body: unknown = null): ApiCallResult => ({
  statusCode,
  header: {},
  bodyText: body === null ? '' : JSON.stringify(body),
  body,
});

const encodeJwt = (payload: Record<string, unknown>): string => {
  const encoded = btoa(JSON.stringify(payload))
    .replace(/=/g, '')
    .replace(/\+/g, '-')
    .replace(/\//g, '_');
  return `header.${encoded}.signature`;
};

describe('xAI paid OAuth quota fallback', () => {
  let requests: ApiCallRequest[];

  beforeEach(() => {
    requests = [];
  });

  afterEach(() => {
    apiCallApi.request = originalApiCallRequest;
  });

  test('recognizes paid credentials without trusting individual route hints', () => {
    expect(isPaidXaiAuthFile({ name: 'paid-route.json', using_api: true, prefix: 'PAID' })).toBe(
      true
    );
    expect(
      isPaidXaiAuthFile({
        name: 'tier.json',
        metadata: { access_token: encodeJwt({ tier: 1 }) },
      })
    ).toBe(true);
    expect(isPaidXaiAuthFile({ name: 'using-api-only.json', using_api: true })).toBe(false);
    expect(isPaidXaiAuthFile({ name: 'prefix-only.json', prefix: 'paid' })).toBe(false);
    expect(isPaidXaiAuthFile({ name: 'free.json', using_api: false })).toBe(false);
    expect(isPaidXaiAuthFile({ name: 'free-default.json', base_url: 'https://api.x.ai/v1' })).toBe(
      false
    );
  });

  test('skips free billing endpoints for a recognized paid list entry', async () => {
    apiCallApi.request = async (payload) => {
      requests.push(payload);
      if (payload.url === XAI_API_ME_URL) {
        return result(200, { user_id: 'user-1', team_id: 'team-1' });
      }
      return result(200, { choices: [] });
    };

    const summary = await XAI_CONFIG.fetchQuota(
      {
        name: 'paid.json',
        type: 'xai',
        auth_index: 'xai:1',
        using_api: true,
        prefix: 'paid',
      },
      t
    );

    expect(requests.map((request) => request.url)).toEqual([XAI_API_ME_URL, XAI_API_CHAT_URL]);
    expect(JSON.parse(requests[1]?.data ?? '{}')).toMatchObject({
      model: 'grok-4.5',
      max_tokens: 1,
      stream: false,
    });
    expect(summary).toMatchObject({
      mode: 'paid-health',
      source: 'api.x.ai-fallback',
      planType: 'paid',
      healthStatus: 'chat-ok',
      userId: 'user-1',
      teamId: 'team-1',
    });
  });

  test('keeps existing billing behavior when only one route hint is present', async () => {
    apiCallApi.request = async (payload) => {
      requests.push(payload);
      if (payload.url === XAI_BILLING_WEEKLY_URL) {
        return result(200, {
          config: {
            currentPeriod: { type: 'weekly' },
            creditUsagePercent: 25,
          },
        });
      }
      if (payload.url === XAI_BILLING_MONTHLY_URL) {
        return result(200, {
          config: {
            monthlyLimit: { val: 10000 },
            used: { val: 2500 },
            billingPeriodStart: '2026-07-01T00:00:00Z',
            billingPeriodEnd: '2026-08-01T00:00:00Z',
          },
        });
      }
      throw new Error(`Unexpected URL: ${payload.url}`);
    };

    const summary = await XAI_CONFIG.fetchQuota(
      { name: 'free.json', type: 'xai', auth_index: 'xai:3', using_api: true },
      t
    );

    expect(requests.map((request) => request.url).sort()).toEqual(
      [XAI_BILLING_WEEKLY_URL, XAI_BILLING_MONTHLY_URL].sort()
    );
    expect(summary).toMatchObject({
      mode: 'billing',
      source: 'cli-chat-proxy',
      periodType: 'weekly',
      usagePercent: 25,
      monthlyLimitCents: 10000,
      billingPeriodEnd: '2026-08-01T00:00:00Z',
      resetAtMs: null,
      periodHours: null,
    });
    expect(summary.periodStart).toBeUndefined();
    expect(summary.periodEnd).toBeUndefined();
  });

  test('falls back to paid health after both free billing probes fail', async () => {
    apiCallApi.request = async (payload) => {
      requests.push(payload);
      if (payload.url === XAI_BILLING_WEEKLY_URL || payload.url === XAI_BILLING_MONTHLY_URL) {
        return result(403, { error: 'Access denied' });
      }
      if (payload.url === XAI_API_ME_URL) return result(200, { user_id: 'paid-user' });
      return result(200, { choices: [] });
    };

    const summary = await XAI_CONFIG.fetchQuota(
      { name: 'unknown.json', type: 'xai', auth_index: 'xai:4' },
      t
    );

    expect(requests.map((request) => request.url)).toEqual([
      XAI_BILLING_WEEKLY_URL,
      XAI_BILLING_MONTHLY_URL,
      XAI_API_ME_URL,
      XAI_API_CHAT_URL,
    ]);
    expect(summary).toMatchObject({ mode: 'paid-health', userId: 'paid-user' });
  });

  test('preserves the billing error when the paid fallback also fails', async () => {
    apiCallApi.request = async (payload) => {
      if (payload.url === XAI_API_CHAT_URL) return result(401, { error: 'Invalid token' });
      return result(403, { error: 'Access denied' });
    };

    await expect(
      XAI_CONFIG.fetchQuota({ name: 'invalid.json', type: 'xai', auth_index: 'xai:5' }, t)
    ).rejects.toMatchObject({ status: 403 });
  });
});
