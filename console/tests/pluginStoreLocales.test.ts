import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { apiClient } from '@/services/api/client';
import { pluginStoreApi } from '@/services/api/plugins';
import { getStoreEntryDisplay } from '@/features/plugins/pluginStoreMetadata';
import { filterStoreEntries, getStoreSearchText } from '@/features/plugins/pluginSearch';
import type { PluginStoreEntry } from '@/types';

const spies: Array<{ mockRestore(): void }> = [];
const mockGet = (value: unknown) => {
  const spy = spyOn(apiClient, 'get').mockResolvedValue(value as never);
  spies.push(spy);
  return spy;
};
afterEach(() => spies.splice(0).forEach((spy) => spy.mockRestore()));

const readSource = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');

const storeEntry = (overrides: Partial<PluginStoreEntry>): PluginStoreEntry => ({
  storeId: 'official/example',
  sourceId: 'official',
  sourceName: 'official',
  sourceUrl: '',
  id: 'example',
  name: 'Example',
  description: 'An English description.',
  author: 'someone',
  version: '1.0.0',
  repository: 'router-for-me/example-plugin',
  installType: 'github-release',
  authRequired: false,
  authConfigured: false,
  platforms: [],
  logo: '',
  homepage: 'https://example.test',
  license: 'MIT',
  tags: ['Interceptor'],
  installed: false,
  installedVersion: '',
  path: '',
  configured: false,
  registered: false,
  enabled: false,
  effectiveEnabled: false,
  updateAvailable: false,
  ...overrides,
});

const unrelatedEntry = (id: string) =>
  storeEntry({
    storeId: `official/${id}`,
    id,
    name: id.toUpperCase(),
    description: 'Unrelated.',
    author: 'nobody',
    repository: `router-for-me/${id}`,
    license: 'Apache-2.0',
    tags: [],
  });

describe('plugin store entry normalization', () => {
  test('keeps backend locales on normalized entries', async () => {
    mockGet({
      plugins_enabled: true,
      plugins_dir: 'plugins',
      plugins: [
        {
          id: 'example',
          name: 'Example',
          description: 'An English description.',
          locales: {
            'zh-CN': { description: '中文描述。 ' },
            'zh-TW': {},
          },
        },
      ],
    });
    const [entry] = (await pluginStoreApi.list()).plugins;
    // 后端已把键归一化成 BCP-47，空块由这里丢弃。
    expect(entry.locales).toEqual({ 'zh-CN': { description: '中文描述。' } });
  });

  test('drops empty and blank-key locale blocks like the backend does', async () => {
    mockGet({
      plugins_enabled: true,
      plugins_dir: 'plugins',
      plugins: [
        {
          id: 'example',
          locales: {
            '': { description: 'lost' },
            fr: {},
            de: { description: '   ' },
            en: { name: '', homepage: 'https://example.test/en' },
          },
        },
      ],
    });
    const [entry] = (await pluginStoreApi.list()).plugins;
    expect(entry.locales).toEqual({ en: { homepage: 'https://example.test/en' } });
  });

  test('omits locales when the response has none', async () => {
    mockGet({
      plugins_enabled: true,
      plugins_dir: 'plugins',
      plugins: [{ id: 'example', description: 'plain' }],
    });
    const [entry] = (await pluginStoreApi.list()).plugins;
    expect(entry.locales).toBeUndefined();
    expect(Object.prototype.hasOwnProperty.call(entry, 'locales')).toBe(false);
  });
});

