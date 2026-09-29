import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import { providerLabel } from '@/features/dashboard/utils';
import { PROVIDER_LOGOS } from '@/features/providers/brandLogos';
import { apiClient } from '@/services/api/client';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api/oauth';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

describe('Cline provider and device OAuth', () => {
  test('uses the backend cline device endpoint without a webui callback param', async () => {
    const provider: BuiltInOAuthProvider = 'cline';
    const response = {
      status: 'ok',
      url: 'https://example.com/device',
      state: 'cline-fixture',
      flow: 'device',
      user_code: 'CLINE-CODE',
      expires_in: 600,
    };
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider, signal)).toEqual(response);
      expect(get).toHaveBeenLastCalledWith('/cline-auth-url', { params: undefined, signal });
      await oauthApi.startAuth('Cline');
      expect(get).toHaveBeenLastCalledWith('/cline-auth-url', { params: undefined });
    } finally {
      get.mockRestore();
    }
  });

  test('resolves names and icons for Cline tokens', () => {
    expect(normalizeOAuthProviderKey(' Cline ')).toBe('cline');
    expect(getAuthFileIcon('cline', 'dark')).toBeTruthy();
    expect(getAuthFileIcon('cline', 'light')).toBe(getAuthFileIcon('cline', 'dark'));
    expect(readFileSync('src/assets/icons/cline.svg', 'utf8')).toContain('<svg');
    expect(providerLabel('cline', 'Unknown')).toBe('Cline');
    expect(providerLabel('mirasim', 'Unknown')).toBe('Mirasim');
    expect(PROVIDER_LOGOS.mirasim.src).toBeTruthy();
  });

  test('registers the device-flow card without a manual OAuth callback', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'cline'");
    expect(source).toContain('auth_login.cline_oauth_title');
    const callbackProviders = source.match(
      /const CALLBACK_SUPPORTED = new Set<string>\(([^;]+)\);/
    );
    expect(callbackProviders?.[1]).not.toContain("'cline'");
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      for (const suffix of [
        'oauth_title',
        'oauth_button',
        'oauth_hint',
        'oauth_url_label',
        'open_link',
        'copy_link',
        'oauth_status_waiting',
        'oauth_status_success',
        'oauth_status_error',
        'oauth_start_error',
        'oauth_polling_error',
      ]) {
        expect(translations.auth_login[`cline_${suffix}`]).toBeTruthy();
      }
      expect(translations.auth_files.filter_cline).toBe('Cline');
      expect(translations.auth_login.device_code_label).toBeTruthy();
      expect(translations.auth_login.device_code_copy).toBeTruthy();
      expect(translations.providersPage.providerNames.mirasim).toBe('Mirasim');
    }
  });
});
