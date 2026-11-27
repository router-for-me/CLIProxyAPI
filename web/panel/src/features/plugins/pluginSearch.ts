import type { PluginListEntry, PluginStoreEntry } from '@/types';
import { getStoreEntryDisplay } from './pluginStoreMetadata';

const matches = (fields: Array<string | undefined>, needle: string) =>
  fields.filter(Boolean).join(' ').toLowerCase().includes(needle);

export const normalizeSearchQuery = (query: string) => query.trim().toLowerCase();

export const filterInstalledPlugins = (
  plugins: readonly PluginListEntry[],
  query: string
): PluginListEntry[] => {
  const needle = normalizeSearchQuery(query);
  if (!needle) return [...plugins];

  return plugins.filter((plugin) =>
    matches(
      [
        plugin.id,
        plugin.path,
        plugin.metadata?.name,
        plugin.metadata?.author,
        plugin.metadata?.version,
        plugin.metadata?.githubRepository,
        ...plugin.menus.map((menu) => `${menu.menu} ${menu.path} ${menu.description}`),
      ],
      needle
    )
  );
};

/**
 * Registry search. Root fields and the localized display fields are both searchable, so a
 * plugin stays findable by its original name after the UI language changes. This is the one
 * place allowed to read the root metadata fields; display surfaces go through
 * `getStoreEntryDisplay`.
 */
export const getStoreSearchText = (plugin: PluginStoreEntry, language: string): string => {
  const display = getStoreEntryDisplay(plugin, language);
  return [
    plugin.id,
    plugin.name,
    plugin.description,
    plugin.author,
    plugin.repository,
    plugin.sourceName,
    plugin.sourceUrl,
    plugin.license,
    ...plugin.tags,
    display.name,
    display.description,
    display.author,
    display.license,
    ...display.tags,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
};

export const filterStoreEntries = (
  entries: readonly PluginStoreEntry[],
  query: string,
  language: string
): PluginStoreEntry[] => {
  const needle = normalizeSearchQuery(query);
  if (!needle) return [...entries];
  return entries.filter((entry) => getStoreSearchText(entry, language).includes(needle));
};
