import { describe, expect, test } from 'bun:test';
import { getPluginLogo } from '../src/features/plugins/pluginLogo';
import type { PluginListEntry, PluginStoreEntry } from '../src/types';

const plugin = (overrides: Partial<PluginListEntry> = {}) =>
  ({ id: 'example', logo: '', metadata: null, ...overrides }) as PluginListEntry;
const entry = (overrides: Partial<PluginStoreEntry> = {}) =>
  ({
    id: 'example',
    logo: 'https://example.com/store.png',
    repository: 'owner/repo',
    ...overrides,
  }) as PluginStoreEntry;

describe('management plugin logos', () => {
  test('uses the store logo for unregistered plugins or missing runtime logos', () => {
    expect(getPluginLogo(plugin({ registered: false }), [entry()])).toBe(entry().logo);
    expect(getPluginLogo(plugin({ registered: true }), [entry()])).toBe(entry().logo);
  });

  test('prefers runtime logo, then metadata logo over store metadata', () => {
    const metadata = {
      name: '',
      version: '',
      author: '',
      githubRepository: '',
      logo: 'metadata.png',
      configFields: [],
    };
    expect(getPluginLogo(plugin({ logo: 'runtime.png', metadata }), [entry()])).toBe('runtime.png');
    expect(getPluginLogo(plugin({ logo: '  ', metadata }), [entry()])).toBe('metadata.png');
  });

  test('does not use unrelated or ambiguous registry entries', () => {
    expect(getPluginLogo(plugin(), [entry({ id: 'other' })])).toBe('');
    expect(getPluginLogo(plugin(), [entry(), entry({ repository: 'other/repo' })])).toBe('');
    expect(getPluginLogo(plugin(), [])).toBe('');
  });

  test('matches repository metadata when multiple sources declare the same ID', () => {
    const metadata = {
      name: '',
      version: '',
      author: '',
      githubRepository: 'https://github.com/owner/repo.git',
      logo: '',
      configFields: [],
    };
    expect(
      getPluginLogo(plugin({ metadata }), [entry(), entry({ repository: 'other/repo' })])
    ).toBe(entry().logo);
    expect(getPluginLogo(plugin({ metadata }), [entry({ repository: 'other/repo' })])).toBe('');
  });
});
