import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { PluginStoreView } from '@/features/plugins/components/PluginStoreView';
import {
  findPluginUpdate,
  formatPluginVersion,
  getStoreEntryKey,
  pluginVersionMatches,
} from '@/features/plugins/pluginStoreEntries';
import { filterInstalledPlugins } from '@/features/plugins/pluginSearch';
import { parsePluginsView, parseStoreLayout } from '@/features/plugins/pluginsView';
import type { PluginListEntry, PluginStoreEntry } from '@/types';

const installedPlugin = (
  overrides: Partial<PluginListEntry> = {},
  repository = 'https://github.com/router-for-me/gemini-bridge'
): PluginListEntry => ({
  id: 'gemini-bridge',
  path: '/plugins/gemini-bridge.so',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: false,
  supportsQuota: false,
  logo: '',
  configFields: [],
  menus: [],
  metadata: {
    name: 'Gemini Bridge',
    version: '1.4.2',
    author: 'router-for-me',
    githubRepository: repository,
    logo: '',
    configFields: [],
  },
  ...overrides,
});

const storeEntry = (overrides: Partial<PluginStoreEntry> = {}): PluginStoreEntry => ({
  storeId: 'official/gemini-bridge',
  sourceId: 'official',
  sourceName: 'official',
  sourceUrl: '',
  id: 'gemini-bridge',
  name: 'Gemini Bridge',
  description: '',
  author: '',
  version: '1.5.0',
  repository: 'router-for-me/gemini-bridge',
  installType: 'github-release',
  authRequired: false,
  authConfigured: false,
  platforms: [],
  logo: '',
  homepage: '',
  license: '',
  tags: [],
  installed: true,
  installedVersion: '1.4.2',
  path: '',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  updateAvailable: true,
  ...overrides,
});

