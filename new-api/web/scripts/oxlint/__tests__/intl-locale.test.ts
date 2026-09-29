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
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterAll, beforeAll, describe, expect, it } from 'vitest'

const cases = [
  ['raw-language', '(1234).toLocaleString(i18n.language)', true],
  [
    'raw-resolved-language',
    'Intl.NumberFormat(i18n.resolvedLanguage || i18n.language)',
    true,
  ],
  [
    'optional-computed-member',
    'new Intl.NumberFormat(i18n?.["language"])',
    true,
  ],
  ['type-assertion', 'new Intl.NumberFormat(i18n.language as string)', true],
  [
    'variable-alias',
    'const locale = i18n.language; const alias = locale; new Intl.NumberFormat(alias)',
    true,
  ],
  [
    'assigned-alias',
    'let locale; locale = i18n.language; new Intl.NumberFormat(locale)',
    true,
  ],
  [
    'destructured-alias',
    'const { resolvedLanguage: locale } = i18n; new Intl.NumberFormat(locale)',
    true,
  ],
  ['locale-array', 'new Intl.NumberFormat(["en", i18n.language])', true],
  ['invalid-literal', 'new Intl.NumberFormat("zhCN")', true],
  ['invalid-template', 'new Intl.NumberFormat(`zhTW`)', true],
  ['date-format', 'new Intl.DateTimeFormat(i18n.language)', true],
  ['relative-time', 'new Intl.RelativeTimeFormat(i18n.language)', true],
  ['date-method', 'new Date().toLocaleDateString(i18n.language)', true],
  ['time-method', 'new Date().toLocaleTimeString(i18n.language)', true],
  ['shared-number-format', 'formatNumber(1234, i18n.language)', true],
  ['shared-compact-format', 'formatCompactNumber(1234, i18n.language)', true],
  [
    'shared-relative-format',
    'formatTimestampRelative(1234, "seconds", i18n.language)',
    true,
  ],
  [
    'shared-import-alias',
    'import { formatNumber as number } from "@/lib/format"; number(1234, i18n.language)',
    true,
  ],
  [
    'jsx-render',
    'const view = <span>{(1234).toLocaleString(i18n.language)}</span>',
    true,
  ],
  [
    'converted-inline',
    'import { toIntlLocale } from "@/i18n/languages"; new Intl.NumberFormat(toIntlLocale(i18n.language))',
    false,
  ],
  [
    'converted-alias',
    'import { toIntlLocale } from "@/i18n/languages"; const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language); formatNumber(1234, locale)',
    false,
  ],
  [
    'converter-import-alias',
    'import { toIntlLocale as normalize } from "@/i18n/languages"; new Intl.NumberFormat(normalize(i18n.language))',
    false,
  ],
  [
    'standard-locales',
    'new Intl.NumberFormat(["en", "zh-CN", "zh-TW", "fr", "ru", "ja", "vi"])',
    false,
  ],
  [
    'default-locale',
    '(1234).toLocaleString(); new Intl.NumberFormat(undefined)',
    false,
  ],
  [
    'shadowed-variable',
    'const locale = i18n.language; function display() { const locale = "en"; return new Intl.NumberFormat(locale) }',
    false,
  ],
  [
    'language-switch',
    'i18n.changeLanguage("zhCN"); const language = i18n.language',
    false,
  ],
] as const

type Diagnostic = { filename: string; code: string; severity: string }

let directory: string
let diagnostics: Diagnostic[]

beforeAll(() => {
  directory = mkdtempSync(join(tmpdir(), 'new-api-intl-lint-'))
  for (const [name, code] of cases) {
    writeFileSync(join(directory, `${name}.tsx`), code)
  }
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
  const result = spawnSync(
    process.execPath,
    [
      join(root, 'node_modules/oxlint/bin/oxlint'),
      '-c',
      join(root, '.oxlintrc.json'),
      '--format',
      'json',
      directory,
    ],
    { cwd: root, encoding: 'utf8' }
  )
  expect(result.error).toBeUndefined()
  expect([0, 1], result.stderr).toContain(result.status)
  diagnostics = JSON.parse(result.stdout).diagnostics
})

afterAll(() => {
  if (directory) rmSync(directory, { recursive: true, force: true })
})

describe('configured Intl locale lint rule', () => {
  it.each(cases)(
    '%s matches the expected lint result',
    (name, _code, unsafe) => {
      const errors = diagnostics.filter(
        (item) =>
          basename(item.filename) === `${name}.tsx` && item.severity === 'error'
      )
      if (unsafe) {
        expect(errors).toHaveLength(1)
        expect(errors[0].code).toContain('intl-locale')
      } else {
        expect(errors).toEqual([])
      }
    }
  )
})
