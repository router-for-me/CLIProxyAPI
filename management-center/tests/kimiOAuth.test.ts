import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { apiClient } from '@/services/api/client';
import { oauthApi } from '@/services/api/oauth';
import { createOAuthAttempts } from '@/pages/oauthAttempts';
import {
  KIMI_CHINESE_AFFILIATE_URL,
  KIMI_INTERNATIONAL_AFFILIATE_URL,
} from '@/features/providers/kimi';

describe('Kimi regional login', () => {
  test('uses separate management endpoints and preserves cancellation', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({ url: 'https://example.test' });
    const controller = new AbortController();
    try {
      await oauthApi.startAuth('kimi', controller.signal);
      expect(get).toHaveBeenLastCalledWith('/kimi-auth-url', {
        params: undefined,
        signal: controller.signal,
      });
      await oauthApi.startAuth('kimi-ai', controller.signal);
      expect(get).toHaveBeenLastCalledWith('/kimi-ai-auth-url', {
        params: undefined,
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

  test('offers both cards with site-specific registration links', () => {
    const source = readFileSync('src/pages/OAuthPage.tsx', 'utf8');
    expect(source).toContain("id: 'kimi-ai'");
    expect(source).toContain("id: 'kimi'");
    expect(source).toMatch(
      /provider.id === 'kimi-ai'\s*\? KIMI_INTERNATIONAL_AFFILIATE_URL\s*: KIMI_CHINESE_AFFILIATE_URL/
    );
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
