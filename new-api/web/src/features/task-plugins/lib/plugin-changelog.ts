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
import { Lexer, type MarkedToken, type Token } from 'marked'
import { parseDocument } from 'yaml'
import { z } from 'zod'

import type { MarketplacePlugin } from '../types'
import {
  DEFAULT_MARKETPLACE_INDEX_URL,
  GITHUB_MARKETPLACE_INDEX_URL,
  findMarketplaceVersion,
  resolvePluginSourceUrl,
} from './marketplace'

export const CHANGELOG_CATEGORIES = [
  'Added',
  'Changed',
  'Deprecated',
  'Removed',
  'Fixed',
  'Security',
  'Migration',
] as const
export type ChangelogCategory = (typeof CHANGELOG_CATEGORIES)[number]
export type PluginChangelog = {
  plugin: string
  version: string
  locale: string
  sourceUrl: string
  sections: { category: ChangelogCategory; markdown: string }[]
}

type ChangelogIdentity = { plugin: string; version: string; locale: string }
const MAX_CHANGELOG_BYTES = 256 * 1024
const metadataSchema = z
  .object({
    changelogVersion: z.literal(1),
    plugin: z.string().min(1),
    version: z.string().min(1),
    locale: z.string().min(1),
    translations: z.record(z.string(), z.string()).optional(),
  })
  .strict()

/** Translate the app's language aliases to canonical release-note language tags. */
export function changelogLocale(language: string): string {
  const normalized = language.trim().replaceAll('_', '-').toLowerCase()
  if (['zh', 'zhcn', 'zh-cn', 'zh-hans'].includes(normalized)) return 'zh-CN'
  if (['zhtw', 'zh-tw', 'zh-hant'].includes(normalized)) return 'zh-TW'
  try {
    return Intl.getCanonicalLocales(normalized)[0] ?? 'en'
  } catch {
    return 'en'
  }
}

/** Resolve the sidecar for an index-registered version, including the official proxy index. */
export function resolvePluginChangelogUrl(
  indexUrl: string,
  plugin: MarketplacePlugin,
  version: string
): string | null {
  const entry = findMarketplaceVersion(plugin, version)
  if (!entry) return null
  const source = resolvePluginSourceUrl(indexUrl, entry.path)
  if (!source) return null
  const url = new URL(source)
  if (url.username || url.password || !url.pathname.endsWith('/plugin.js')) {
    return null
  }
  // The official website rewrites plugin paths into download API URLs. Its
  // release sidecars still live beside the same key/version in the official repo.
  if (indexUrl.trim() === DEFAULT_MARKETPLACE_INDEX_URL) {
    if (
      ![plugin.key, version].every(
        (id) => /^[A-Za-z0-9._-]+$/.test(id) && id !== '.' && id !== '..'
      )
    ) {
      return null
    }
    const expected = new URL(`${plugin.key}/${version}/plugin.js`, indexUrl)
    if (url.pathname !== expected.pathname) return null
    return new URL(
      `plugins/tasks/${plugin.key}/${version}/CHANGELOG.md`,
      GITHUB_MARKETPLACE_INDEX_URL
    ).href
  }
  return new URL('CHANGELOG.md', url).href
}

/** No HTML, images, task checkboxes or nested blocks can become marketplace content. */
function validateChangelogInline(tokens: Token[], depth = 0): void {
  if (depth > 32) throw new Error('Changelog inline nesting exceeds the limit')
  for (const token of tokens as MarkedToken[]) {
    switch (token.type) {
      case 'text':
        if (token.tokens) validateChangelogInline(token.tokens, depth + 1)
        break
      case 'escape':
      case 'codespan':
      case 'br':
        break
      case 'strong':
      case 'em':
      case 'del':
      case 'link':
        validateChangelogInline(token.tokens, depth + 1)
        break
      default:
        throw new Error('Unsupported changelog content')
    }
  }
}

