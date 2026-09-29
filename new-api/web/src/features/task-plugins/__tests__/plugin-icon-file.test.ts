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
  encodePluginIconFile,
  fetchPluginIconDataUri,
  MAX_PLUGIN_ICON_BYTES,
  PluginIconFileError,
  pluginIconMediaType,
} from '../lib/plugin-icon-file'

describe('pluginIconMediaType', () => {
  test('maps svg and png extensions case-insensitively and rejects everything else', () => {
    assert.equal(pluginIconMediaType('icon.svg'), 'image/svg+xml')
    assert.equal(pluginIconMediaType('ICON.PNG'), 'image/png')
    assert.equal(pluginIconMediaType('icon.jpg'), null)
    assert.equal(pluginIconMediaType('plugin.js'), null)
  })
})

describe('encodePluginIconFile', () => {
  test('encodes an svg file as the data URI the gateway stores', async () => {
    const file = new File(['<svg/>'], 'icon.svg', { type: '' })
    assert.equal(
      await encodePluginIconFile(file),
      'data:image/svg+xml;base64,PHN2Zy8+'
    )
  })

  test('rejects a file that is not svg or png', async () => {
    const file = new File(['x'], 'icon.gif', { type: 'image/gif' })
    await assert.rejects(encodePluginIconFile(file), (error: unknown) => {
      assert.ok(error instanceof PluginIconFileError)
      assert.equal(error.reason, 'unsupported_type')
      return true
    })
  })

  test('rejects a file over the size cap before reading it', async () => {
    const file = new File(
      [new Uint8Array(MAX_PLUGIN_ICON_BYTES + 1)],
      'icon.png'
    )
    await assert.rejects(encodePluginIconFile(file), (error: unknown) => {
      assert.ok(error instanceof PluginIconFileError)
      assert.equal(error.reason, 'too_large')
      return true
    })
  })
})

describe('fetchPluginIconDataUri', () => {
  const okResponse = (body: string) =>
    new Response(body, {
      status: 200,
      headers: { 'content-type': 'image/svg+xml' },
    })

  test('returns a data URI for a reachable svg even when the host labels it text/plain', async () => {
    const result = await fetchPluginIconDataUri(
      'https://raw.example/plugins/tasks/incho/icon.svg',
      {
        fetchImpl: async () =>
          new Response('<svg/>', {
            status: 200,
            headers: {
              'content-type': 'text/plain; charset=utf-8',
              'x-content-type-options': 'nosniff',
            },
          }),
      }
    )
    assert.equal(result, 'data:image/svg+xml;base64,PHN2Zy8+')
  })

  test('keeps the logo when the index digest matches and drops it on a mismatch', async () => {
    const url = 'https://raw.example/plugins/tasks/incho/icon.svg'
    assert.equal(
      await fetchPluginIconDataUri(url, {
        sha256:
          'd4dc56669143034f31aa309635d4113d9ad76a02b1739da22c965ed2049be9e6',
        fetchImpl: async () => okResponse('<svg/>'),
      }),
      'data:image/svg+xml;base64,PHN2Zy8+'
    )
    assert.equal(
      await fetchPluginIconDataUri(url, {
        sha256: 'deadbeef',
        fetchImpl: async () => okResponse('<svg/>'),
      }),
      null
    )
  })

  test('returns null for a non-image path, a failed response, or a network error', async () => {
    assert.equal(
      await fetchPluginIconDataUri('https://raw.example/icon.js', {
        fetchImpl: async () => okResponse('x'),
      }),
      null
    )
    assert.equal(
      await fetchPluginIconDataUri('https://raw.example/icon.svg', {
        fetchImpl: async () => new Response('', { status: 404 }),
      }),
      null
    )
    assert.equal(
      await fetchPluginIconDataUri('https://raw.example/icon.svg', {
        fetchImpl: async () => {
          throw new TypeError('offline')
        },
      }),
      null
    )
  })
})
