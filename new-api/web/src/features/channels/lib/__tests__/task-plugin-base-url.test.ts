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
  assessBaseUrlTrust,
  nextTaskPluginBaseUrl,
} from '../task-plugin-base-url'

describe('assessBaseUrlTrust', () => {
  test('returns null for empty or unparsable input', () => {
    assert.equal(assessBaseUrlTrust(''), null)
    assert.equal(assessBaseUrlTrust('   '), null)
    assert.equal(assessBaseUrlTrust('not a url'), null)
    assert.equal(assessBaseUrlTrust('ftp://files.example.com'), null)
  })

  test('flags plain http on a public host without flagging the host', () => {
    assert.deepEqual(assessBaseUrlTrust('http://api.example.com/v1'), {
      plainHttp: true,
      privateHost: false,
    })
  })

  test('flags private, loopback, link-local and single-label hosts', () => {
    for (const url of [
      'http://127.0.0.1:8000',
      'http://localhost:3000',
      'http://10.0.0.5',
      'http://192.168.1.10:8080',
      'http://172.16.0.1',
      'http://169.254.169.254/latest',
      'http://100.64.0.1',
      'http://[::1]:8000',
      'http://[fe80::1]',
      'http://[fd00::1]',
      'http://suno-api:8000',
      'https://nas.local',
      'https://gateway.internal',
    ]) {
      assert.equal(assessBaseUrlTrust(url)?.privateHost, true, url)
    }
  })

  test('does not flag a public https host', () => {
    assert.deepEqual(assessBaseUrlTrust('https://api.klingai.com'), {
      plainHttp: false,
      privateHost: false,
    })
    assert.equal(
      assessBaseUrlTrust('https://172.32.0.1')?.privateHost,
      false,
      '172.32.x.x is outside the RFC 1918 172.16/12 block'
    )
  })
})

describe('nextTaskPluginBaseUrl', () => {
  const pluginA = 'http://127.0.0.1:8000'
  const pluginB = 'https://api.vendor-b.example'

  test('fills an empty field with the selected plugin default', () => {
    assert.equal(nextTaskPluginBaseUrl('', undefined, pluginA), pluginA)
    assert.equal(nextTaskPluginBaseUrl(undefined, undefined, pluginA), pluginA)
  })

  test('replaces the previous plugin default when switching plugins', () => {
    assert.equal(nextTaskPluginBaseUrl(pluginA, pluginA, pluginB), pluginB)
    assert.equal(
      nextTaskPluginBaseUrl(`${pluginA}/`, pluginA, pluginB),
      pluginB,
      'a trailing slash typed by the browser autocomplete still counts as the default'
    )
  })

  test('keeps a value the administrator typed by hand', () => {
    assert.equal(
      nextTaskPluginBaseUrl('https://my-proxy.example', pluginA, pluginB),
      null
    )
    assert.equal(
      nextTaskPluginBaseUrl('https://my-proxy.example', undefined, pluginB),
      null
    )
  })

  test('changes nothing when the selected plugin declares no default', () => {
    assert.equal(nextTaskPluginBaseUrl('', pluginA, undefined), null)
    assert.equal(nextTaskPluginBaseUrl(pluginA, pluginA, ''), null)
  })

  test('changes nothing when the field already holds the new default', () => {
    assert.equal(nextTaskPluginBaseUrl(`${pluginB}/`, pluginA, pluginB), null)
  })
})
