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
import assert from 'node:assert/strict'

import { describe, test } from 'vitest'

import {
  fetchPluginSourceText,
  MAX_PLUGIN_SOURCE_BYTES,
  normalizePluginSourceUrl,
  PluginSourceFetchError,
  pluginSourceByteLength,
} from '../lib/plugin-url'

describe('plugin source URL normalization', () => {
  test('rewrites a GitHub blob URL to its raw host', () => {
    assert.equal(
      normalizePluginSourceUrl(
        'https://github.com/QuantumNous/new-api-plugins/blob/main/plugins/tasks/doubao/1.0.0/plugin.js'
      ),
      'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/plugins/tasks/doubao/1.0.0/plugin.js'
    )
  })

  test('rewrites a GitHub blob URL whose ref contains a slash', () => {
    assert.equal(
      normalizePluginSourceUrl(
        'https://github.com/owner/repo/blob/feat/plugin-task/plugin.js'
      ),
      'https://raw.githubusercontent.com/owner/repo/feat/plugin-task/plugin.js'
    )
  })

  test('strips a line anchor that would break the raw request', () => {
    assert.equal(
      normalizePluginSourceUrl(
        'https://github.com/owner/repo/blob/main/plugin.js#L12-L20'
      ),
      'https://raw.githubusercontent.com/owner/repo/main/plugin.js'
    )
  })

  test('rewrites a gist page URL to its raw URL', () => {
    assert.equal(
      normalizePluginSourceUrl('https://gist.github.com/someone/abc123'),
      'https://gist.githubusercontent.com/someone/abc123/raw'
    )
  })

  test('keeps a gist URL that already targets raw content', () => {
    assert.equal(
      normalizePluginSourceUrl('https://gist.github.com/someone/abc123/raw'),
      'https://gist.githubusercontent.com/someone/abc123/raw'
    )
  })

  test('passes a raw URL through unchanged', () => {
    const raw =
      'https://raw.githubusercontent.com/owner/repo/main/plugins/tasks/x/1.0.0/plugin.js'
    assert.equal(normalizePluginSourceUrl(raw), raw)
  })

  test('passes an arbitrary https URL through unchanged', () => {
    assert.equal(
      normalizePluginSourceUrl(
        'https://cdn.jsdelivr.net/gh/owner/repo/plugin.js'
      ),
      'https://cdn.jsdelivr.net/gh/owner/repo/plugin.js'
    )
  })

  test('leaves a GitHub repository URL alone when it names no file', () => {
    assert.equal(
      normalizePluginSourceUrl('https://github.com/owner/repo'),
      'https://github.com/owner/repo'
    )
  })

  test('rejects a relative path with no origin to resolve against', () => {
    assert.equal(normalizePluginSourceUrl('plugins/tasks/x/plugin.js'), null)
  })

  test('rejects a non-http scheme', () => {
    assert.equal(normalizePluginSourceUrl('file:///etc/passwd'), null)
    assert.equal(normalizePluginSourceUrl('javascript:alert(1)'), null)
  })

  test('rejects empty input', () => {
    assert.equal(normalizePluginSourceUrl('   '), null)
  })
})

describe('plugin source byte length', () => {
  test('counts UTF-8 bytes rather than UTF-16 code units', () => {
    assert.equal(pluginSourceByteLength('abc'), 3)
    assert.equal(pluginSourceByteLength('中文'), 6)
  })
})

function stubResponse(options: {
  ok: boolean
  status?: number
  body?: string
  contentLength?: string
}): Response {
  const headers = new Headers()
  if (options.contentLength) {
    headers.set('content-length', options.contentLength)
  }
  return {
    ok: options.ok,
    status: options.status ?? (options.ok ? 200 : 404),
    headers,
    text: async () => options.body ?? '',
  } as unknown as Response
}

describe('browser plugin source fetch', () => {
  test('returns the response body for a successful fetch', async () => {
    const source = 'export function manifest() {}'
    const text = await fetchPluginSourceText(
      'https://example.com/plugin.js',
      async () => stubResponse({ ok: true, body: source })
    )
    assert.equal(text, source)
  })

  test('reports the status when the host answers with an error', async () => {
    await assert.rejects(
      fetchPluginSourceText('https://example.com/missing.js', async () =>
        stubResponse({ ok: false, status: 404 })
      ),
      (error: unknown) => {
        assert.ok(error instanceof PluginSourceFetchError)
        assert.equal(error.reason, 'not_found')
        assert.equal(error.status, 404)
        return true
      }
    )
  })

  test('reports unreachable when the request itself throws', async () => {
    await assert.rejects(
      fetchPluginSourceText(
        'https://blocked.example.com/plugin.js',
        async () => {
          throw new TypeError('Failed to fetch')
        }
      ),
      (error: unknown) => {
        assert.ok(error instanceof PluginSourceFetchError)
        assert.equal(error.reason, 'unreachable')
        return true
      }
    )
  })

  test('rejects a declared content-length above the 8 MiB backend limit before reading the body', async () => {
    let bodyRead = false
    await assert.rejects(
      fetchPluginSourceText('https://example.com/huge.js', async () => {
        const response = stubResponse({
          ok: true,
          contentLength: String(MAX_PLUGIN_SOURCE_BYTES + 1),
        })
        return {
          ...response,
          headers: response.headers,
          text: async () => {
            bodyRead = true
            return ''
          },
        } as unknown as Response
      }),
      (error: unknown) => {
        assert.ok(error instanceof PluginSourceFetchError)
        assert.equal(error.reason, 'too_large')
        return true
      }
    )
    assert.equal(bodyRead, false)
  })

  test('rejects an oversized body when the host declares no content-length', async () => {
    await assert.rejects(
      fetchPluginSourceText('https://example.com/huge.js', async () =>
        stubResponse({
          ok: true,
          body: 'x'.repeat(MAX_PLUGIN_SOURCE_BYTES + 1),
        })
      ),
      (error: unknown) => {
        assert.ok(error instanceof PluginSourceFetchError)
        assert.equal(error.reason, 'too_large')
        return true
      }
    )
  })

  test('accepts a body exactly at the limit', async () => {
    const text = await fetchPluginSourceText(
      'https://example.com/limit.js',
      async () =>
        stubResponse({ ok: true, body: 'x'.repeat(MAX_PLUGIN_SOURCE_BYTES) })
    )
    assert.equal(pluginSourceByteLength(text), MAX_PLUGIN_SOURCE_BYTES)
  })
})
