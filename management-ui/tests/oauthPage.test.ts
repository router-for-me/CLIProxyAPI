import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import ko from '@/i18n/locales/ko.json';
import vi from '@/i18n/locales/vi.json';
import { OAuthPage } from '@/features/oauth/OAuthPage';
import { resolveCallbackUrl, XAI_CALLBACK_URL } from '@/features/oauth/callbackUrl';
import { resolveFlowView, splitAuthUrl } from '@/features/oauth/flowView';
import { buildPluginOAuthProviderCards, OAUTH_PROVIDERS } from '@/features/oauth/providers';
import {
  KIMI_CHINESE_AFFILIATE_URL,
  KIMI_INTERNATIONAL_AFFILIATE_URL,
} from '@/features/providers/kimi';
import type { PluginListEntry } from '@/types';

describe('OAuth dialog view resolution', () => {
  test('maps every flow state to exactly one view', () => {
    expect(resolveFlowView({})).toBe('idle');
    // 正在生成链接：已进入步骤视图（骨架占位）
    expect(resolveFlowView({ status: 'waiting' })).toBe('flow');
    expect(resolveFlowView({ status: 'waiting', url: 'https://a.test' })).toBe('flow');
    expect(resolveFlowView({ status: 'success' })).toBe('success');
    // 轮询网络失败但链接仍在：留在步骤视图，手动回调仍可恢复
    expect(resolveFlowView({ status: 'error', url: 'https://a.test' })).toBe('flow');
    // 链接已失效（启动失败 / Devin 终态失败）：失败视图
    expect(resolveFlowView({ status: 'error', error: 'denied' })).toBe('failed');
  });

  test('highlights the host of the authorization link', () => {
    expect(splitAuthUrl('https://auth.openai.com/oauth/authorize?x=1')).toEqual({
      host: 'auth.openai.com',
      rest: '/oauth/authorize?x=1',
    });
    expect(splitAuthUrl('http://127.0.0.1:8317/cb')).toEqual({
      host: '127.0.0.1:8317',
      rest: '/cb',
    });
    expect(splitAuthUrl('not a url')).toEqual({ host: '', rest: 'not a url' });
  });
});

describe('manual callback input', () => {
  test('submits non-xAI input verbatim (trimmed)', () => {
    expect(resolveCallbackUrl('codex', '  http://localhost:1455/auth/callback?code=c  ')).toBe(
      'http://localhost:1455/auth/callback?code=c'
    );
  });

  test('passes absolute xAI callback URLs through', () => {
    const url = 'http://127.0.0.1:56121/callback?code=c&state=s';
    expect(resolveCallbackUrl('xai', url, 'other')).toBe(url);
  });

  test('builds the xAI loopback URL from a displayed code and the attempt state', () => {
    const built = new URL(resolveCallbackUrl('xai', 'abc123', 'st-1')!);
    expect(`${built.origin}${built.pathname}`).toBe(XAI_CALLBACK_URL);
    expect(built.searchParams.get('code')).toBe('abc123');
    expect(built.searchParams.get('state')).toBe('st-1');
    expect(new URL(resolveCallbackUrl('xai', 'code: xyz', 'st-1')!).searchParams.get('code')).toBe(
      'xyz'
    );
  });

  test('prefers the pasted query state and keeps provider errors', () => {
    const built = new URL(
      resolveCallbackUrl('xai', '?error=access_denied&error_description=no&state=pasted', 'st')!
    );
    expect(built.searchParams.get('state')).toBe('pasted');
    expect(built.searchParams.get('error')).toBe('access_denied');
    expect(built.searchParams.get('error_description')).toBe('no');
  });

  test('refuses to invent a state for xAI', () => {
    expect(resolveCallbackUrl('xai', 'abc123')).toBeNull();
    expect(resolveCallbackUrl('xai', 'code=abc123')).toBeNull();
    expect(resolveCallbackUrl('xai', '   ', 'st')).toBeNull();
  });
});

const plugin = (overrides: Partial<PluginListEntry>): PluginListEntry => ({
  id: 'example',
  path: '/plugins/example',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: true,
  oauthProvider: 'example',
  supportsQuota: false,
  logo: '',
  configFields: [],
  menus: [],
  metadata: null,
  ...overrides,
});

