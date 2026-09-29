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
 * Backend limit for a single plugin source (`maxTaskPluginSourceBytes` in
 * controller/task_plugin.go). Enforced client-side too so an oversized fetch
 * fails with a readable message instead of a server rejection.
 */
export const MAX_PLUGIN_SOURCE_BYTES = 8 * 1024 * 1024

export function pluginSourceByteLength(source: string): number {
  return new TextEncoder().encode(source).length
}

/**
 * Rewrites human-facing code-hosting URLs to their raw-content equivalents, so
 * pasting a GitHub or gist page URL fetches plugin source instead of HTML.
 * Returns `null` when the input is not an absolute http(s) URL; every other URL
 * is passed through unchanged and attempted as-is.
 */
export function normalizePluginSourceUrl(input: string): string | null {
  const trimmed = input.trim()
  if (!trimmed) return null

  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    return null
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null

  // Line anchors (#L12-L20) are page-only and break raw requests.
  parsed.hash = ''
  const host = parsed.hostname.toLowerCase()
  const segments = parsed.pathname.split('/').filter(Boolean)

  if (host === 'github.com' || host === 'www.github.com') {
    // github.com/<owner>/<repo>/{blob,raw}/<ref>/<path...>. Both forms need the
    // raw host: github.com sends no CORS headers, and its /raw redirect is
    // blocked before the redirect is ever followed. `<ref>/<path...>` is kept
    // verbatim because raw.githubusercontent.com expects exactly that shape,
    // slashes in branch names included. The query string is dropped: blob-only
    // parameters such as ?plain=1 are meaningless on the raw host.
    const isSourceView = segments[2] === 'blob' || segments[2] === 'raw'
    if (isSourceView && segments.length > 4) {
      const rest = segments.slice(3).join('/')
      return `https://raw.githubusercontent.com/${segments[0]}/${segments[1]}/${rest}`
    }
    return parsed.toString()
  }

  if (host === 'gist.github.com' && segments.length > 0) {
    // gist.github.com/<user>/<id> renders HTML; the same path under
    // gist.githubusercontent.com with a /raw suffix serves the file bytes.
    const path = segments.join('/')
    const suffix = segments.includes('raw') ? path : `${path}/raw`
    return `https://gist.githubusercontent.com/${suffix}${parsed.search}`
  }

  return parsed.toString()
}

export type PluginSourceFetchFailure = 'unreachable' | 'not_found' | 'too_large'

export class PluginSourceFetchError extends Error {
  constructor(
    public reason: PluginSourceFetchFailure,
    public status?: number
  ) {
    super(reason)
  }
}

/**
 * Fetches plugin source in the browser. Every marketplace and URL-import fetch
 * goes through here: the gateway never makes the outbound request, so there is
 * no server-side SSRF surface.
 */
export async function fetchPluginSourceText(
  url: string,
  fetchImpl: typeof fetch = globalThis.fetch
): Promise<string> {
  let response: Response
  try {
    response = await fetchImpl(url)
  } catch {
    throw new PluginSourceFetchError('unreachable')
  }
  if (!response.ok) {
    throw new PluginSourceFetchError('not_found', response.status)
  }
  const declaredLength = Number(response.headers.get('content-length'))
  if (
    Number.isFinite(declaredLength) &&
    declaredLength > MAX_PLUGIN_SOURCE_BYTES
  ) {
    throw new PluginSourceFetchError('too_large')
  }
  const text = await response.text()
  if (pluginSourceByteLength(text) > MAX_PLUGIN_SOURCE_BYTES) {
    throw new PluginSourceFetchError('too_large')
  }
  return text
}

/**
 * SHA-256 of the source bytes, hex encoded. Returns `null` when WebCrypto is
 * unavailable (an insecure-context deployment): the hash is only an early
 * client-side check, the authoritative comparison happens on upload where the
 * server re-hashes the bytes it received.
 */
export async function computeSourceSha256(
  source: string
): Promise<string | null> {
  if (!globalThis.crypto?.subtle) return null
  const digest = await globalThis.crypto.subtle.digest(
    'SHA-256',
    new TextEncoder().encode(source)
  )
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('')
}
