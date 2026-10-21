import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { apiClient } from '@/services/api/client';
import { oauthApi } from '@/services/api/oauth';
import { createOAuthAttempts } from '@/features/oauth/oauthAttempts';
import { isSponsor, OAUTH_PROVIDERS } from '@/features/oauth/providers';
import {
  KIMI_CHINESE_AFFILIATE_URL,
  KIMI_INTERNATIONAL_AFFILIATE_URL,
} from '@/features/providers/kimi';

describe('Kimi regional login', () => {
  test('uses separate provider parameters and preserves cancellation', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({ url: 'https://example.test' });
    const controller = new AbortController();
    try {
      await oauthApi.startAuth('kimi', controller.signal);
      expect(get).toHaveBeenLastCalledWith('/oauth/auth-url', {
        params: { provider: 'kimi' },
        signal: controller.signal,
      });
      await oauthApi.startAuth('kimi-ai', controller.signal);
      expect(get).toHaveBeenLastCalledWith('/oauth/auth-url', {
        params: { provider: 'kimi-ai' },
        signal: controller.signal,
      });
    } finally {
      get.mockRestore();
    }
  });

  test('keeps regional login attempts independent', () => {
    const attempts = createOAuthAttempts({ setTimeout: () => 0, clearTimeout: () => {} });
    try {
      const china = attempts.begin('kimi');
      const international = attempts.begin('kimi-ai');
      attempts.begin('kimi-ai');
      expect(china.signal.aborted).toBe(false);
      expect(international.signal.aborted).toBe(true);
    } finally {
      attempts.invalidateAll();
    }
  });

  test('offers both tiles with site-specific registration links', () => {
    const kimi = OAUTH_PROVIDERS.find((provider) => provider.id === 'kimi');
    const kimiAi = OAUTH_PROVIDERS.find((provider) => provider.id === 'kimi-ai');
    expect(kimi).toMatchObject({ flow: 'device', domain: 'kimi.com' });
    expect(kimiAi).toMatchObject({ flow: 'device', domain: 'kimi.ai' });
    expect(kimi?.sponsor?.signUpUrl).toBe(KIMI_CHINESE_AFFILIATE_URL);
    expect(kimiAi?.sponsor?.signUpUrl).toBe(KIMI_INTERNATIONAL_AFFILIATE_URL);
    // 赞助商：两站排在画廊最前，且只有它们是赞助位
    expect(OAUTH_PROVIDERS.slice(0, 2).map((provider) => provider.id)).toEqual(['kimi', 'kimi-ai']);
    expect(OAUTH_PROVIDERS.filter(isSponsor).map((provider) => provider.id)).toEqual([
      'kimi',
      'kimi-ai',
    ]);
    expect(new URL(KIMI_CHINESE_AFFILIATE_URL).hostname).toBe('platform.kimi.com');
    expect(new URL(KIMI_INTERNATIONAL_AFFILIATE_URL).hostname).toBe('platform.kimi.ai');
  });

  for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
    test(`provides complete regional login translations (${locale})`, () => {
      const { auth_login: messages } = JSON.parse(
        readFileSync(`src/i18n/locales/${locale}.json`, 'utf8')
      ) as { auth_login: Record<string, string> };
      for (const key of Object.keys(messages).filter((key) => key.startsWith('kimi_'))) {
        if (key.startsWith('kimi_ai_') || key === 'kimi_sign_up_button') continue;
        expect(messages[key.replace('kimi_', 'kimi_ai_')]).toBeTruthy();
      }
      expect(messages.kimi_oauth_title).toContain('kimi.com');
      expect(messages.kimi_ai_oauth_title).toContain('kimi.ai');
    });
  }
});
