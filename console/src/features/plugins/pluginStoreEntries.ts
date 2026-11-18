import type { PluginListEntry, PluginStoreEntry } from '@/types';
import { buildRepositoryURL } from './pluginResources';
import { getStoreEntryDisplay } from './pluginStoreMetadata';

/** One registry entry per source: the same plugin ID can be advertised by several sources. */
export const getStoreEntryKey = (entry: PluginStoreEntry) => entry.storeId || entry.id;

export const getStoreEntryTitle = (entry: PluginStoreEntry, language: string) =>
  getStoreEntryDisplay(entry, language).name || entry.id;

export const normalizePluginVersion = (version: string) => version.trim().replace(/^v/i, '');

export const formatPluginVersion = (version: string) => {
  const trimmed = version.trim();
  if (!trimmed) return '';
  return /^v/i.test(trimmed) ? trimmed : `v${trimmed}`;
};

export const pluginVersionMatches = (left: string, right: string) =>
  normalizePluginVersion(left) === normalizePluginVersion(right);

export const formatInstallType = (installType: string) =>
  installType
    .trim()
    .split('-')
    .map((part) => (part ? `${part[0].toUpperCase()}${part.slice(1)}` : part))
    .join(' ');

const normalizeRepository = (value: string) =>
  buildRepositoryURL(value)
    .replace(/\/+$/, '')
    .replace(/\.git$/i, '')
    .toLowerCase();

/**
 * The registry entry that offers an update for an installed plugin, or `null`.
 *
 * Backend contract (CLIProxyAPI `ListPluginStore`): `installed` and `installed_version` are
 * computed per plugin ID, so every source advertising the same ID reports them identically.
 * `update_available` is additionally gated per source: it is only true for the source the
 * plugin was installed from, or for the sole source advertising the ID when the install is not
 * store-managed. An unknown or different install source never reports an update.
 *
 * The match therefore trusts the backend's per-source flag and adds two guards of its own:
 * when the installed plugin declares a repository it must equal the entry's repository (the
 * same rule `getPluginLogo` uses), and exactly one entry may qualify. Anything ambiguous
 * yields no update affordance rather than a guess.
 */
export const findPluginUpdate = (
  plugin: PluginListEntry,
  entries: readonly PluginStoreEntry[]
): PluginStoreEntry | null => {
  const repository = plugin.metadata?.githubRepository.trim() ?? '';
  const candidates = entries.filter(
    (entry) =>
      entry.id === plugin.id &&
      entry.installed &&
      entry.updateAvailable &&
      (!repository || normalizeRepository(entry.repository) === normalizeRepository(repository))
  );
  return candidates.length === 1 ? candidates[0] : null;
};
