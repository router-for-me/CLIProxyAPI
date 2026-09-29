/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type {
  MarketplaceIndex,
  MarketplaceIndexVersion,
  MarketplacePlugin,
  MarketplacePluginIcon,
  TaskPluginListItem,
} from '../types'
import { pluginProtocolClaimsSchema } from './plugin-meta-preview'
import { getPluginWebsite } from './plugin-website'

export const SUPPORTED_INDEX_VERSION = 1

/** The gateway only runs task plugins today; other kinds are filtered out. */
export const SUPPORTED_PLUGIN_KIND = 'task'

/**
 * Resolves a version's `path` against the index URL it was declared in, so the
 * same index works behind any raw prefix (GitHub raw, jsDelivr, a mirror).
 * Returns `null` when the path escapes to another origin or cannot be resolved.
 */
export function resolvePluginSourceUrl(
  indexUrl: string,
  path: string
): string | null {
  const trimmed = path.trim()
  if (!trimmed) return null
  let base: URL
  try {
    base = new URL(indexUrl)
  } catch {
    return null
  }
  let resolved: URL
  try {
    resolved = new URL(trimmed, base)
  } catch {
    return null
  }
  if (resolved.protocol !== 'http:' && resolved.protocol !== 'https:') {
    return null
  }
  // A relative path in an index must stay on the host that served the index;
  // an index that redirects source downloads elsewhere is not a source we can
  // reason about for integrity.
  if (resolved.origin !== base.origin) return null
  return resolved.toString()
}

/**
 * Validates an untrusted index payload into the display shape. Unknown fields
 * are dropped and malformed plugin entries are skipped rather than failing the
 * whole source, because the index is only a display cache — admission still
 * happens server-side on the compiled source.
 */
export function parseMarketplaceIndex(payload: unknown): MarketplaceIndex {
  if (!payload || typeof payload !== 'object') {
    throw new Error('index is not an object')
  }
  const raw = payload as Record<string, unknown>
  const indexVersion = Number(raw.indexVersion)
  if (!Number.isFinite(indexVersion)) {
    throw new Error('index is missing indexVersion')
  }
  if (indexVersion > SUPPORTED_INDEX_VERSION) {
    throw new Error(`unsupported indexVersion ${indexVersion}`)
  }
  const plugins: MarketplacePlugin[] = []
  if (Array.isArray(raw.plugins)) {
    for (const entry of raw.plugins) {
      const plugin = parseMarketplacePlugin(entry)
      if (plugin) plugins.push(plugin)
    }
  }
  return {
    indexVersion,
    name: typeof raw.name === 'string' ? raw.name : '',
    plugins: plugins.sort((left, right) => {
      const difference = (right.sortPriority ?? 0) - (left.sortPriority ?? 0)
      if (difference !== 0) return difference
      if (left.key === right.key) return 0
      return left.key < right.key ? -1 : 1
    }),
  }
}

function parseMarketplacePlugin(entry: unknown): MarketplacePlugin | null {
  if (!entry || typeof entry !== 'object') return null
  const raw = entry as Record<string, unknown>
  const key = typeof raw.key === 'string' ? raw.key.trim() : ''
  if (!key) return null

  const versions: MarketplaceIndexVersion[] = []
  if (Array.isArray(raw.versions)) {
    for (const candidate of raw.versions) {
      if (!candidate || typeof candidate !== 'object') continue
      const rawVersion = candidate as Record<string, unknown>
      const version =
        typeof rawVersion.version === 'string' ? rawVersion.version.trim() : ''
      const path =
        typeof rawVersion.path === 'string' ? rawVersion.path.trim() : ''
      if (!version || !path) continue
      const kind =
        typeof rawVersion.kind === 'string' ? rawVersion.kind.trim() : ''
      if (kind && kind !== SUPPORTED_PLUGIN_KIND) continue
      versions.push({
        version,
        path,
        sha256:
          typeof rawVersion.sha256 === 'string'
            ? rawVersion.sha256.trim()
            : undefined,
        minApiVersion: Number.isFinite(Number(rawVersion.minApiVersion))
          ? Number(rawVersion.minApiVersion)
          : undefined,
        kind: kind || undefined,
        allowedHosts: stringArray(rawVersion.allowedHosts),
        baseUrl:
          typeof rawVersion.baseUrl === 'string' && rawVersion.baseUrl.trim()
            ? rawVersion.baseUrl.trim()
            : undefined,
        auth: typeof rawVersion.auth === 'string' ? rawVersion.auth : undefined,
      })
    }
  }
  if (versions.length === 0) return null

  const declaredLatest = typeof raw.latest === 'string' ? raw.latest.trim() : ''
  const latest = versions.some((entry) => entry.version === declaredLatest)
    ? declaredLatest
    : versions[0].version

  // `icon` is a LobeHub name or the text scheme, exactly what meta.icon admits.
  // Inline data URIs and remote URLs are dropped: image logos ship as sidecar
  // files declared in `iconFile`, and are only ever loaded from the index's own
  // origin, so an index cannot turn the marketplace page into a beacon.
  let icon: string | undefined
  if (typeof raw.icon === 'string') {
    const trimmed = raw.icon.trim()
    if (
      trimmed &&
      trimmed.length <= 128 &&
      !trimmed.startsWith('data:') &&
      !trimmed.includes('://')
    ) {
      icon = trimmed
    }
  }
  let iconFile: MarketplacePluginIcon | undefined
  if (raw.iconFile && typeof raw.iconFile === 'object') {
    const rawIconFile = raw.iconFile as Record<string, unknown>
    const iconPath =
      typeof rawIconFile.path === 'string' ? rawIconFile.path.trim() : ''
    if (/\.(svg|png)$/i.test(iconPath)) {
      iconFile = {
        path: iconPath,
        sha256:
          typeof rawIconFile.sha256 === 'string'
            ? rawIconFile.sha256.trim()
            : undefined,
      }
    }
  }

  return {
    key,
    sortPriority:
      typeof raw.sortPriority === 'number' &&
      Number.isInteger(raw.sortPriority) &&
      raw.sortPriority >= -2147483648 &&
      raw.sortPriority <= 2147483647
        ? raw.sortPriority
        : 0,
    website: getPluginWebsite(raw.website),
    name: typeof raw.name === 'string' && raw.name ? raw.name : key,
    icon,
    iconFile,
    description: parseMarketplaceDescription(raw.description),
    channelTypes: numberArray(raw.channelTypes),
    models: stringArray(raw.models),
    protocols: pluginProtocolClaimsSchema.safeParse(raw.protocols).data,
    latest,
    versions,
  }
}

