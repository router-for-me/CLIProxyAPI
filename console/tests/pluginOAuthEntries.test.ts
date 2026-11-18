import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { apiClient } from '@/services/api/client';
import { pluginsApi } from '@/services/api/plugins';
import { buildPluginOAuthProviderCards } from '@/features/oauth/providers';
import { resolveQuotaPluginId } from '@/features/quota/providers/plugin/data';
import type { PluginListEntry } from '@/types';

const spies: Array<{ mockRestore(): void }> = [];
const mockGet = (value: unknown) => {
  const spy = spyOn(apiClient, 'get').mockResolvedValue(value as never);
  spies.push(spy);
  return spy;
};
afterEach(() => spies.splice(0).forEach((spy) => spy.mockRestore()));

const plugin = (overrides: Partial<PluginListEntry> = {}): PluginListEntry => ({
  id: 'example',
  path: '/plugins/example',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: true,
  logo: '',
  configFields: [],
  menus: [],
  metadata: null,
  ...overrides,
});

const listResponse = (entries: Array<Record<string, unknown>>) => ({
  plugins_enabled: true,
  plugins_dir: 'plugins',
  plugins: entries,
});

describe('plugin OAuth entry normalization', () => {
  test('reads every declared entry with its own name and logo', async () => {
    mockGet(
      listResponse([
        {
          id: 'omc-zcode-oauth',
          supports_oauth: true,
          oauth_provider: 'zcode-zai',
          oauth_providers: ['zcode-zai', 'zcode-bigmodel'],
          oauth_provider_entries: [
            { identifier: 'zcode-zai', name: 'Z.ai', logo: 'https://chat.z.ai/zai.png' },
            {
              identifier: 'zcode-bigmodel',
              name: 'BigModel',
              logo: 'https://bigmodel.cn/bigmodel.png',
            },
          ],
          logo: 'https://cdn.example/logo.png',
        },
      ])
    );
    const [entry] = (await pluginsApi.list()).plugins;
    expect(entry?.oauthProvider).toBe('zcode-zai');
    expect(entry?.oauthProviders).toEqual(['zcode-zai', 'zcode-bigmodel']);
    expect(entry?.oauthProviderEntries).toEqual([
      { identifier: 'zcode-zai', name: 'Z.ai', logo: 'https://chat.z.ai/zai.png' },
      { identifier: 'zcode-bigmodel', name: 'BigModel', logo: 'https://bigmodel.cn/bigmodel.png' },
    ]);
  });

  test('decodes backend-escaped entry metadata before building cards', async () => {
    // GET /plugins uses Go html.EscapeString for entry names and logo URLs.
    mockGet(
      listResponse([
        {
          id: 'multi',
          supports_oauth: true,
          effective_enabled: true,
          oauth_providers: ['portal'],
          logo: 'https://example.test/default.svg',
          oauth_provider_entries: [
            {
              identifier: 'portal',
              name: ' Z.ai &lt;International&gt; &amp; &#34;Team&#34; &#39;云端&#39; ',
              logo: ' https://example.test/logo.svg?a=1&amp;b=2&amp;sig=x%2By%3D ',
            },
          ],
        },
      ])
    );
    const cards = buildPluginOAuthProviderCards(
      (await pluginsApi.list()).plugins,
      'https://proxy.test'
    );
    expect(cards[0].title).toBe('Z.ai <International> & "Team" \'云端\'');
    expect(cards[0].icon).toBe('https://example.test/logo.svg?a=1&b=2&sig=x%2By%3D');
    expect(new URL(cards[0].icon).searchParams.get('b')).toBe('2');
    // Decoding restores text, not trusted HTML; React still escapes it at render time.
    const markup = renderToStaticMarkup(createElement('span', null, cards[0].title));
    expect(markup).toContain('&lt;International&gt;');
    expect(markup).not.toContain('<International>');
  });

  test('decodes exactly once and preserves literal entities and URL encoding', async () => {
    mockGet(
      listResponse([
        {
          id: 'multi',
          oauth_providers: ['portal'],
          oauth_provider_entries: [
            {
              identifier: 'portal',
              name: 'Literal &amp;lt;tag&amp;gt; &amp;#39; &amp;amp;',
              logo: '/logo.svg?literal=&amp;amp;&amp;encoded=%26amp%3B',
            },
          ],
        },
      ])
    );
    const [entry] = (await pluginsApi.list()).plugins;
    expect(entry.oauthProviderEntries).toEqual([
      {
        identifier: 'portal',
        name: 'Literal &lt;tag&gt; &#39; &amp;',
        logo: '/logo.svg?literal=&amp;&encoded=%26amp%3B',
      },
    ]);
  });

  test('normalizes identifiers per element and drops undeclared entries', async () => {
    mockGet(
      listResponse([
        {
          id: 'multi',
          supports_oauth: true,
          oauth_providers: ['Portal-A', ' portal-a ', 'muse', 'bad/id', '', 'portal-b'],
          oauth_provider_entries: [
            { identifier: 'Portal-A', name: ' A ' },
            { identifier: 'portal-a', name: 'duplicate' },
            { identifier: 'portal-c', name: 'not declared' },
            { identifier: 'bad/id', name: 'invalid' },
            { identifier: 'portal-b' },
            'not-an-entry',
          ],
        },
      ])
    );
    const [entry] = (await pluginsApi.list()).plugins;
    expect(entry?.oauthProviders).toEqual(['portal-a', 'meta', 'portal-b']);
    expect(entry?.oauthProviderEntries).toEqual([
      { identifier: 'portal-a', name: 'A' },
      { identifier: 'portal-b' },
    ]);
  });

  test('omits the plural fields when the backend only publishes oauth_provider', async () => {
    mockGet(listResponse([{ id: 'single', supports_oauth: true, oauth_provider: 'example' }]));
    const [entry] = (await pluginsApi.list()).plugins;
    expect(entry?.oauthProvider).toBe('example');
    expect(entry?.oauthProviders).toBeUndefined();
    expect(entry?.oauthProviderEntries).toBeUndefined();
  });
});

