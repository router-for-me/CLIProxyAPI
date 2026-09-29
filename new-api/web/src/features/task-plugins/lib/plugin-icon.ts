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
export type PluginIconDescriptor =
  | { kind: 'lobe'; name: string }
  | { kind: 'image'; src: string }
  | { kind: 'text'; label: string; colorSeed: string }

/**
 * Where the gateway serves a plugin's sidecar logo (icon.svg / icon.png shipped
 * next to plugin.js). The bytes never travel inside list JSON; the browser
 * loads them like any other image.
 */
export function pluginIconUrl(key: string, version?: string): string {
  const base = `/api/plugin/task/${encodeURIComponent(key)}/icon`
  return version ? `${base}?version=${encodeURIComponent(version)}` : base
}

export type PluginIconInput = {
  icon?: string
  channelTypes?: number[] | null
  key: string
  name?: string
  /** True when the gateway holds a sidecar logo for this plugin. */
  hasIcon?: boolean
  /** Direct image source, used by marketplace cards whose logo lives in the index repository. */
  iconSrc?: string
}

/**
 * Resolves how a plugin logo should render.
 *
 * Priority: a shipped image logo (`iconSrc`, or `hasIcon` served by the
 * gateway), then explicit `meta.icon` (a LobeHub icon name, or the `text` /
 * `text:<label>` scheme for a generated text avatar), then a text avatar derived
 * from the plugin name. Channel compatibility does not imply brand identity.
 * Inline data URIs and remote URLs in `meta.icon` are
 * not honoured: logos ship as sidecar files, never inside the manifest.
 */
export function resolvePluginIcon(
  input: PluginIconInput
): PluginIconDescriptor {
  if (input.iconSrc) {
    return { kind: 'image', src: input.iconSrc }
  }
  if (input.hasIcon) {
    return { kind: 'image', src: pluginIconUrl(input.key) }
  }
  const icon = input.icon?.trim()
  if (icon) {
    if (icon === 'text' || icon.startsWith('text:')) {
      const explicit = icon.startsWith('text:') ? icon.slice(5).trim() : ''
      return {
        kind: 'text',
        label: explicit ? explicit.slice(0, 4) : deriveTextLabel(input),
        colorSeed: input.key,
      }
    }
    if (!icon.startsWith('data:') && !icon.includes('://')) {
      return { kind: 'lobe', name: icon }
    }
  }
  return { kind: 'text', label: deriveTextLabel(input), colorSeed: input.key }
}

function deriveTextLabel(input: PluginIconInput): string {
  const source = input.name?.trim() || input.key.trim()
  return [...source].slice(0, 2).join('').toUpperCase()
}

/**
 * Deterministic palette pick: the same plugin key always renders the same
 * color, and every pair meets WCAG AA contrast in both themes.
 */
export const TEXT_AVATAR_PALETTE = [
  'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-100',
  'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-100',
  'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-100',
  'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-100',
  'bg-rose-100 text-rose-800 dark:bg-rose-900 dark:text-rose-100',
  'bg-cyan-100 text-cyan-800 dark:bg-cyan-900 dark:text-cyan-100',
  'bg-orange-100 text-orange-800 dark:bg-orange-900 dark:text-orange-100',
  'bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-100',
] as const

export function textAvatarClass(colorSeed: string): string {
  let hash = 0
  for (let i = 0; i < colorSeed.length; i++) {
    hash = (hash * 31 + colorSeed.charCodeAt(i)) | 0
  }
  return TEXT_AVATAR_PALETTE[Math.abs(hash) % TEXT_AVATAR_PALETTE.length]
}
