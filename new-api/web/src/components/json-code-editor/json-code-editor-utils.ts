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
import type { Plugin, TextareaProps } from 'yace'

export type JsonValidationState = {
  isValid: boolean
  messageKey: 'JSON' | 'Invalid JSON'
}

export type JsonFormatResult = {
  didFormat: boolean
  value: string
}

export type CursorLocation = {
  line: number
  column: number
}

type ScrollSource = {
  scrollLeft: number
  scrollTop: number
}

type TransformLayer = {
  style: Pick<CSSStyleDeclaration, 'transform'>
}

type ScrollLayers = {
  contentLayer: TransformLayer
  lineNumberLayer: TransformLayer
}

type RequestFrame = (callback: () => void) => number

export type ScrollLayerSynchronizer = {
  sync: () => void
}

export function getJsonValidationState(value: string): JsonValidationState {
  const trimmed = value.trim()
  if (!trimmed) {
    return { isValid: true, messageKey: 'JSON' }
  }

  try {
    JSON.parse(trimmed)
    return { isValid: true, messageKey: 'JSON' }
  } catch {
    return { isValid: false, messageKey: 'Invalid JSON' }
  }
}

export function formatJsonDraft(value: string): JsonFormatResult {
  const trimmed = value.trim()
  if (!trimmed) {
    return { didFormat: false, value }
  }

  try {
    return {
      didFormat: true,
      value: JSON.stringify(JSON.parse(trimmed), null, 2),
    }
  } catch {
    return { didFormat: false, value }
  }
}

export function getCursorLocation(
  value: string,
  selectionStart: number
): CursorLocation {
  const boundedSelectionStart = Math.min(
    Math.max(selectionStart, 0),
    value.length
  )
  const linesBeforeCursor = value.slice(0, boundedSelectionStart).split('\n')
  const currentLine = linesBeforeCursor.at(-1) ?? ''

  return {
    line: linesBeforeCursor.length,
    column: currentLine.length + 1,
  }
}

export function createScrollLayerSynchronizer(
  source: ScrollSource,
  layers: ScrollLayers,
  requestFrame: RequestFrame = window.requestAnimationFrame
): ScrollLayerSynchronizer {
  let hasPendingFrame = false

  return {
    sync: () => {
      if (hasPendingFrame) {
        return
      }

      hasPendingFrame = true
      requestFrame(() => {
        hasPendingFrame = false
        layers.contentLayer.style.transform = `translate3d(-${source.scrollLeft}px, -${source.scrollTop}px, 0)`
        layers.lineNumberLayer.style.transform = `translate3d(0, -${source.scrollTop}px, 0)`
      })
    },
  }
}

export function applyJsonSmartEnter(
  value: string,
  selectionStart: number,
  selectionEnd: number
): TextareaProps | undefined {
  const before = value.slice(0, selectionStart)
  const after = value.slice(selectionEnd)
  const indent = getLineIndent(value, selectionStart)
  const previousChar = before.trimEnd().at(-1)
  const nextChar = after.trimStart().at(0)
  const shouldNest = previousChar === '{' || previousChar === '['
  const shouldClose =
    (previousChar === '{' && nextChar === '}') ||
    (previousChar === '[' && nextChar === ']')

  if (shouldNest && shouldClose) {
    const innerIndent = `${indent}  `
    const insert = `\n${innerIndent}\n${indent}`
    const nextSelection = selectionStart + 1 + innerIndent.length

    return {
      value: `${before}${insert}${after}`,
      selectionStart: nextSelection,
      selectionEnd: nextSelection,
    }
  }

  if (!indent && !shouldNest) {
    return undefined
  }

  const nextIndent = shouldNest ? `${indent}  ` : indent
  const insert = `\n${nextIndent}`
  const nextSelection = selectionStart + insert.length

  return {
    value: `${before}${insert}${after}`,
    selectionStart: nextSelection,
    selectionEnd: nextSelection,
  }
}

export function jsonSmartEnter(): Plugin {
  return (props, event) => {
    if (event.type !== 'keydown') {
      return undefined
    }

    const keyboardEvent = event as KeyboardEvent
    if (keyboardEvent.key !== 'Enter') {
      return undefined
    }

    const nextProps = applyJsonSmartEnter(
      props.value,
      props.selectionStart,
      props.selectionEnd
    )

    if (!nextProps) {
      return undefined
    }

    event.preventDefault()
    return nextProps
  }
}

function getLineIndent(value: string, cursor: number): string {
  const lineStart = value.lastIndexOf('\n', cursor - 1) + 1

  return value.slice(lineStart, cursor).match(/^\s*/)?.[0] ?? ''
}
