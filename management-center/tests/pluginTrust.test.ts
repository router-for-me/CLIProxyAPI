import { describe, expect, test } from 'bun:test';
import { isOfficialPlugin } from '../src/features/plugins/pluginResources';
import type { PluginStoreEntry } from '../src/types';

const pluginEntry = (sourceId: string, repository: string) =>
  ({ sourceId, repository }) as PluginStoreEntry;

describe('plugin store trust', () => {
  test('trusts only the official source with an official repository', () => {
    expect(isOfficialPlugin(pluginEntry('official', 'router-for-me/example-plugin'))).toBe(true);
    expect(isOfficialPlugin(pluginEntry('third-party', 'router-for-me/example-plugin'))).toBe(
      false
    );
    expect(isOfficialPlugin(pluginEntry('official', 'someone-else/example-plugin'))).toBe(false);
  });
});
