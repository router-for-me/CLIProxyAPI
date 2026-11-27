import { afterEach, describe, expect, test } from 'bun:test';
import type { TFunction } from 'i18next';
import { ANTIGRAVITY_CONFIG } from '@/features/quota/providers/antigravity/data';
import { antigravitySubscriptionApi, apiCallApi, type ApiCallResult } from '@/services/api';
import { getAntigravityValidationError } from '@/services/api/antigravityValidation';
import { ANTIGRAVITY_QUOTA_URLS, getStatusFromError } from '@/utils/quota';

const originalRequest = apiCallApi.request;
const originalSubscription = antigravitySubscriptionApi.get;
const t = ((key: string) => key) as unknown as TFunction;
const file = { name: 'test.json', auth_index: 'test-account', project_id: 'test-project' };
const group = (displayName: string, bucketId: string, remainingFraction = 0.5) => ({
  displayName,
  buckets: [{ bucketId, window: 'weekly', remainingFraction }],
});
const claude = group('Claude and GPT models', 'claude-weekly');
const gemini = group('Gemini Models', 'gemini-weekly');
const response = (groups: unknown[]): ApiCallResult => ({
  statusCode: 200,
  header: { Date: ['Thu, 29 Oct 2026 12:00:00 GMT'] },
  body: { groups },
  bodyText: JSON.stringify({ groups }),
});

function mockResponses(responses: Array<ApiCallResult | Error>) {
  const urls: string[] = [];
  antigravitySubscriptionApi.get = async () => ({
    plan: 'pro',
    tierName: 'Pro',
    tierId: 'g1-pro-tier',
  });
  apiCallApi.request = async (request) => {
    urls.push(request.url);
    const next = responses.shift();
    if (!next) throw new Error('Unexpected request');
    if (next instanceof Error) throw next;
    return next;
  };
  return urls;
}

afterEach(() => {
  apiCallApi.request = originalRequest;
  antigravitySubscriptionApi.get = originalSubscription;
});

