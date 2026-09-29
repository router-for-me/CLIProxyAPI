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
import { afterEach, describe, expect, test, vi } from 'vitest'

import {
  DEFAULT_MARKETPLACE_INDEX_URL,
  GITHUB_MARKETPLACE_INDEX_URL,
} from '../lib/marketplace'
import {
  changelogLocale,
  fetchPluginChangelog,
  parsePluginChangelog,
  resolvePluginChangelogUrl,
} from '../lib/plugin-changelog'
import type { MarketplacePlugin } from '../types'

const plugin: MarketplacePlugin = {
  key: 'demo',
  name: 'Demo',
  latest: '1.2.0',
  versions: [{ version: '1.2.0', path: 'plugins/tasks/demo/1.2.0/plugin.js' }],
}
const indexUrl = 'https://example.com/repo/index.json'
const sourceUrl =
  'https://example.com/repo/plugins/tasks/demo/1.2.0/CHANGELOG.md'
const identity = { plugin: 'demo', version: '1.2.0', locale: 'en' }
const english = `---
changelogVersion: 1
plugin: demo
version: "1.2.0"
locale: en
translations:
  zh-CN: CHANGELOG.zh-CN.md
  ja: CHANGELOG.ja.md
---
# Changelog

## [1.2.0]

### Fixed

- Preserve **explicit \`seed: 0\`** and *false* values.
  See [source](plugin.js).
- Preserve &amp; entities and ~~old~~ labels.

### Migration

- Configure the new resolution tier's price.
`
const chinese = english
  .replace(
    'locale: en\ntranslations:\n  zh-CN: CHANGELOG.zh-CN.md\n  ja: CHANGELOG.ja.md',
    'locale: zh-CN'
  )
  .replace(
    "Configure the new resolution tier's price.",
    '配置新分辨率档位的价格。'
  )

