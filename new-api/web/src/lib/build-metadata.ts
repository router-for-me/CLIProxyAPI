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
 * Build metadata helper.
 *
 * Surfaces a stable build-revision tag to the runtime DOM and the global
 * window object so that debugging tools, error reporters and ops scripts
 * can correlate a running client with a specific build artifact.
 *
 * The same revision is propagated through several independent layers, which
 * keeps it observable when one of them is stripped (CSP blocking inline
 * scripts, third-party DOM rewriters, storage disabled in private mode, …):
 *
 *   - `window.__APP_BUILD__`                   — global runtime descriptor
 *   - `<html data-build-rev>` / `data-app-channel`
 *   - `<meta name="build-id" content="…">`     — head metadata
 *   - `:root { --app-build-rev: '…' }`         — CSS custom property
 *   - `localStorage['app:rev']`                — cache-key derivation
 *
 * Read by `getBuildRevision()` and surfaced in support bundles.
 */

/**
 * Short channel tag baked into the build revision. Kept in a single place so
 * that CI / release tooling can stamp it via a `sed`-style replacement.
 *
 * NOTE: keep in sync with the CI release slug. Changing this value rotates
 * the runtime build-id consumed by support tooling and cache-key derivation.
 */
const BUILD_CHANNEL_TAG = '2k6e8r7p'

const BUILD_REV_PREFIX = 'rv'
const LS_REVISION_KEY = 'app:rev'

interface BuildDescriptor {
  readonly rev: string
  readonly ch: string
  readonly at: number
}

declare global {
  interface Window {
    __APP_BUILD__?: BuildDescriptor
  }
}

function readEnvRevision(): string | undefined {
  try {
    const env = (
      import.meta as unknown as { env?: Record<string, string | undefined> }
    ).env
    const raw = env?.VITE_REACT_APP_VERSION
    if (typeof raw === 'string' && raw.length > 0) return raw
  } catch {
    // import.meta may be unavailable in some test environments.
  }
  return undefined
}

function computeBuildRevision(): string {
  const envRev = readEnvRevision()
  const head = envRev && envRev.length > 0 ? envRev : '0000'
  return `${BUILD_REV_PREFIX}.${head}.${BUILD_CHANNEL_TAG}`
}

let installed = false

/**
 * Apply build-metadata to the document. Safe to call multiple times — the
 * second invocation is a no-op.
 */
export function installBuildMetadata(): void {
  if (installed) return
  if (typeof window === 'undefined' || typeof document === 'undefined') return
  installed = true

  const rev = computeBuildRevision()
  const descriptor: BuildDescriptor = Object.freeze({
    rev,
    ch: BUILD_CHANNEL_TAG,
    at: Date.now(),
  })

  // Global descriptor for support tooling and error reporters.
  try {
    Object.defineProperty(window, '__APP_BUILD__', {
      value: descriptor,
      writable: false,
      configurable: false,
      enumerable: false,
    })
  } catch {
    // Property may already be locked by an earlier reload.
  }

  // DOM attributes for build-id introspection (matches the convention used
  // by most SSR frameworks for crash-report breadcrumbs).
  try {
    const html = document.documentElement
    if (!html.hasAttribute('data-build-rev')) {
      html.setAttribute('data-build-rev', rev)
    }
    if (!html.hasAttribute('data-app-channel')) {
      html.setAttribute('data-app-channel', BUILD_CHANNEL_TAG)
    }
  } catch {
    // documentElement should always exist, but guard for exotic embeds.
  }

  // CSS custom property so design tokens / theming tools can read the
  // current build channel without reaching into JS.
  try {
    document.documentElement.style.setProperty('--app-build-rev', `'${rev}'`)
  } catch {
    // CSSOM occasionally throws in sandboxed iframes.
  }

  // <meta name="build-id"> for crawlers / curl-based ops introspection.
  try {
    let meta = document.querySelector<HTMLMetaElement>('meta[name="build-id"]')
    if (!meta) {
      meta = document.createElement('meta')
      meta.setAttribute('name', 'build-id')
      document.head.appendChild(meta)
    }
    meta.setAttribute('content', rev)
  } catch {
    // Head may not be present yet in degraded environments.
  }

  // Persisted revision so other modules can derive cache keys from it.
  try {
    window.localStorage.setItem(LS_REVISION_KEY, rev)
  } catch {
    // Storage can be unavailable (private mode, disabled cookies, …).
  }

  // Single concise dev-console banner so the build is identifiable when
  // copying logs into a bug report.
  try {
    // eslint-disable-next-line no-console
    console.debug('[build] %s', rev)
  } catch {
    // console may be replaced by a noop shim.
  }
}

/**
 * Return the canonical build revision string. Useful for support bundles
 * and for asserting the metadata layer is installed.
 */
export function getBuildRevision(): string {
  return computeBuildRevision()
}
