import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  getAuthFileIcon,
  OAUTH_PROVIDER_PRESETS,
  QUOTA_PROVIDER_TYPES,
  supportsAuthFileManualRefresh,
  supportsAuthFileWebsockets,
} from '@/features/authFiles/constants';
import { providerLabel } from '@/features/dashboard/utils';
import { PROVIDER_LOGOS } from '@/features/providers/brandLogos';
import { apiClient } from '@/services/api/client';
import { oauthApi, type BuiltInOAuthProvider } from '@/services/api/oauth';
import { classifyModels } from '@/utils/models';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

describe('Muse (Meta) provider and device OAuth', () => {
  test('uses the backend meta device endpoint and preserves the device code', async () => {
    const provider: BuiltInOAuthProvider = 'meta';
    const response = {
      url: 'https://example.com/device',
      state: 'meta-fixture',
      flow: 'device',
      user_code: 'FIXTURE-CODE',
      expires_in: 600,
    };
    const signal = new AbortController().signal;
    const get = spyOn(apiClient, 'get').mockResolvedValue(response);
    try {
      expect(await oauthApi.startAuth(provider, signal)).toEqual(response);
      expect(get).toHaveBeenLastCalledWith('/meta-auth-url', { params: undefined, signal });
      await oauthApi.startAuth('Muse');
      expect(get).toHaveBeenLastCalledWith('/meta-auth-url', { params: undefined });
    } finally {
      get.mockRestore();
    }
  });

  test('recognizes both names and quota support without enabling websockets', () => {
    expect(normalizeOAuthProviderKey(' Muse ')).toBe('meta');
    expect(OAUTH_PROVIDER_PRESETS).toContain('meta');
    expect(getAuthFileIcon('muse', 'dark')).toBe(PROVIDER_LOGOS.meta.src);
    expect(getAuthFileIcon('meta', 'light')).toBeTruthy();
    expect(providerLabel('meta', 'Unknown')).toBe('Muse (Meta)');
    expect(supportsAuthFileManualRefresh('meta')).toBe(true);
    expect(supportsAuthFileWebsockets('meta')).toBe(false);
    expect([...QUOTA_PROVIDER_TYPES]).toContain('meta');
  });

  test('classifies Muse models without changing Devin namespace precedence', () => {
    const groups = classifyModels([
      { name: 'muse-spark-1.3' },
      { name: 'muse-latest' },
      { name: 'devin/muse-spark-1.3' },
      { name: 'gpt-5' },
    ]);
    expect(groups.find((group) => group.id === 'meta')?.items).toHaveLength(2);
    expect(groups.find((group) => group.id === 'devin')?.items).toHaveLength(1);
    expect(groups.find((group) => group.id === 'gpt')?.items).toHaveLength(1);
  });

  test('registers device-code UI but not a manual OAuth callback', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'meta'");
    expect(source).toContain('userCode: res.user_code');
    expect(source).toContain("t('auth_login.device_code_copy')");
    const callbackProviders = source.match(
      /const CALLBACK_SUPPORTED = new Set<string>\(([^;]+)\);/
    );
    expect(callbackProviders?.[1]).not.toContain("'meta'");
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
        expect(translations.auth_login[`meta_${suffix}`]).toBeTruthy();
      }
      expect(translations.auth_files.filter_meta).toBe('Muse (Meta)');
      expect(translations.providersPage.providerNames.meta).toBe('Muse (Meta)');
      expect(translations.auth_login.device_code_label).toBeTruthy();
      expect(translations.auth_login.device_code_copy).toBeTruthy();
    }
  });
});
