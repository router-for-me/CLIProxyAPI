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

const TARGET_DIRS = ['src', 'scripts']
const SOURCE_EXTENSIONS = new Set([
  '.cjs',
  '.css',
  '.js',
  '.jsx',
  '.mjs',
  '.scss',
  '.ts',
  '.tsx',
])
const EXCLUDED_DIRS = new Set([
  '.git',
  '.rsbuild',
  '.turbo',
  'build',
  'coverage',
  'dist',
  'node_modules',
])
const GENERATED_FILE_MARKERS = [
  'This file was automatically generated',
  'This file is auto-generated',
  'This file is generated',
  'DO NOT EDIT',
  'You should NOT make any changes in this file',
]

const COPYRIGHT_HEADER = `/*
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
`

const PROJECT_COPYRIGHT_BLOCK_PATTERN =
  /^\/\*\r?\nCopyright \(C\) .+? QuantumNous\r?\n[\s\S]*?For commercial licensing, please contact support@quantumnous\.com\r?\n\*\/\r?\n?/
const THIRD_PARTY_COPYRIGHT_PATTERN =
  /^\/\*[\s\S]*?Copyright[\s\S]*?\*\/\r?\n?/i

const checkMode = process.argv.includes('--check')

function isGeneratedFile(filePath) {
  return path.basename(filePath).includes('.gen.')
}

function hasGeneratedMarker(text) {
  return GENERATED_FILE_MARKERS.some((marker) => text.includes(marker))
}

function hasThirdPartyCopyright(text) {
  return (
    THIRD_PARTY_COPYRIGHT_PATTERN.test(text) &&
    !PROJECT_COPYRIGHT_BLOCK_PATTERN.test(text)
  )
}

async function collectSourceFiles(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true })
  const files = []

  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name)

    if (entry.isDirectory()) {
      if (!EXCLUDED_DIRS.has(entry.name)) {
        files.push(...(await collectSourceFiles(fullPath)))
      }
      continue
    }

    if (
      entry.isFile() &&
      SOURCE_EXTENSIONS.has(path.extname(entry.name)) &&
      !isGeneratedFile(fullPath)
    ) {
      files.push(fullPath)
    }
  }

  return files
}

async function collectTargetFiles(rootDir) {
  const files = []

  for (const targetDir of TARGET_DIRS) {
    const fullPath = path.join(rootDir, targetDir)

    try {
      const stat = await fs.stat(fullPath)
      if (stat.isDirectory()) {
        files.push(...(await collectSourceFiles(fullPath)))
      }
    } catch (error) {
      if (error.code !== 'ENOENT') {
        throw error
      }
    }
  }

  return files.sort()
}

function splitShebang(text) {
  if (!text.startsWith('#!')) {
    return ['', text]
  }

  const lineEnd = text.indexOf('\n')
  if (lineEnd === -1) {
    return [text, '']
  }

  return [text.slice(0, lineEnd + 1), text.slice(lineEnd + 1)]
}

function applyHeader(text) {
  const newline = text.includes('\r\n') ? '\r\n' : '\n'
  const header = COPYRIGHT_HEADER.replaceAll('\n', newline)
  const [shebang, body] = splitShebang(text)
  const hadHeader = PROJECT_COPYRIGHT_BLOCK_PATTERN.test(body)
  let strippedBody = body
  while (PROJECT_COPYRIGHT_BLOCK_PATTERN.test(strippedBody)) {
    strippedBody = strippedBody
      .replace(PROJECT_COPYRIGHT_BLOCK_PATTERN, '')
      .replace(/^(?:\r?\n)+/, '')
  }

  if (strippedBody.length === 0) {
    return {
      action: hadHeader ? 'updated' : 'added',
      text: shebang + header,
    }
  }

  return {
    action: hadHeader ? 'updated' : 'added',
    text: shebang + header + strippedBody,
  }
}

function formatPath(rootDir, filePath) {
  return path.relative(rootDir, filePath).replaceAll(path.sep, '/')
}

async function main() {
  const rootDir = process.cwd()
  const sourceFiles = await collectTargetFiles(rootDir)
  const stats = {
    added: 0,
    checked: 0,
    skippedGenerated: 0,
    skippedThirdParty: 0,
    updated: 0,
  }
  const pendingFiles = []

  for (const file of sourceFiles) {
    stats.checked += 1

    const originalText = await fs.readFile(file, 'utf8')
    const bom = originalText.startsWith('\uFEFF') ? '\uFEFF' : ''
    const text = bom ? originalText.slice(1) : originalText
    const [, body] = splitShebang(text)

    if (hasGeneratedMarker(body)) {
      stats.skippedGenerated += 1
      continue
    }

    if (hasThirdPartyCopyright(body)) {
      stats.skippedThirdParty += 1
      continue
    }

    const result = applyHeader(text)
    const nextText = bom + result.text

    if (nextText !== originalText) {
      stats[result.action] += 1
      pendingFiles.push(formatPath(rootDir, file))

      if (!checkMode) {
        await fs.writeFile(file, nextText)
      }
    }
  }

  console.log(
    [
      `copyright: checked ${stats.checked}`,
      `added ${stats.added}`,
      `updated ${stats.updated}`,
      `skipped generated ${stats.skippedGenerated}`,
      `skipped third-party ${stats.skippedThirdParty}`,
    ].join(', ')
  )

  if (checkMode && pendingFiles.length > 0) {
    console.error('copyright: headers need to be updated in:')
    for (const file of pendingFiles) {
      console.error(`- ${file}`)
    }
    process.exitCode = 1
  }
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