export function parsePluginChangelog(
  text: string,
  identity: ChangelogIdentity,
  sourceUrl: string
): { document: PluginChangelog; translations: Record<string, string> } {
  if (new TextEncoder().encode(text).length > MAX_CHANGELOG_BYTES) {
    throw new Error('Changelog exceeds the size limit')
  }
  const lines = text.replaceAll('\r\n', '\n').split('\n')
  const end = lines.indexOf('---', 1)
  if (lines[0] !== '---' || end < 1) {
    throw new Error('Missing changelog metadata')
  }
  const yaml = parseDocument(lines.slice(1, end).join('\n'), {
    schema: 'core',
    uniqueKeys: true,
    stringKeys: true,
  })
  if (yaml.errors.length || yaml.warnings.length) {
    throw new Error('Invalid changelog metadata')
  }
  const metadata = metadataSchema.parse(yaml.toJS({ maxAliasCount: 0 }))
  if (
    metadata.plugin !== identity.plugin ||
    metadata.version !== identity.version ||
    metadata.locale !== identity.locale
  ) {
    throw new Error('Changelog identity does not match the selected version')
  }
  if (
    Intl.getCanonicalLocales(metadata.locale)[0] !== metadata.locale ||
    (metadata.locale !== 'en' && metadata.translations !== undefined)
  ) {
    throw new Error('Invalid changelog locale')
  }
  const translations = metadata.translations ?? {}
  for (const [locale, file] of Object.entries(translations)) {
    if (
      locale === 'en' ||
      Intl.getCanonicalLocales(locale)[0] !== locale ||
      file !== `CHANGELOG.${locale}.md`
    ) {
      throw new Error('Invalid changelog translation filename')
    }
  }
  const tokens = Lexer.lex(lines.slice(end + 1).join('\n'), {
    gfm: true,
  }).filter((token) => token.type !== 'space') as MarkedToken[]
  const title = tokens[0]
  const release = tokens[1]
  if (
    title?.type !== 'heading' ||
    title.depth !== 1 ||
    title.text !== 'Changelog' ||
    release?.type !== 'heading' ||
    release.depth !== 2 ||
    release.text !== `[${identity.version}]`
  ) {
    throw new Error('Invalid changelog release heading')
  }
  const sections: PluginChangelog['sections'] = []
  let previousCategory = -1
  for (let i = 2; i < tokens.length; i += 2) {
    const heading = tokens[i]
    const list = tokens[i + 1]
    if (
      heading?.type !== 'heading' ||
      heading.depth !== 3 ||
      list?.type !== 'list' ||
      list.ordered ||
      !list.items.length
    ) {
      throw new Error('Invalid changelog category or list')
    }
    const categoryIndex = (CHANGELOG_CATEGORIES as readonly string[]).indexOf(
      heading.text
    )
    if (categoryIndex <= previousCategory) {
      throw new Error('Invalid changelog category order')
    }
    previousCategory = categoryIndex
    for (const item of list.items) {
      const blocks = item.tokens.filter(
        (token) => token.type !== 'space'
      ) as MarkedToken[]
      const paragraph = blocks[0]
      if (
        item.task ||
        blocks.length !== 1 ||
        (paragraph?.type !== 'text' && paragraph?.type !== 'paragraph') ||
        !paragraph.text.trim() ||
        !paragraph.tokens
      ) {
        throw new Error('Changelog entries must be nonempty inline paragraphs')
      }
      validateChangelogInline(paragraph.tokens)
    }
    sections.push({
      category: CHANGELOG_CATEGORIES[categoryIndex],
      markdown: list.raw,
    })
  }
  if (!sections.some((section) => section.category !== 'Migration')) {
    throw new Error('Changelog must contain a change')
  }
  return { document: { ...identity, sourceUrl, sections }, translations }
}

async function fetchChangelogFile(
  url: string,
  signal: AbortSignal
): Promise<string | null> {
  signal.throwIfAborted()
  const response = await fetch(url, {
    signal,
    credentials: 'omit',
    redirect: 'error',
    referrerPolicy: 'no-referrer',
  })
  if (response.status === 404) {
    await response.body?.cancel()
    return null
  }
  if (!response.ok) {
    await response.body?.cancel()
    throw new Error('Changelog download failed')
  }
  const reader = response.body?.getReader()
  if (!reader) throw new Error('Missing changelog body')
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let bytes = 0
  let text = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      bytes += value.byteLength
      if (bytes > MAX_CHANGELOG_BYTES) {
        throw new Error('Changelog exceeds the size limit')
      }
      text += decoder.decode(value, { stream: true })
    }
    return text + decoder.decode()
  } finally {
    await reader.cancel()
  }
}

export async function fetchPluginChangelog(options: {
  indexUrl: string
  plugin: MarketplacePlugin
  version: string
  language: string
  signal: AbortSignal
}): Promise<PluginChangelog | null> {
  const url = resolvePluginChangelogUrl(
    options.indexUrl,
    options.plugin,
    options.version
  )
  if (!url) throw new Error('No changelog path for the selected version')
  const signal = AbortSignal.any([options.signal, AbortSignal.timeout(15_000)])
  const text = await fetchChangelogFile(url, signal)
  if (text === null) return null
  const identity = {
    plugin: options.plugin.key,
    version: options.version,
    locale: 'en',
  }
  const canonical = parsePluginChangelog(text, identity, url)
  const locale = changelogLocale(options.language)
  const filename = canonical.translations[locale]
  if (filename) {
    try {
      const translatedUrl = new URL(filename, url).href
      const translation = await fetchChangelogFile(translatedUrl, signal)
      if (translation !== null) {
        const translated = parsePluginChangelog(
          translation,
          { ...identity, locale },
          translatedUrl
        )
        if (
          translated.document.sections.length ===
            canonical.document.sections.length &&
          translated.document.sections.every(
            (section, i) =>
              section.category === canonical.document.sections[i].category
          )
        ) {
          return translated.document
        }
      }
    } catch {
      // A missing, invalid or unavailable translation always falls back to English.
      options.signal.throwIfAborted()
    }
  }
  return canonical.document
}