describe('Antigravity partial quota fallback', () => {
  test('reaches production after partial daily responses and keeps the first bucket value', async () => {
    const urls = mockResponses([
      response([claude]),
      response([group('Claude and GPT models', 'claude-weekly', 0.1)]),
      response([gemini, group('Claude and GPT models', 'claude-weekly', 0.2)]),
    ]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(urls).toEqual(ANTIGRAVITY_QUOTA_URLS);
    expect(result.groups.map((entry) => entry.label)).toEqual([
      'Claude and GPT models',
      'Gemini Models',
    ]);
    expect(result.groups[0].buckets).toHaveLength(1);
    expect(result.groups[0].buckets[0].remainingFraction).toBe(0.5);
    expect(result.subscription?.plan).toBe('pro');
    expect(result.serverTimeOffsetMs).not.toBeNull();
  });

  test('does not probe fallback endpoints when Gemini is already present', async () => {
    const urls = mockResponses([response([claude, gemini])]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(urls).toEqual([ANTIGRAVITY_QUOTA_URLS[0]]);
    expect(result.groups).toHaveLength(2);
  });

  test('retains partial quota when subsequent endpoints fail or return empty groups', async () => {
    const urls = mockResponses([response([claude]), new Error('offline'), response([])]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(urls).toEqual(ANTIGRAVITY_QUOTA_URLS);
    expect(result.groups[0].label).toBe('Claude and GPT models');
    expect(result.subscription?.plan).toBe('pro');
    expect(result.serverTimeOffsetMs).not.toBeNull();
  });

  test('adds missing buckets within an existing group without replacing earlier values', async () => {
    mockResponses([
      response([claude]),
      response([
        {
          ...claude,
          buckets: [
            { bucketId: 'claude-weekly', window: 'weekly', remainingFraction: 0.1 },
            { bucketId: 'claude-five-hour', window: '5h', remainingFraction: 0.7 },
          ],
        },
        gemini,
      ]),
    ]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(result.groups[0].buckets.map((bucket) => bucket.id)).toEqual([
      'claude-five-hour',
      'claude-weekly',
    ]);
    expect(result.groups[0].buckets.map((bucket) => bucket.remainingFraction)).toEqual([0.7, 0.5]);
  });

  test('retains partial quota when fallback endpoints return HTTP errors', async () => {
    const failed = { ...response([]), statusCode: 403 };
    mockResponses([response([claude]), failed, failed]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(result.groups).toHaveLength(1);
    expect(result.groups[0].label).toBe('Claude and GPT models');
  });

  test('keeps the existing empty-success behavior when no endpoint has usable groups', async () => {
    mockResponses([response([]), response([]), response([])]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(result.groups).toEqual([]);
    expect(result.serverTimeOffsetMs).toBeNull();
  });

  const link = 'https://accounts.google.com/signin/continue?test=1';
  const denied = (details: unknown[], statusCode = 403): ApiCallResult => ({
    ...response([]),
    statusCode,
    body: { error: { message: 'Verify this account.', details } },
  });

  test('retains verification across fallback failures and prefers an actionable response', async () => {
    mockResponses([
      denied([{ reason: 'VALIDATION_REQUIRED' }]),
      denied([null, { metadata: { validation_url: link } }]),
      denied([], 404),
    ]);
    const error = await ANTIGRAVITY_CONFIG.fetchQuota(file, t).then(
      () => null,
      (err: unknown) => err
    );
    expect(error).toBeInstanceOf(Error);
    const state = ANTIGRAVITY_CONFIG.buildErrorState(
      (error as Error).message,
      getStatusFromError(error),
      error
    );
    expect(state).toMatchObject({
      error: 'Verify this account.',
      errorStatus: 403,
      validation: { url: link },
    });
  });

  test.each([true, false])(
    'keeps verification without usable quota (challenge first: %s)',
    async (challengeFirst) => {
      const challenge = denied([{ metadata: { validationUrl: link } }]);
      const unusable = [response([]), response([{ buckets: [{}] }])];
      mockResponses(challengeFirst ? [challenge, ...unusable] : [...unusable, challenge]);
      await expect(ANTIGRAVITY_CONFIG.fetchQuota(file, t)).rejects.toMatchObject({
        status: 403,
        message: 'Verify this account.',
        url: link,
      });
    }
  );

  test('partial quota wins over a later verification challenge', async () => {
    mockResponses([response([claude]), denied([{ reason: 'VALIDATION_REQUIRED' }]), response([])]);
    const result = await ANTIGRAVITY_CONFIG.fetchQuota(file, t);
    expect(result.groups.map((entry) => entry.label)).toEqual(['Claude and GPT models']);
  });

  test('a successful fallback still wins over a verification challenge', async () => {
    mockResponses([denied([{ metadata: { validationUrl: link } }]), response([gemini])]);
    expect((await ANTIGRAVITY_CONFIG.fetchQuota(file, t)).groups).toHaveLength(1);
  });

  test('accepts camelCase metadata only on 403 and ignores unrelated errors', () => {
    const details = [{ metadata: { validationUrl: link } }];
    expect(getAntigravityValidationError(denied(details), '')?.url).toBe(link);
    expect(getAntigravityValidationError(denied(details, 404), '')).toBeUndefined();
    expect(getAntigravityValidationError(denied([{ reason: 'OTHER' }]), '')).toBeUndefined();
  });

  test.each([
    'javascript:alert(1)',
    'not a URL',
    'http://accounts.google.com/',
    'https://accounts.google.com.example.org/',
    'https://elsewhere.test@accounts.google.com/',
    'https://accounts.google.com:8080/',
  ])('preserves the challenge without exposing unsafe URL %s', (url) => {
    const result = getAntigravityValidationError(
      denied([{ reason: 'VALIDATION_REQUIRED', metadata: { validation_url: url } }]),
      'fallback'
    );
    expect(result?.message).toBe('Verify this account.');
    expect(result?.url).toBeUndefined();
  });

  test('preserves the existing error when every endpoint fails', async () => {
    mockResponses([new Error('offline'), new Error('offline'), new Error('offline')]);
    await expect(ANTIGRAVITY_CONFIG.fetchQuota(file, t)).rejects.toThrow('offline');
  });
});
