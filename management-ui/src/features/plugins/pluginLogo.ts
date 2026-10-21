import type { PluginListEntry, PluginStoreEntry } from '@/types';
import { buildRepositoryURL } from './pluginResources';

// Do not pick an arbitrary registry when multiple sources advertise the same ID.
export const getPluginLogo = (plugin: PluginListEntry, entries: PluginStoreEntry[]): string => {
  const logo = plugin.logo.trim() || plugin.metadata?.logo.trim();
  if (logo) return logo;

  const repository = plugin.metadata?.githubRepository.trim();
  const normalizeRepository = (value: string) =>
    buildRepositoryURL(value)
      .replace(/\/+$/, '')
      .replace(/\.git$/i, '')
      .toLowerCase();
  const matches = entries.filter(
    (entry) =>
      entry.id === plugin.id &&
      (!repository || normalizeRepository(entry.repository) === normalizeRepository(repository))
  );
  return matches.length === 1 ? matches[0].logo.trim() : '';
};
