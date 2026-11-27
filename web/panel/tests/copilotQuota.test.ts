import { afterEach, expect, mock, spyOn, test } from 'bun:test';
import { COPILOT_CONFIG, parseCopilotQuota } from '@/features/quota/providers/copilot/data';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { CopilotQuotaBody } from '@/features/quota/providers/copilot/CopilotQuotaBody';
import { QUOTA_CLASS_KEYS, bindQuotaClasses } from '@/features/quota/types';
import i18n from '@/i18n';
import { authFilesApi } from '@/services/api/authFiles';
import { apiClient } from '@/services/api/client';
import { useQuotaStore } from '@/stores/useQuotaStore';

const file = { name: 'copilot-test.json', authIndex: 'test-index', type: 'copilot' };
const credential = JSON.stringify({
  type: 'copilot',
  refresh_token: 'test-oauth',
  access_token: 'test-inference',
});
const snapshot = {
  quota_remaining: 1150.9,
  remaining: 1150,
  entitlement: 20000,
  percent_remaining: 5.7,
};
const payload = (premium: unknown) => ({
  login: 'fixture-account',
  quota_snapshots: { premium_interactions: premium },
});

afterEach(() => mock.restore());

test('downloads the selected credential and passes only its GitHub token to the fixed quota endpoint', async () => {
  spyOn(apiClient, 'captureConnection').mockReturnValue(apiClient);
  const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue(credential);
  const post = spyOn(apiClient, 'post').mockResolvedValue({
    status_code: 200,
    body: payload(snapshot),
  });
  const data = await COPILOT_CONFIG.fetchQuota(file, i18n.t);
  expect(download).toHaveBeenCalledWith(file.name);
  expect(post).toHaveBeenCalledWith(
    '/requests/api-call',
    {
      method: 'GET',
      authIndex: 'test-index',
      url: 'https://api.github.com/copilot_internal/user',
      header: {
        Authorization: 'Bearer test-oauth',
        'User-Agent': 'copilot/1.0.114',
        Accept: 'application/json',
      },
    },
    undefined
  );
  expect(COPILOT_CONFIG.buildSuccessState(data)).toEqual({
    status: 'success',
    data: { remaining: 1150.9, entitlement: 20000, percentRemaining: 5.7 },
  });
});

test('zero is valid but missing, unlimited and invalid windows are not exhausted quotas', () => {
  expect(
    parseCopilotQuota(payload({ ...snapshot, quota_remaining: 0, percent_remaining: 0 }))?.remaining
  ).toBe(0);
  for (const premium of [
    undefined,
    {},
    { ...snapshot, unlimited: true },
    { ...snapshot, percent_remaining: null },
  ]) {
    expect(parseCopilotQuota(payload(premium))).toBeNull();
  }
});

test('renders reported percentages independently of has_quota and optional counts', () => {
  const classes = bindQuotaClasses(Object.fromEntries(QUOTA_CLASS_KEYS.map((k) => [k, k])), 'test');
  const render = (premium: unknown) => {
    const data = parseCopilotQuota(payload(premium));
    return renderToStaticMarkup(
      createElement(CopilotQuotaBody, {
        quota: { status: 'success', data: data ?? undefined },
        classes,
      })
    );
  };
  for (const counts of [
    {},
    { entitlement: '100' },
    { quota_remaining: null },
    { quota_remaining: '0', entitlement: '0' },
  ]) {
    const markup = render({ percent_remaining: ' 0 ', has_quota: false, ...counts });
    expect(markup).toContain('aria-valuenow="0"');
    expect(markup).not.toContain('quotaAmount');
  }
  const markup = render({ percent_remaining: '0.5', entitlement: '100', quota_remaining: '0' });
  expect(markup).toContain('aria-valuenow="0.5"');
  expect(markup).toContain('0 / 100');
  for (const premium of [
    {},
    { ...snapshot, unlimited: true },
    ...[' ', false, 'NaN', -1, 101].map((percent_remaining) => ({ percent_remaining })),
  ]) {
    const unavailable = render(premium);
    expect(unavailable).not.toContain('role="meter"');
    expect(unavailable).not.toContain('quotaAmount');
  }
});

test('missing token or changed file/session never forwards the credential', async () => {
  spyOn(apiClient, 'captureConnection').mockReturnValue(apiClient);
  const post = spyOn(apiClient, 'post');
  const download = spyOn(authFilesApi, 'downloadText');
  for (const mode of ['missing', 'file', 'session']) {
    download.mockImplementation(async () => {
      if (mode === 'missing') return '{"type":"copilot","access_token":"test-inference"}';
      useQuotaStore.getState().clearQuotaCache(mode === 'file' ? [file.name] : undefined);
      return credential;
    });
    await expect(COPILOT_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
      i18n.t('copilot_quota.unavailable')
    );
  }
  expect(post).not.toHaveBeenCalled();
});

test('transport errors expose neither the token nor upstream response details', async () => {
  spyOn(apiClient, 'captureConnection').mockReturnValue(apiClient);
  spyOn(authFilesApi, 'downloadText').mockResolvedValue(credential);
  const post = spyOn(apiClient, 'post').mockResolvedValue({ status_code: 401, body: credential });
  const safeError = new Error(i18n.t('copilot_quota.unavailable'));
  await expect(COPILOT_CONFIG.fetchQuota(file, i18n.t)).rejects.toEqual(safeError);
  post.mockRejectedValue(new Error(credential));
  await expect(COPILOT_CONFIG.fetchQuota(file, i18n.t)).rejects.toEqual(safeError);
});
