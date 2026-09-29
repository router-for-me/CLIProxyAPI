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
import type { ReactNode } from 'react'
import type { ParsedNode } from 'stream-markdown-parser'

import { isFootnoteNode } from './response-node-guards'
import type { ParsedResponseContent } from './response-types'

const FENCE_START_PATTERN = /^(`{3,}|~{3,})([^\n]*)$/
const FENCE_END_PATTERN = /^(`{3,}|~{3,})\s*$/
const SECTION_HEADING_PATTERN = /^#{2,6}\s+\d+\.\s+/
const MARKDOWN_EXAMPLE_LANGUAGES = new Set(['markdown', 'md', 'mdx'])

type MarkdownExampleFence = {
  contentLines: string[]
  fenceChar: string
  language: string
  nestedFence: boolean
}

function getFenceRunLength(line: string, fenceChar: string): number {
  let length = 0

  for (const char of line) {
    if (char !== fenceChar) {
      break
    }

    length++
  }

  return length
}

function getMarkdownExampleFenceLength(block: MarkdownExampleFence): number {
  let maxFenceLength = 3

  for (const line of block.contentLines) {
    if (!line.startsWith(block.fenceChar)) {
      continue
    }

    maxFenceLength = Math.max(
      maxFenceLength,
      getFenceRunLength(line, block.fenceChar) + 1
    )
  }

  return maxFenceLength
}

function appendMarkdownExampleFence(
  output: string[],
  block: MarkdownExampleFence
): void {
  const fence = block.fenceChar.repeat(getMarkdownExampleFenceLength(block))

  output.push(`${fence}${block.language}`)
  output.push(...block.contentLines)
  output.push(fence)
}

function normalizeMarkdownExampleFences(input: string): string {
  const lines = input.split('\n')
  const output: string[] = []
  let exampleFence: MarkdownExampleFence | null = null

  for (const line of lines) {
    if (!exampleFence) {
      const match = line.match(FENCE_START_PATTERN)

      if (!match) {
        output.push(line)
        continue
      }

      const language = match[2].trim().toLowerCase()
      if (MARKDOWN_EXAMPLE_LANGUAGES.has(language)) {
        exampleFence = {
          contentLines: [],
          fenceChar: match[1][0],
          language,
          nestedFence: false,
        }
        continue
      }

      output.push(line)
      continue
    }

    if (!exampleFence.nestedFence && SECTION_HEADING_PATTERN.test(line)) {
      appendMarkdownExampleFence(output, exampleFence)
      output.push(line)
      exampleFence = null
      continue
    }

    if (exampleFence.nestedFence && FENCE_END_PATTERN.test(line)) {
      exampleFence.contentLines.push(line)
      exampleFence.nestedFence = false
      continue
    }

    if (
      line.startsWith(exampleFence.fenceChar.repeat(3)) &&
      !FENCE_END_PATTERN.test(line)
    ) {
      exampleFence.contentLines.push(line)
      exampleFence.nestedFence = true
      continue
    }

    if (FENCE_END_PATTERN.test(line)) {
      appendMarkdownExampleFence(output, exampleFence)
      exampleFence = null
      continue
    }

    exampleFence.contentLines.push(line)
  }

  if (exampleFence) {
    appendMarkdownExampleFence(output, exampleFence)
  }

  return output.join('\n')
}

export function stripCustomTags(input: unknown): string {
  if (typeof input !== 'string') {
    return String(input ?? '')
  }

  return input
    .replaceAll(
      /<\/?(conversation|conversationcontent|reasoning|reasoningcontent|reasoningtrigger|sources|sourcescontent|sourcestrigger|branch|branchmessages|branchnext|branchpage|branchprevious|branchselector|message|messagecontent)\b[^>]*>/gi,
      ''
    )
    .replaceAll(/<\/?think\b[^>]*>/gi, '')
}

export function getMarkdownContent(children: ReactNode): string {
  if (Array.isArray(children)) {
    return normalizeMarkdownExampleFences(stripCustomTags(children.join('')))
  }

  return normalizeMarkdownExampleFences(stripCustomTags(children))
}

export function getNodeKey(node: ParsedNode, index: number): string {
  const raw = typeof node.raw === 'string' ? node.raw : ''
  return `${node.type}-${index}-${raw.slice(0, 24)}`
}

export function parseResponseContent(
  nodes: ParsedNode[]
): ParsedResponseContent {
  const footnotes: ParsedResponseContent['footnotes'] = []
  const bodyNodes: ParsedNode[] = []

  for (const node of nodes) {
    if (isFootnoteNode(node)) {
      footnotes.push(node)
      continue
    }

    bodyNodes.push(node)
  }

  return { bodyNodes, footnotes }
}