describe('plugin OAuth tiles', () => {
  test('appends enabled OAuth plugins once and never shadows built-in providers', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({ id: 'a', oauthProvider: 'acme' }),
        plugin({ id: 'a-dup', oauthProvider: 'acme' }),
        plugin({ id: 'b', oauthProvider: 'codex' }),
        plugin({ id: 'c', oauthProvider: 'off', effectiveEnabled: false }),
        plugin({ id: 'd', oauthProvider: 'nooauth', supportsOAuth: false }),
        plugin({ id: 'e', oauthProvider: undefined }),
      ],
      'http://127.0.0.1:8317'
    );
    expect(cards.map((card) => [card.id, card.title])).toEqual([['acme', 'a']]);
  });
});

describe('OAuth provider gallery', () => {
  const locales = { en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru, ko, vi } as const;

  for (const [lng, translation] of Object.entries(locales)) {
    test(`renders every provider tile without raw keys (${lng})`, async () => {
      const i18n = createInstance();
      await i18n.init({ lng, resources: { [lng]: { translation } } });
      const markup = renderToStaticMarkup(
        createElement(
          I18nextProvider,
          { i18n },
          createElement(MemoryRouter, null, createElement(OAuthPage))
        )
      );
      for (const provider of OAUTH_PROVIDERS) {
        const label =
          typeof provider.label === 'string' ? provider.label : i18n.t(provider.label.key);
        expect(markup).toContain(label);
      }
      expect(markup).toContain('Vertex AI');
      expect(markup).not.toMatch(/auth_login\.|vertex_import\./);
      // 每个磁贴都是打开对话框的按钮
      expect(markup.match(/aria-haspopup="dialog"/g)?.length).toBe(OAUTH_PROVIDERS.length + 1);
    });
  }

  test('merges both Kimi sites into one sponsor card with per-site entries', async () => {
    const i18n = createInstance();
    await i18n.init({ lng: 'zh-CN', resources: { 'zh-CN': { translation: zhCN } } });
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(MemoryRouter, null, createElement(OAuthPage))
      )
    );
    // 一张品牌卡（role=group），卡内两个站点登录入口 + 两个开放平台注册链接
    expect(markup.match(/role="group"/g)?.length).toBe(1);
    const card = markup.slice(markup.indexOf('role="group"'));
    expect(card).toContain('aria-label="Kimi 国内站"');
    expect(card).toContain('aria-label="Kimi 国际站"');
    expect(card).toContain('>国内站</button>');
    expect(card).toContain('>国际站</button>');
    expect(card.match(/aria-label="注册开放平台 · Kimi /g)?.length).toBe(2);
  });

  test('keeps each visible Kimi site name inside its accessible name', () => {
    const sponsors = OAUTH_PROVIDERS.filter((provider) => provider.sponsor);
    expect(sponsors.map((provider) => provider.id)).toEqual(['kimi', 'kimi-ai']);
    for (const translation of Object.values(locales)) {
      const messages = translation.auth_login as Record<string, string>;
      for (const provider of sponsors) {
        const nameKey = typeof provider.label === 'string' ? '' : provider.label.key;
        const name = messages[nameKey.replace('auth_login.', '')];
        const region = messages[provider.sponsor!.siteKey.replace('auth_login.', '')];
        expect(region).toBeTruthy();
        // label-in-name：语音控制说出可见文字就能命中按钮
        expect(name.toLowerCase()).toContain(region.toLowerCase());
      }
      expect(messages.sponsor_kimi_caption).toBeTruthy();
      expect(messages.sponsor_sign_up).toBeTruthy();
    }
  });

  test('overrides per-pool animations with equal specificity under reduced motion', () => {
    const styles = readFileSync('src/features/oauth/components/SponsorCard.module.scss', 'utf8');
    const reducedMotionStart = styles.indexOf('@media (prefers-reduced-motion: reduce)');
    const poolAnimationStart = styles.indexOf(".pool[data-pool='#{$name}']");
    expect(poolAnimationStart).toBeGreaterThanOrEqual(0);
    expect(reducedMotionStart).toBeGreaterThan(poolAnimationStart);
    // 属性选择器与每个光池的动画规则保持同等优先级；仅 .pool 无法覆盖动画。
    expect(styles.slice(reducedMotionStart)).toMatch(
      /\.pool\[data-pool\]\s*,[^{}]*\{\s*animation:\s*none;/
    );
  });

  test('keeps both Kimi registration links as safe external links', async () => {
    const i18n = createInstance();
    await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
    const markup = renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(MemoryRouter, null, createElement(OAuthPage))
      )
    );
    for (const url of [KIMI_CHINESE_AFFILIATE_URL, KIMI_INTERNATIONAL_AFFILIATE_URL]) {
      const escaped = url.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/&/g, '&amp;');
      expect(markup).toMatch(
        new RegExp(`href="${escaped}" target="_blank" rel="noopener noreferrer"`)
      );
    }
  });
});
