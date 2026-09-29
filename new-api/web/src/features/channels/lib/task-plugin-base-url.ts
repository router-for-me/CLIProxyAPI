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
/**
 * Hosts an administrator should look at twice before sending a channel key to
 * them: loopback, RFC 1918 and CGNAT ranges, link-local (including the cloud
 * metadata address), IPv6 loopback/link-local/ULA, and single-label or
 * *.local / *.internal / *.lan names. These are legitimate for self-hosted
 * upstreams, so they are flagged, never blocked.
 */
const PRIVATE_HOST_PATTERNS: readonly RegExp[] = [
  /^localhost$/i,
  /\.localhost$/i,
  /^127\./,
  /^0\.0\.0\.0$/,
  /^10\./,
  /^192\.168\./,
  /^172\.(1[6-9]|2\d|3[01])\./,
  /^169\.254\./,
  /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./,
  /^\[?::1\]?$/,
  /^\[?fe80:/i,
  /^\[?f[cd][0-9a-f]{2}:/i,
  /\.(local|internal|lan)$/i,
]

export type BaseUrlTrust = {
  plainHttp: boolean
  privateHost: boolean
}

/**
 * Describes why a base URL deserves a warning before a channel key is bound to
 * it. Returns null for empty or unparsable input so callers render nothing.
 */
export function assessBaseUrlTrust(
  value: string | undefined
): BaseUrlTrust | null {
  const trimmed = value?.trim()
  if (!trimmed) return null
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    return null
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null
  const host = parsed.hostname.toLowerCase()
  const singleLabel = !host.includes('.') && !host.includes(':')
  return {
    plainHttp: parsed.protocol === 'http:',
    privateHost:
      singleLabel ||
      PRIVATE_HOST_PATTERNS.some((pattern) => pattern.test(host)),
  }
}

function normalizeBaseUrlValue(value: string | undefined): string {
  return String(value ?? '')
    .trim()
    .replace(/\/+$/, '')
}

/**
 * Decides what the Base URL field should become when the bound task plugin
 * changes. The plugin default is written only when the field is empty or still
 * holds the previous plugin's default, so a value the administrator typed by
 * hand survives switching plugins. Returns null when nothing should change.
 */
export function nextTaskPluginBaseUrl(
  currentValue: string | undefined,
  previousDefault: string | undefined,
  nextDefault: string | undefined
): string | null {
  if (!nextDefault) return null
  const current = normalizeBaseUrlValue(currentValue)
  if (current && current !== normalizeBaseUrlValue(previousDefault)) {
    return null
  }
  if (current === normalizeBaseUrlValue(nextDefault)) return null
  return nextDefault
}
