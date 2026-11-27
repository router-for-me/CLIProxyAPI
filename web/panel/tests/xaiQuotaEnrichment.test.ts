import { afterEach, expect, test } from 'bun:test';
import i18n from '@/i18n';
import type { AuthFileItem } from '@/types';
import { apiCallApi, type ApiCallResult } from '@/services/api';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { XAI_CONFIG } from '@/features/quota/providers/xai/data';
import type { QuotaAdapter } from '@/features/quota/providers';
import { enrichQuotaInBackground } from '@/features/quota/quotaEnrichment';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { XAI_USER_URL, XAI_SETTINGS_URL, buildXaiBillingSummary } from '@/utils/quota';

const originalRequest = apiCallApi.request;
const file: AuthFileItem = { name: 'fixture.json', type: 'xai', auth_index: 'fixture' };
const key = getQuotaCacheKey(file);
const adapter = XAI_CONFIG as unknown as QuotaAdapter;
const result = (body: unknown, statusCode = 200): ApiCallResult => ({
  statusCode,
  body,
  bodyText: JSON.stringify(body),
  header: {},
});
const billing = () =>
  buildXaiBillingSummary({ monthlyLimit: { val: 15000 }, used: { val: 1500 } })!;
const current = () => useQuotaStore.getState().xaiQuota[key];
const deferred = () => {
  let resolve!: (value: ApiCallResult) => void;
  const promise = new Promise<ApiCallResult>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
const beginEnrichment = () => {
  const data = billing();
  const state = XAI_CONFIG.buildSuccessState(data);
  useQuotaStore.getState().setXaiQuota({ [key]: state });
  const pending = enrichQuotaInBackground(adapter, file, data, state, i18n.t);
  return { data, state, pending };
};

afterEach(() => {
  apiCallApi.request = originalRequest;
  useQuotaStore.getState().clearQuotaCache();
});

test('commits quota before requesting optional plan and updates it later', async () => {
  const gate = deferred();
  const urls: string[] = [];
  apiCallApi.request = async (request, config) => {
    urls.push(request.url);
    if (request.url === XAI_USER_URL || request.url === XAI_SETTINGS_URL) {
      expect(current()?.status).toBe('success');
      expect(config?.timeout).toBe(8000);
      return gate.promise;
    }
    return result({ config: { monthlyLimit: { val: 15000 }, used: { val: 1500 } } });
  };
  const data = await XAI_CONFIG.fetchQuota(file, i18n.t);
  expect(urls).toHaveLength(2);
  expect(urls).not.toContain(XAI_USER_URL);
  const state = XAI_CONFIG.buildSuccessState(data);
  useQuotaStore.getState().setXaiQuota({ [key]: state });
  const pending = enrichQuotaInBackground(adapter, file, data, state, i18n.t);
  expect(current()).toBe(state);
  expect(current().billing?.planLabel).toBeUndefined();
  gate.resolve(result({ subscription_tier_display: 'SuperGrok Heavy' }));
  await pending;
  expect(current().billing).toMatchObject({
    ...data,
    planLabel: 'SuperGrok Heavy',
    planTier: 'elite',
  });
});

for (const failure of ['network', 'http', 'malformed'] as const) {
  test(`optional ${failure} failure preserves the exact original quota and fallback`, async () => {
    apiCallApi.request = async () => {
      if (failure === 'network') throw new Error('unavailable');
      return failure === 'http' ? result({}, 503) : result('not-json');
    };
    const { state, pending } = beginEnrichment();
    await pending;
    expect(current()).toBe(state);
  });
}

test('uses user tier when settings fails', async () => {
  apiCallApi.request = async ({ url }) => {
    if (url === XAI_SETTINGS_URL) throw new Error('unavailable');
    return result({ subscriptionTier: 'SuperGrok' });
  };
  const { pending } = beginEnrichment();
  await pending;
  expect(current().billing).toMatchObject({ planLabel: 'SuperGrok', planTier: 'premium' });
});

for (const change of ['session', 'file', 'new-result', 'refresh'] as const) {
  test(`late subscription cannot overwrite ${change}`, async () => {
    const gate = deferred();
    apiCallApi.request = async () => gate.promise;
    const { pending } = beginEnrichment();
    if (change === 'session') useQuotaStore.getState().clearQuotaCache();
    if (change === 'file') useQuotaStore.getState().clearQuotaCache([file.name]);
    if (change === 'new-result') {
      useQuotaStore.getState().setXaiQuota({ [key]: XAI_CONFIG.buildSuccessState(billing()) });
    }
    if (change === 'refresh') {
      useQuotaStore.getState().setXaiQuota({ [key]: XAI_CONFIG.buildLoadingState() });
    }
    const expected = current();
    gate.resolve(result({ subscription_tier_display: 'SuperGrok Heavy' }));
    await pending;
    expect(current()).toBe(expected);
  });
}

test('unrelated file invalidation does not discard a valid subscription', async () => {
  const gate = deferred();
  apiCallApi.request = async () => gate.promise;
  const { pending } = beginEnrichment();
  useQuotaStore.getState().clearQuotaCache(['other.json']);
  gate.resolve(result({ subscription_tier_display: 'SuperGrok Heavy' }));
  await pending;
  expect(current().billing?.planLabel).toBe('SuperGrok Heavy');
});
