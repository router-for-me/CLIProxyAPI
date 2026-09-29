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
  pluginIconUrl,
  resolvePluginIcon,
  TEXT_AVATAR_PALETTE,
  textAvatarClass,
} from '../lib/plugin-icon'

describe('resolvePluginIcon', () => {
  test('uses icon when present even if channelTypes exist', () => {
    assert.deepEqual(
      resolvePluginIcon({
        icon: 'Sora.Color',
        channelTypes: [1],
        key: 'sora',
      }),
      { kind: 'lobe', name: 'Sora.Color' }
    )
  })

  test('uses a text avatar when icon is absent even with a declared channel type', () => {
    assert.deepEqual(
      resolvePluginIcon({
        channelTypes: [36],
        key: 'sunoapi',
        name: 'SunoAPI',
      }),
      { kind: 'text', label: 'SU', colorSeed: 'sunoapi' }
    )
  })

  test('renders a text avatar when neither icon nor channelTypes are present', () => {
    assert.deepEqual(resolvePluginIcon({ key: 'third-party-plugin' }), {
      kind: 'text',
      label: 'TH',
      colorSeed: 'third-party-plugin',
    })
  })

  test('derives the text label from name over key when both exist', () => {
    assert.deepEqual(
      resolvePluginIcon({ key: 'vendor-x', name: 'My Plugin' }),
      { kind: 'text', label: 'MY', colorSeed: 'vendor-x' }
    )
  })

  test('icon "text" wins over channelTypes so branded borrow is suppressed', () => {
    assert.deepEqual(
      resolvePluginIcon({
        icon: 'text',
        channelTypes: [36],
        key: 'sunoapi',
        name: 'SunoAPI',
      }),
      { kind: 'text', label: 'SU', colorSeed: 'sunoapi' }
    )
  })

  test('icon "text:<label>" uses the explicit label capped at 4 characters', () => {
    assert.deepEqual(
      resolvePluginIcon({ icon: 'text:Suno API', key: 'sunoapi' }),
      { kind: 'text', label: 'Suno', colorSeed: 'sunoapi' }
    )
  })

  test('icon "text:" with empty label falls back to the derived label', () => {
    assert.deepEqual(
      resolvePluginIcon({ icon: 'text:', key: 'sunoapi', name: 'SunoAPI' }),
      { kind: 'text', label: 'SU', colorSeed: 'sunoapi' }
    )
  })
})

describe('resolvePluginIcon image logos', () => {
  test('serves a gateway-held sidecar logo through the icon endpoint, ahead of meta.icon and channelTypes', () => {
    assert.deepEqual(
      resolvePluginIcon({
        icon: 'Sora.Color',
        channelTypes: [1],
        key: 'in cho',
        hasIcon: true,
      }),
      { kind: 'image', src: '/api/plugin/task/in%20cho/icon' }
    )
  })

  test('prefers an explicit image source over the gateway endpoint', () => {
    assert.deepEqual(
      resolvePluginIcon({
        key: 'incho',
        hasIcon: true,
        iconSrc: 'https://raw.example/plugins/tasks/incho/icon.svg',
      }),
      { kind: 'image', src: 'https://raw.example/plugins/tasks/incho/icon.svg' }
    )
  })

  test('ignores an inline data URI in meta.icon and falls back to the text avatar', () => {
    assert.deepEqual(
      resolvePluginIcon({
        icon: 'data:image/png;base64,iVBORw0KGgo=',
        channelTypes: [1],
        key: 'x',
      }),
      { kind: 'text', label: 'X', colorSeed: 'x' }
    )
  })

  test('ignores a remote URL in meta.icon and falls back to the text avatar', () => {
    assert.deepEqual(
      resolvePluginIcon({
        icon: 'https://evil.example/icon.png',
        key: 'x',
        name: 'Suno',
      }),
      { kind: 'text', label: 'SU', colorSeed: 'x' }
    )
  })
})

describe('pluginIconUrl', () => {
  test('pins a version through the query string', () => {
    assert.equal(
      pluginIconUrl('incho', '1.0.1'),
      '/api/plugin/task/incho/icon?version=1.0.1'
    )
  })
})

describe('textAvatarClass', () => {
  test('is deterministic for the same seed', () => {
    assert.equal(textAvatarClass('sunoapi'), textAvatarClass('sunoapi'))
  })

  test('always picks from the palette', () => {
    for (const seed of ['a', 'sunoapi', 'third-party-plugin', '插件', '']) {
      assert.ok(
        (TEXT_AVATAR_PALETTE as readonly string[]).includes(
          textAvatarClass(seed)
        )
      )
    }
  })
})
