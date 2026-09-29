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
interface ParsedThinkTags {
  visibleContent: string
  reasoning: string
  hasUnclosedTag: boolean
}

/**
 * Parse content to separate thinking from visible text.
 * Handles both complete and incomplete <think> tags.
 */
export function parseThinkTags(content: string): ParsedThinkTags {
  if (!content.includes('<think>')) {
    return { visibleContent: content, reasoning: '', hasUnclosedTag: false }
  }

  const visibleParts: string[] = []
  const reasoningParts: string[] = []
  let currentPos = 0
  let hasUnclosedTag = false

  while (true) {
    const openPos = content.indexOf('<think>', currentPos)

    if (openPos === -1) {
      if (currentPos < content.length) {
        visibleParts.push(content.slice(currentPos))
      }
      break
    }

    if (openPos > currentPos) {
      visibleParts.push(content.slice(currentPos, openPos))
    }

    const closePos = content.indexOf('</think>', openPos + 7)

    if (closePos === -1) {
      reasoningParts.push(content.slice(openPos + 7))
      hasUnclosedTag = true
      break
    }

    reasoningParts.push(content.slice(openPos + 7, closePos))
    currentPos = closePos + 8
  }

  return {
    visibleContent: visibleParts.join('').trim(),
    reasoning: reasoningParts.join('\n\n').trim(),
    hasUnclosedTag,
  }
}