function load(language = 'en', signal = new AbortController().signal) {
  return fetchPluginChangelog({
    indexUrl,
    plugin,
    version: '1.2.0',
    language,
    signal,
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('changelog path and language resolution', () => {
  test('resolves the registered sidecar under raw and third-party repository prefixes', () => {
    expect(resolvePluginChangelogUrl(indexUrl, plugin, '1.2.0')).toBe(sourceUrl)
    expect(
      resolvePluginChangelogUrl(GITHUB_MARKETPLACE_INDEX_URL, plugin, '1.2.0')
    ).toBe(
      'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/plugins/tasks/demo/1.2.0/CHANGELOG.md'
    )
    expect(resolvePluginChangelogUrl(indexUrl, plugin, '9.0.0')).toBeNull()
  })

  test('the official proxy index resolves only its registered key/version to the official repository', () => {
    const proxyPlugin = {
      ...plugin,
      versions: [{ version: '1.2.0', path: 'demo/1.2.0/plugin.js' }],
    }
    expect(
      resolvePluginChangelogUrl(
        DEFAULT_MARKETPLACE_INDEX_URL,
        proxyPlugin,
        '1.2.0'
      )
    ).toBe(
      'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/plugins/tasks/demo/1.2.0/CHANGELOG.md'
    )
    expect(
      resolvePluginChangelogUrl(
        DEFAULT_MARKETPLACE_INDEX_URL,
        { ...proxyPlugin, key: 'other' },
        '1.2.0'
      )
    ).toBeNull()
  })

  test.each([
    'https://other.example/plugin.js',
    'data:text/javascript,foo',
    'https://user:password@example.com/plugin.js',
    'old.js',
  ])('does not derive a changelog from an invalid source path %s', (path) => {
    expect(
      resolvePluginChangelogUrl(
        indexUrl,
        { ...plugin, versions: [{ version: '1.2.0', path }] },
        '1.2.0'
      )
    ).toBeNull()
  })

  test.each([
    ['zhCN', 'zh-CN'],
    ['zhTW', 'zh-TW'],
    ['zh', 'zh-CN'],
    ['zh_TW', 'zh-TW'],
    ['ja', 'ja'],
    ['en-US', 'en-US'],
    ['invalid_locale!', 'en'],
  ])('maps application language %s to %s', (input, expected) =>
    expect(changelogLocale(input)).toBe(expected)
  )
})

describe('changelog format', () => {
  test('extracts the declared release and preserves inline Markdown, wrapped prose and migration guidance', () => {
    const result = parsePluginChangelog(
      english.replaceAll('\n', '\r\n'),
      identity,
      sourceUrl
    )
    expect(result.document).toMatchObject({ ...identity, sourceUrl })
    expect(result.document.sections.map((section) => section.category)).toEqual(
      ['Fixed', 'Migration']
    )
    expect(result.document.sections[0].markdown).toContain(
      '**explicit `seed: 0`**'
    )
    expect(result.document.sections[0].markdown).toContain(
      'See [source](plugin.js).'
    )
    expect(result.document.sections[1].markdown).toContain('resolution tier')
  })

  test.each([
    [
      'format version',
      english.replace('changelogVersion: 1', 'changelogVersion: 2'),
    ],
    ['plugin identity', english.replace('plugin: demo', 'plugin: other')],
    [
      'version identity',
      english.replace('version: "1.2.0"', 'version: "1.1.0"'),
    ],
    ['locale identity', english.replace('locale: en', 'locale: ja')],
    [
      'duplicate YAML keys',
      english.replace('locale: en', 'locale: en\nlocale: en'),
    ],
    [
      'custom YAML tags',
      english.replace('plugin: demo', 'plugin: !execute demo'),
    ],
    [
      'YAML aliases',
      english
        .replace('plugin: demo', 'plugin: &id demo')
        .replace('version: "1.2.0"', 'version: *id'),
    ],
    [
      'translation traversal',
      english.replace('CHANGELOG.zh-CN.md', '../CHANGELOG.zh-CN.md'),
    ],
    [
      'translation URL',
      english.replace(
        'CHANGELOG.zh-CN.md',
        'https://other.example/CHANGELOG.zh-CN.md'
      ),
    ],
    ['unknown category', english.replace('### Fixed', '### Validation')],
    ['reordered categories', english.replace('### Migration', '### Added')],
    ['duplicate category', english.replace('### Migration', '### Fixed')],
    ['wrong heading', english.replace('## [1.2.0]', '## [1.1.0]')],
    [
      'empty category',
      english.replace("- Configure the new resolution tier's price.", ''),
    ],
    ['nested list', english.replace('- Configure', '- Setup\n  - Configure')],
    ['ordered list', english.replace('- Configure', '1. Configure')],
    ['checkbox list', english.replace('- Configure', '- [ ] Configure')],
    [
      'HTML',
      english.replace('- Configure', '- <script>alert(1)</script> Configure'),
    ],
    [
      'image',
      english.replace(
        '- Configure',
        '- ![image](https://other.example/img) Configure'
      ),
    ],
    ['additional heading', `${english}\n#### Notes\n- More.`],
    [
      'migration without changes',
      `${english.slice(
        0,
        english.indexOf('### Fixed')
      )}### Migration\n\n- Configure prices.`,
    ],
    ['oversized body', english + 'x'.repeat(256 * 1024)],
  ])('rejects %s', (_label, text) => {
    expect(() => parsePluginChangelog(text, identity, sourceUrl)).toThrow()
  })
})

describe('changelog downloads', () => {
  test('fetches the declared translation without credentials, redirects, or dashboard referrers', async () => {
    const fetchMock = vi.fn(
      async (url: string) => new Response(url === sourceUrl ? english : chinese)
    )
    vi.stubGlobal('fetch', fetchMock)
    const result = await load('zhCN')
    expect(result?.locale).toBe('zh-CN')
    expect(result?.sections[1].markdown).toContain('配置新分辨率档位的价格。')
    expect(fetchMock).toHaveBeenLastCalledWith(
      sourceUrl.replace('CHANGELOG.md', 'CHANGELOG.zh-CN.md'),
      expect.objectContaining({
        credentials: 'omit',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
      })
    )
  })

  test.each(['fr', 'ru', 'vi', 'zhTW', 'en-US'])(
    'falls back to English for %s rather than another available translation',
    async (language) => {
      const fetchMock = vi.fn(async () => new Response(english))
      vi.stubGlobal('fetch', fetchMock)
      expect((await load(language))?.locale).toBe('en')
      expect(fetchMock).toHaveBeenCalledTimes(1)
    }
  )

  test.each(['404', 'network', 'identity', 'categories'])(
    'falls back to English when the requested translation fails with %s',
    async (failure) => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url === sourceUrl) return new Response(english)
          if (failure === 'network') throw new Error('network failure')
          if (failure === '404') return new Response(null, { status: 404 })
          return new Response(
            failure === 'identity'
              ? chinese.replace('plugin: demo', 'plugin: other')
              : chinese.replace('### Fixed', '### Added')
          )
        })
      )
      expect((await load('zhCN'))?.locale).toBe('en')
    }
  )

  test('a canonical 404 is an empty release; network and format failures remain retryable errors', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 404 }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response('invalid'))
    vi.stubGlobal('fetch', fetchMock)
    expect(await load()).toBeNull()
    await expect(load()).rejects.toThrow()
    await expect(load()).rejects.toThrow()
  })

  test('cancellation during a translation download is propagated instead of returned as English', async () => {
    const controller = new AbortController()
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url === sourceUrl) return new Response(english)
        controller.abort()
        throw controller.signal.reason
      })
    )
    await expect(load('zhCN', controller.signal)).rejects.toMatchObject({
      name: 'AbortError',
    })
  })

  test('rejects oversized streamed content and invalid UTF-8', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(new Response('x'.repeat(256 * 1024 + 1)))
        .mockResolvedValueOnce(new Response(new Uint8Array([0xff])))
    )
    await expect(load()).rejects.toThrow()
    await expect(load()).rejects.toThrow()
  })
})