function parseMarketplaceDescription(
  value: unknown
): string | Record<string, string> | undefined {
  if (typeof value === 'string') return value
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return undefined
  }
  const mapped: Record<string, string> = {}
  for (const [locale, text] of Object.entries(
    value as Record<string, unknown>
  )) {
    if (typeof text === 'string') mapped[locale] = text
  }
  return Object.keys(mapped).length > 0 ? mapped : undefined
}

function stringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  if (!value.every((item) => typeof item === 'string' && item.length > 0)) {
    return undefined
  }
  return value
}

function numberArray(value: unknown): number[] | undefined {
  if (!Array.isArray(value)) return undefined
  const items = value.filter(
    (item): item is number => typeof item === 'number' && Number.isFinite(item)
  )
  return items.length > 0 ? items : undefined
}

export function findMarketplaceVersion(
  plugin: MarketplacePlugin,
  version: string
): MarketplaceIndexVersion | undefined {
  return plugin.versions.find((entry) => entry.version === version)
}

export type InstallState =
  | { status: 'not_installed' }
  | { status: 'up_to_date'; installedVersion: string }
  | { status: 'upgradable'; installedVersion: string; latestVersion: string }
  | { status: 'diverged'; installedVersion: string; latestVersion: string }

/**
 * Compares a marketplace entry against the gateway's installed plugins.
 *
 * `diverged` covers the case where a plugin is installed at a version the index
 * does not list (locally uploaded, or the source rolled a version back): the UI
 * must not call that an upgrade, because installing would move the gateway to a
 * version it may already have moved away from deliberately.
 */
export function deriveInstallState(
  plugin: MarketplacePlugin,
  installed: TaskPluginListItem[]
): InstallState {
  const match = installed.find((item) => item.meta.key === plugin.key)
  if (!match) return { status: 'not_installed' }

  const installedVersion = match.meta.version
  if (installedVersion === plugin.latest) {
    return { status: 'up_to_date', installedVersion }
  }
  const known = plugin.versions.some(
    (entry) => entry.version === installedVersion
  )
  if (!known) {
    return {
      status: 'diverged',
      installedVersion,
      latestVersion: plugin.latest,
    }
  }
  return {
    status: 'upgradable',
    installedVersion,
    latestVersion: plugin.latest,
  }
}

/**
 * Built-in version shown next to the marketplace latest. Factory-served items
 * do not carry `factory_meta` (their `meta` *is* the factory meta); overridden
 * factory plugins expose the shadowed built-in on `factory_meta`.
 */
export function marketplaceBuiltInVersion(
  installed?: TaskPluginListItem
): string | undefined {
  if (!installed) return undefined
  if (installed.source === 'factory') return installed.meta.version
  return installed.factory_meta?.version
}

export function isStaleFactoryOverride(item: TaskPluginListItem): boolean {
  return (
    item.source === 'override_over_factory' &&
    item.factory_meta != null &&
    item.factory_meta.version !== item.meta.version
  )
}

/**
 * A source is only integrity-checked when every listed version carries a
 * sha256. Anything less and installs from it cannot be pinned, so the UI warns.
 */
export function indexHasIntegrityHashes(index: MarketplaceIndex): boolean {
  return (
    index.plugins.length > 0 &&
    index.plugins.every((plugin) =>
      plugin.versions.every((version) => Boolean(version.sha256))
    )
  )
}

export const DEFAULT_MARKETPLACE_INDEX_URL =
  'https://www.newapi.ai/api/v1/plugins/index.json'

export const GITHUB_MARKETPLACE_INDEX_URL =
  'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/index.json'

/**
 * Both built-in indexes are maintained by the project. Other configured
 * sources get an explicit at-your-own-risk label.
 */
export function isDefaultMarketplaceSource(indexUrl: string): boolean {
  const normalized = indexUrl.trim()
  return (
    normalized === DEFAULT_MARKETPLACE_INDEX_URL ||
    normalized === GITHUB_MARKETPLACE_INDEX_URL
  )
}