describe('plugin OAuth tiles per declared entry', () => {
  const apiBase = 'http://127.0.0.1:8317';

  test('renders one card per entry with its own title and mark', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({
          id: 'omc-zcode-oauth',
          oauthProvider: 'zcode-zai',
          oauthProviders: ['zcode-zai', 'zcode-bigmodel'],
          oauthProviderEntries: [
            { identifier: 'zcode-zai', name: 'Z.ai', logo: 'https://chat.z.ai/zai.png' },
            { identifier: 'zcode-bigmodel', name: 'BigModel', logo: '/static/bigmodel.png' },
          ],
          logo: 'https://cdn.example/logo.png',
        }),
      ],
      apiBase
    );
    expect(cards.map((card) => [card.id, card.title, card.icon])).toEqual([
      ['zcode-zai', 'Z.ai', 'https://chat.z.ai/zai.png'],
      ['zcode-bigmodel', 'BigModel', `${apiBase}/static/bigmodel.png`],
    ]);
  });

  test('titles entries without metadata with the plugin name and plugin logo', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({
          id: 'legacy-multi',
          oauthProviders: ['portal-a', 'portal-b'],
          logo: 'https://cdn.example/logo.png',
        }),
      ],
      apiBase
    );
    expect(cards.map((card) => [card.id, card.title, card.icon])).toEqual([
      ['portal-a', 'legacy-multi', 'https://cdn.example/logo.png'],
      ['portal-b', 'legacy-multi', 'https://cdn.example/logo.png'],
    ]);
  });

  test('keeps one card for a single-entry plugin', () => {
    const cards = buildPluginOAuthProviderCards(
      [plugin({ id: 'single', oauthProvider: 'example', logo: '/logo.png' })],
      apiBase
    );
    expect(cards).toEqual([
      { kind: 'plugin', id: 'example', title: 'single', icon: `${apiBase}/logo.png` },
    ]);
  });

  test('keeps the plugin-id fallback when only supports_oauth is set', () => {
    const cards = buildPluginOAuthProviderCards(
      [plugin({ id: 'plugin-id-only', oauthProvider: 'plugin-id-only' })],
      apiBase
    );
    expect(cards.map((card) => [card.id, card.icon])).toEqual([['plugin-id-only', '']]);
  });

  test('never lets a second plugin claim a declared identifier twice', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({ id: 'first', oauthProviders: ['shared', 'first-only'] }),
        plugin({ id: 'second', oauthProviders: ['shared', 'second-only'] }),
      ],
      apiBase
    );
    expect(cards.map((card) => card.id)).toEqual(['shared', 'first-only', 'second-only']);
  });

  test('keeps an entry without metadata on the plugin title', () => {
    const cards = buildPluginOAuthProviderCards(
      [
        plugin({
          id: 'omc-zcode-oauth',
          oauthProviders: ['zcode-zai', 'zcode-bigmodel'],
          oauthProviderEntries: [{ identifier: 'zcode-bigmodel', name: 'BigModel' }],
        }),
      ],
      apiBase
    );
    // 每个磁贴只带自己那一条：id 就是登录路由的 provider 参数
    expect(cards.map((card) => [card.id, card.title])).toEqual([
      ['zcode-zai', 'omc-zcode-oauth'],
      ['zcode-bigmodel', 'BigModel'],
    ]);
  });
});

describe('plugin quota resolution for secondary portals', () => {
  test('matches any declared OAuth identifier before the sole-plugin guess', () => {
    const plugins = [
      plugin({
        id: 'zcode',
        supportsOAuth: false,
        supportsQuota: true,
        quotaProvider: 'other',
        oauthProviders: ['zcode-zai', 'zcode-bigmodel'],
      }),
      plugin({
        id: 'unrelated',
        supportsOAuth: false,
        supportsQuota: true,
        quotaProvider: 'other-2',
      }),
    ];
    expect(resolveQuotaPluginId(plugins, 'zcode-bigmodel')).toBe('zcode');
    expect(resolveQuotaPluginId(plugins, ' ZCode-BigModel ')).toBe('zcode');
    expect(resolveQuotaPluginId(plugins, 'unknown')).toBeNull();
  });
});
