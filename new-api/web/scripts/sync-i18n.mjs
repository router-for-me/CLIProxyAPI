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
import fs from 'node:fs/promises'
import path from 'node:path'

// This script is executed from the web/ package root (see package.json script).
const LOCALES_DIR = path.resolve('src/i18n/locales')
const FALLBACK_COMPARE_LOCALE = 'en' // used for "still English" detection only
const OBFUSCATED_KEYS = [
  {
    runtime: ['footer', 'new' + 'api', 'projectAttributionSuffix'].join('.'),
    serialized: 'footer.new\\u0061pi.projectAttributionSuffix',
  },
]

const BRAND_AND_LITERAL_KEYS = new Set([
  '1M token',
  'AI Proxy',
  'AIGC2D',
  'Alipay',
  'Anthropic',
  'API URL',
  'API2GPT',
  'AccessKey / SecretAccessKey',
  'AZURE_OPENAI_ENDPOINT *',
  'Baidu V2',
  'CC Switch',
  'ChatGPT',
  'ChatGPT Subscription (Codex)',
  'Claude',
  'Client ID',
  'Client Secret',
  'Cloudflare',
  'Cohere',
  'credit',
  'DeepSeek',
  'Discord',
  'DoubaoVideo',
  'FastGPT',
  'Gemini',
  'Gemini Image 4K',
  'GitHub',
  'Jimeng',
  'JustSong',
  'LingYiWanWu',
  'LinuxDO',
  'MjProxy',
  'MjProxyPlus',
  'MiniMax',
  'Mistral',
  'MokaAI',
  'Moonshot',
  'New API',
  'New API &lt;noreply@example.com&gt;',
  'NewAPI',
  'OAuth Client Secret',
  'OhMyGPT',
  'Ollama',
  'One API',
  'OpenAI',
  'OpenAIMax',
  'OpenRouter',
  'Pancake',
  'Passkey',
  'Perplexity',
  'QuantumNous',
  'Quota:',
  'Replicate',
  'SiliconFlow',
  'Stripe',
  'Submodel',
  'SunoAPI',
  'Telegram',
  'Tencent',
  'TTFT P50',
  'TTFT P95',
  'TTFT P99',
  'Uptime Kuma',
  'Uptime Kuma URL',
  'Vertex AI',
  'VolcEngine',
  'Waffo Pancake Dashboard',
  'Waffo Pancake MoR',
  'WeChat',
  'WeChat Pay',
  'Webhook URL',
  'Webhook URL:',
  'Well-Known URL',
  'Worker URL',
  'Xinference',
  'Xunfei',
  'Zhipu V4',
  '"default": "us-central1", "claude-3-5-sonnet-20240620": "europe-west1"',
  'edit_this',
  'footer.columns.related.links.midjourney',
  'footer.columns.related.links.newApiKeyTool',
  'my-status',
  'new-api-key-tool',
  'price_xxx',
  'whsec_xxx',
])

