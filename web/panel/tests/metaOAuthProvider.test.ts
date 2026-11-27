import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import en from '@/i18n/locales/en.json';
import { OAuthFlowDialog } from '@/features/oauth/components/OAuthFlowDialog';
import {
  CALLBACK_SUPPORTED,
  OAUTH_PROVIDERS,
  supportsManualCallback,
} from '@/features/oauth/providers';
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
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });

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
      expect(get).toHaveBeenLastCalledWith('/oauth/auth-url', {
        params: { provider: 'meta' },
        signal,
      });
      await oauthApi.startAuth('Muse');
      expect(get).toHaveBeenLastCalledWith('/oauth/auth-url', { params: { provider: 'meta' } });
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

  test('registers device-code UI but not a manual OAuth callback', () => {
    const meta = OAUTH_PROVIDERS.find((provider) => provider.id === 'meta');
    expect(meta).toMatchObject({ flow: 'device', label: 'Muse (Meta)' });
    expect(CALLBACK_SUPPORTED.has('meta')).toBe(false);
    expect(meta && supportsManualCallback(meta)).toBe(false);

    // 设备码步骤真实渲染：码本身 + 复制按钮，且没有回调步骤
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(OAuthFlowDialog, {
          open: true,
          providerId: 'meta',
          heading: { title: en.auth_login.meta_oauth_title, caption: 'Device code', icon: '' },
          state: { status: 'waiting', url: 'https://meta.example/device', userCode: 'WDJB-MJHT' },
          text: (suffix: string) => i18n.t(`auth_login.meta_${suffix}`),
          supportsCallback: false,
          onStart: () => {},
          onCancel: () => {},
          onCallbackChange: () => {},
          onCallbackSubmit: () => {},
          onViewAuthFiles: () => {},
          onClose: () => {},
        })
      )
    );
    expect(markup).toContain('WDJB-MJHT');
    expect(markup).toContain(en.auth_login.device_code_copy);
    expect(markup).toContain(en.auth_login.step_enter_device_code);
    expect(markup).not.toContain(en.auth_login.oauth_callback_label);
    expect(markup).not.toContain('auth_login.');
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      expect(translations.auth_files.filter_meta).toBe('Muse (Meta)');
      expect(translations.providersPage.providerNames.meta).toBe('Muse (Meta)');
    }
  });
});