describe('shared plugin mutation lock', () => {
  for (const kind of ['install', 'update'] as const) {
    test(`${kind} is disabled during installed-plugin mutations and unlocked afterward`, () => {
      const entry = storeEntry({ installed: kind === 'update' });
      const render = (actionBusy: boolean) =>
        renderToStaticMarkup(
          createElement(PluginStoreView, {
            data: {
              plugins: [entry],
              pluginsEnabled: true,
              pluginsDir: 'plugins',
              sourceErrors: [],
            },
            error: null,
            loading: false,
            entries: [entry],
            layout: 'list',
            query: '',
            connected: true,
            actionBusy,
            installingKey: '',
            getLogo: () => '',
            getAction: () => ({ kind }),
            onInstall: () => {},
            onRetry: () => {},
            onClearSearch: () => {},
          })
        );
      expect(render(true)).toMatch(/<button[^>]*disabled=""[^>]*>/);
      expect(render(false)).not.toMatch(/<button[^>]*disabled=""[^>]*>/);
    });
  }

  test('the page shares its lock with the store UI and all install entry points', () => {
    const page = readFileSync('src/features/plugins/PluginsPage.tsx', 'utf8');
    const hook = readFileSync('src/features/plugins/hooks/usePluginStore.ts', 'utf8');
    expect(page).toContain('actionBusy: installed.actionBusy');
    expect(page).toContain('actionBusy={installed.actionBusy}');
    expect(hook).toContain(
      'const installBlocked = !connected || actionBusy || Boolean(installingKey)'
    );
    expect(hook).toMatch(/async \(entry:[\s\S]*?if \(installBlocked\) return;/);
    expect(hook).toMatch(/const openInstall[\s\S]*?if \(installBlocked\) return;/);
    expect(hook).toContain('if (installBlocked || !installOptionsEntry) return;');
    expect(hook).toContain('if (installBlocked || !gateEntry) return;');
  });
});

describe('installed plugin to registry update match', () => {
  test('matches the single entry the backend flags as an update', () => {
    const entry = storeEntry();
    expect(findPluginUpdate(installedPlugin(), [entry])).toBe(entry);
  });

  test('no update when the registry does not flag one', () => {
    expect(findPluginUpdate(installedPlugin(), [storeEntry({ updateAvailable: false })])).toBe(
      null
    );
  });

  test('ignores entries for other plugin IDs and entries not reported as installed', () => {
    expect(findPluginUpdate(installedPlugin(), [storeEntry({ id: 'gemini-bridge-2' })])).toBe(null);
    expect(findPluginUpdate(installedPlugin(), [storeEntry({ installed: false })])).toBe(null);
  });

  test('picks the flagged source when another source advertises the same ID', () => {
    // The backend only flags the source the plugin was installed from.
    const flagged = storeEntry();
    const mirror = storeEntry({
      storeId: 'mirror/gemini-bridge',
      sourceId: 'mirror',
      sourceName: 'mirror',
      updateAvailable: false,
    });
    expect(findPluginUpdate(installedPlugin(), [mirror, flagged])).toBe(flagged);
  });

  test('refuses to guess when several entries flag an update for the same ID', () => {
    const first = storeEntry();
    const second = storeEntry({ storeId: 'mirror/gemini-bridge', sourceId: 'mirror' });
    expect(findPluginUpdate(installedPlugin(), [first, second])).toBe(null);
  });

  test('a declared repository must match the entry, whatever its URL form', () => {
    expect(
      findPluginUpdate(installedPlugin(), [
        storeEntry({ repository: 'someone-else/gemini-bridge' }),
      ])
    ).toBe(null);
    expect(findPluginUpdate(installedPlugin(), [storeEntry({ repository: '' })])).toBe(null);
    for (const repository of [
      'router-for-me/gemini-bridge',
      'https://github.com/router-for-me/gemini-bridge.git',
      'https://github.com/Router-For-Me/gemini-bridge/',
    ]) {
      expect(findPluginUpdate(installedPlugin(), [storeEntry({ repository })])).not.toBe(null);
    }
  });

  test('a repository mismatch also rules out the only flagged entry among several sources', () => {
    const lookalike = storeEntry({ repository: 'evil/gemini-bridge' });
    const mirror = storeEntry({ storeId: 'mirror/gemini-bridge', updateAvailable: false });
    expect(findPluginUpdate(installedPlugin(), [lookalike, mirror])).toBe(null);
  });

  test('without declared metadata the backend flag and a unique ID decide', () => {
    const entry = storeEntry();
    expect(findPluginUpdate(installedPlugin({ metadata: null }), [entry])).toBe(entry);
    expect(findPluginUpdate(installedPlugin({}, ''), [entry])).toBe(entry);
  });
});

describe('store entry helpers', () => {
  test('versions are shown with a single v prefix and compared without it', () => {
    expect(formatPluginVersion('1.2.0')).toBe('v1.2.0');
    expect(formatPluginVersion('v1.2.0')).toBe('v1.2.0');
    expect(formatPluginVersion(' V2 ')).toBe('V2');
    expect(formatPluginVersion('  ')).toBe('');
    expect(pluginVersionMatches('v1.2.0', '1.2.0')).toBe(true);
    expect(pluginVersionMatches('1.2.0', '1.2.1')).toBe(false);
  });

  test('entries are keyed per source, falling back to the plugin ID', () => {
    expect(getStoreEntryKey(storeEntry())).toBe('official/gemini-bridge');
    expect(getStoreEntryKey(storeEntry({ storeId: '' }))).toBe('gemini-bridge');
  });

  test('the view parameter only recognises the store view', () => {
    expect(parsePluginsView('store')).toBe('store');
    expect(parsePluginsView(null)).toBe('installed');
    expect(parsePluginsView('installed')).toBe('installed');
    expect(parsePluginsView('STORE')).toBe('installed');
  });
});

describe('installed plugin search', () => {
  const plugins = [
    installedPlugin(),
    installedPlugin({
      id: 'request-audit',
      path: '/plugins/request-audit.so',
      metadata: null,
      menus: [{ menu: 'Audit log', path: '/audit', description: 'Recent requests' }],
    }),
  ];

  test('matches id, metadata and menus, case-insensitively', () => {
    const ids = (query: string) => filterInstalledPlugins(plugins, query).map((p) => p.id);
    expect(ids('')).toEqual(['gemini-bridge', 'request-audit']);
    expect(ids('GEMINI bridge')).toEqual(['gemini-bridge']);
    expect(ids('1.4.2')).toEqual(['gemini-bridge']);
    expect(ids('audit log')).toEqual(['request-audit']);
    expect(ids('recent')).toEqual(['request-audit']);
    expect(ids('nothing-like-this')).toEqual([]);
  });
});

describe('store layout preference', () => {
  test('only a stored "cards" selects the card layout', () => {
    expect(parseStoreLayout('cards')).toBe('cards');
    expect(parseStoreLayout('list')).toBe('list');
    expect(parseStoreLayout(null)).toBe('list');
    expect(parseStoreLayout('compact')).toBe('list');
  });
});