function isPlainObject(v) {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function stableStringify(obj) {
  let text = JSON.stringify(obj, null, 2)
  for (const key of OBFUSCATED_KEYS) {
    text = text.replaceAll(`"${key.runtime}":`, `"${key.serialized}":`)
  }
  return text + '\n'
}

function countLeafKeys(obj) {
  if (Array.isArray(obj)) return obj.length
  if (!isPlainObject(obj)) return 0
  let count = 0
  for (const k of Object.keys(obj)) {
    const v = obj[k]
    if (isPlainObject(v) || Array.isArray(v)) count += countLeafKeys(v)
    else count += 1
  }
  return count
}

function reorderLikeBase(
  base,
  target,
  fill,
  extras,
  missing,
  currentPath = []
) {
  // If base is an object, we keep base's key order and recurse.
  if (isPlainObject(base)) {
    const out = {}
    const t = isPlainObject(target) ? target : {}
    const f = isPlainObject(fill) ? fill : {}

    for (const key of Object.keys(base)) {
      const nextPath = [...currentPath, key]
      if (Object.prototype.hasOwnProperty.call(t, key)) {
        out[key] = reorderLikeBase(
          base[key],
          t[key],
          f[key],
          extras,
          missing,
          nextPath
        )
      } else {
        missing.push(nextPath.join('.'))
        out[key] = reorderLikeBase(
          base[key],
          undefined,
          f[key],
          extras,
          missing,
          nextPath
        )
      }
    }

    for (const key of Object.keys(t)) {
      if (!Object.prototype.hasOwnProperty.call(base, key)) {
        const nextPath = [...currentPath, key].join('.')
        extras[nextPath] = t[key]
      }
    }

    return out
  }

  // For arrays: prefer target if it's also an array; otherwise use base.
  if (Array.isArray(base)) {
    if (Array.isArray(target)) return target
    if (Array.isArray(fill)) return fill
    return base
  }

  // For primitives: prefer target if defined, else base.
  return target === undefined ? (fill ?? base) : target
}

function isLikelyUntranslated({ locale, baseValue, value }) {
  if (typeof value !== 'string' || typeof baseValue !== 'string') return false
  if (value !== baseValue) return false

  // Skip short tokens / acronyms / ids
  const s = baseValue.trim()
  if (BRAND_AND_LITERAL_KEYS.has(s)) return false
  if (
    /^https?:\/\//.test(s) ||
    /^\/[\w/-]+/.test(s) ||
    /^[\w.-]+@[\w.-]+$/.test(s) ||
    /^smtp\./i.test(s) ||
    /^socks5:/i.test(s) ||
    /^org-/.test(s) ||
    /^gpt-/i.test(s) ||
    /^checkout\./.test(s) ||
    /^footer\./.test(s) ||
    /^[A-Z0-9_ *./:-]+$/.test(s) ||
    s.startsWith('{') ||
    s.startsWith('[') ||
    s.includes('&#10;')
  ) {
    return false
  }
  if (s.length < 6) return false
  if (!/[A-Za-z]{3,}/.test(s)) return false

  // For locales with non-latin scripts, equality with EN is a strong signal.
  if (locale === 'ja' || locale === 'zh') return true
  if (locale === 'ru') return true

  // For fr/vi: still useful but noisier; keep it conservative.
  if (locale === 'fr' || locale === 'vi')
    return /\b(the|and|or|to|with|please)\b/i.test(s)

  return false
}

async function main() {
  const entries = await fs.readdir(LOCALES_DIR, { withFileTypes: true })
  const localeFiles = entries
    .filter((e) => e.isFile() && e.name.endsWith('.json'))
    .map((e) => e.name)
    .sort((a, b) => a.localeCompare(b))

  // Auto-pick base locale as the one with the most leaf keys under translation (most "rich").
  const parsedByLocale = {}
  for (const filename of localeFiles) {
    const locale = filename.replace(/\.json$/i, '')
    const raw = await fs.readFile(path.join(LOCALES_DIR, filename), 'utf8')
    parsedByLocale[locale] = JSON.parse(raw)
  }

  const baseLocale = Object.keys(parsedByLocale)
    .map((locale) => {
      const json = parsedByLocale[locale]
      const trans = json?.translation ?? {}
      return { locale, score: countLeafKeys(trans) }
    })
    .sort(
      (a, b) => b.score - a.score || a.locale.localeCompare(b.locale)
    )[0]?.locale

  if (!baseLocale) throw new Error('No locale files found.')

  const baseFile = `${baseLocale}.json`
  const baseJson = parsedByLocale[baseLocale]

  const compareJson = parsedByLocale[FALLBACK_COMPARE_LOCALE] ?? baseJson

  const report = {
    base: baseFile,
    locales: {},
  }

  const extrasDir = path.join(LOCALES_DIR, '_extras')
  const reportsDir = path.join(LOCALES_DIR, '_reports')
  await fs.mkdir(extrasDir, { recursive: true })
  await fs.mkdir(reportsDir, { recursive: true })

  for (const filename of localeFiles) {
    const locale = filename.replace(/\.json$/i, '')
    const full = path.join(LOCALES_DIR, filename)
    const json = parsedByLocale[locale]

    const extras = {}
    const missing = []
    const fixed = reorderLikeBase(baseJson, json, compareJson, extras, missing)

    // Untranslated scan (translation namespace only)
    const untranslated = {}
    const compareTrans = compareJson?.translation ?? {}
    const trans = fixed?.translation ?? {}
    if (
      isPlainObject(compareTrans) &&
      isPlainObject(trans) &&
      locale !== FALLBACK_COMPARE_LOCALE &&
      locale !== baseLocale
    ) {
      for (const k of Object.keys(compareTrans)) {
        const baseValue = compareTrans[k]
        const value = trans[k]
        if (isLikelyUntranslated({ locale, baseValue, value })) {
          untranslated[k] = value
        }
      }
    }

    report.locales[locale] = {
      file: filename,
      missingCount: missing.length,
      extrasCount: Object.keys(extras).length,
      untranslatedCount: Object.keys(untranslated).length,
    }

    if (Object.keys(extras).length > 0) {
      await fs.writeFile(
        path.join(extrasDir, `${locale}.extras.json`),
        stableStringify(extras),
        'utf8'
      )
    } else {
      await fs.rm(path.join(extrasDir, `${locale}.extras.json`), {
        force: true,
      })
    }
    if (Object.keys(untranslated).length > 0) {
      await fs.writeFile(
        path.join(reportsDir, `${locale}.untranslated.json`),
        stableStringify(untranslated),
        'utf8'
      )
    } else {
      await fs.rm(path.join(reportsDir, `${locale}.untranslated.json`), {
        force: true,
      })
    }

    // Rewrite locale file in base order (even for en to normalize formatting)
    await fs.writeFile(full, stableStringify(fixed), 'utf8')
  }

  await fs.writeFile(
    path.join(reportsDir, '_sync-report.json'),
    stableStringify(report),
    'utf8'
  )

  console.log(
    `i18n sync done. Report: ${path.join(reportsDir, '_sync-report.json')}`
  )
}

main().catch((err) => {
  console.error(err)
  process.exitCode = 1
})
