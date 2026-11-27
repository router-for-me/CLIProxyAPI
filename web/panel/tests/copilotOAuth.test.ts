import { expect, spyOn, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { oauthApi } from '@/services/api/oauth';
import { OAUTH_PROVIDERS, supportsManualCallback } from '@/features/oauth/providers';
import {
  buildOAuthProviderOptions,
  getAuthFileIcon,
  supportsAuthFileManualRefresh,
} from '@/features/authFiles/constants';

test('GitHub Copilot starts a device session without browser callback parameters', async () => {
  const card = OAUTH_PROVIDERS.find(({ id }) => id === 'copilot');
  if (!card) throw new Error('Missing GitHub Copilot login card');
  expect(card.flow).toBe('device');
  expect(supportsManualCallback(card)).toBe(false);
  const response = { url: 'https://example.test/device', user_code: 'TEST-CODE', state: 'test' };
  const get = spyOn(apiClient, 'get').mockResolvedValue(response);
  try {
    expect(await oauthApi.startAuth(card.id)).toEqual(response);
    expect(get).toHaveBeenCalledWith('/oauth/auth-url', { params: { provider: 'copilot' } });
  } finally {
    get.mockRestore();
  }
});

test('Copilot credentials expose model rules, branding and token refresh', () => {
  expect(buildOAuthProviderOptions([])).toContain('copilot');
  expect(getAuthFileIcon('copilot', 'dark')).toBeTruthy();
  expect(supportsAuthFileManualRefresh('copilot')).toBe(true);
});