describe('store entry display metadata', () => {
  const entry = storeEntry({
    locales: {
      'zh-CN': { description: '中文描述。' },
      'zh-Hans': { description: '简体描述。', author: '作者' },
    },
  });

  test('simplified Chinese walks zh-CN, zh-Hans then bare zh', () => {
    // zh-CN 块只缺 author，逐字段回退到根值，不跨块借用 zh-Hans 的 author。
    expect(getStoreEntryDisplay(entry, 'zh-CN')).toEqual({
      name: 'Example',
      description: '中文描述。',
      author: 'someone',
      homepage: 'https://example.test',
      license: 'MIT',
      tags: ['Interceptor'],
    });
    expect(
      getStoreEntryDisplay(storeEntry({ locales: { zh: { description: '裸 zh。' } } }), 'zh-CN')
        .description
    ).toBe('裸 zh。');
  });

  test('traditional Chinese never receives Simplified locales', () => {
    expect(getStoreEntryDisplay(entry, 'zh-TW').description).toBe('An English description.');
    expect(
      getStoreEntryDisplay(storeEntry({ locales: { zh: { description: '裸 zh。' } } }), 'zh-TW')
        .description
    ).toBe('An English description.');
  });

  test('tags only switch when the matched locale carries its own list', () => {
    expect(getStoreEntryDisplay(entry, 'zh-CN').tags).toEqual(['Interceptor']);
    expect(
      getStoreEntryDisplay(
        storeEntry({ locales: { 'zh-CN': { tags: [' 拦截器 ', ''] } } }),
        'zh-CN'
      ).tags
    ).toEqual(['拦截器']);
  });

  test('tolerates non-canonical locale keys from other registries', () => {
    expect(
      getStoreEntryDisplay(storeEntry({ locales: { zh_cn: { description: '中文。' } } }), 'zh-CN')
        .description
    ).toBe('中文。');
  });

  test('localized names stay searchable next to their root values', () => {
    const localized = storeEntry({
      tags: ['Interceptor'],
      locales: { 'zh-CN': { name: '示例插件', description: '中文描述。', tags: ['拦截器'] } },
    });
    const other = unrelatedEntry('other');
    const search = (query: string, language = 'zh-CN') =>
      filterStoreEntries([localized, other], query, language).map((entry) => entry.id);

    // 根字段与当前语言的翻译都进入检索文本，切语言后原文仍可搜到。
    expect(search('Example')).toEqual(['example']);
    expect(search('示例插件')).toEqual(['example']);
    expect(search('english description')).toEqual(['example']);
    expect(search('中文描述')).toEqual(['example']);
    expect(search('interceptor')).toEqual(['example']);
    expect(search('拦截器')).toEqual(['example']);
    // 另一种语言不会带入不匹配的翻译。
    expect(search('示例插件', 'en')).toEqual([]);

    const text = getStoreSearchText(localized, 'zh-CN');
    for (const field of ['example', 'someone', 'router-for-me/example-plugin', 'mit', 'official']) {
      expect(text).toContain(field);
    }
  });

  test('an empty query keeps every entry and the search ignores case and padding', () => {
    const entries = [storeEntry({}), unrelatedEntry('b')];
    expect(filterStoreEntries(entries, '   ', 'en')).toHaveLength(2);
    expect(filterStoreEntries(entries, '  EXAMPLE ', 'en').map((entry) => entry.id)).toEqual([
      'example',
    ]);
  });

  test('unknown languages and missing locales keep the root fields', () => {
    const plain = storeEntry({});
    expect(getStoreEntryDisplay(plain, 'en')).toEqual({
      name: 'Example',
      description: 'An English description.',
      author: 'someone',
      homepage: 'https://example.test',
      license: 'MIT',
      tags: ['Interceptor'],
    });
    expect(getStoreEntryDisplay(entry, 'xx')).toEqual(getStoreEntryDisplay(storeEntry({}), 'en'));
  });
});

// Source contract only: there is no browser DOM harness, so this pins that every
// store surface reads the localized display fields instead of the root ones. The registry
// search in `pluginSearch.ts` is the one deliberate exception and is tested directly above.
describe('plugin store locale wiring', () => {
  const ROOT_FIELD_READ = /\b(entry|plugin|item)\.(description|author|license|tags|name)\b/;

  test('rows, the install flow and the gate resolve metadata through one selector', () => {
    const selectorUsers = [
      'src/features/plugins/components/StorePluginRow.tsx',
      'src/features/plugins/components/PluginInstallGateModal.tsx',
      'src/features/plugins/pluginStoreEntries.ts',
    ];
    const titleUsers = [
      'src/features/plugins/components/PluginInstallOptionsModal.tsx',
      'src/features/plugins/PluginsPage.tsx',
    ];
    const otherSurfaces = [
      'src/features/plugins/components/PluginStoreView.tsx',
      'src/features/plugins/hooks/usePluginStore.ts',
    ];

    for (const path of selectorUsers) {
      expect(readSource(path)).toContain('getStoreEntryDisplay');
    }
    // getStoreEntryTitle is a thin wrapper over getStoreEntryDisplay in pluginStoreEntries.ts.
    expect(readSource('src/features/plugins/pluginStoreEntries.ts')).toMatch(
      /getStoreEntryTitle = [\s\S]*?getStoreEntryDisplay\(entry, language\)\.name/
    );
    for (const path of titleUsers) {
      expect(readSource(path)).toContain('getStoreEntryTitle');
    }

    for (const path of [...selectorUsers, ...titleUsers, ...otherSurfaces]) {
      const source = readSource(path);
      expect(source).not.toMatch(/entry\.description/);
      expect(source).not.toMatch(/entry\.author/);
      expect(source).not.toMatch(/entry\.license/);
      expect(source).not.toMatch(/entry\.tags/);
      expect(source).not.toMatch(/entry\.name/);
      expect(source).not.toMatch(ROOT_FIELD_READ);
    }
  });
});
