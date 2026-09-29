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
export function formatJsonForTextarea(value: string) {
  if (!value || !value.trim()) {
    return ''
  }

  try {
    const parsed = JSON.parse(value)
    return JSON.stringify(parsed, null, 2)
  } catch {
    return value
  }
}

export function normalizeJsonString(value: string) {
  const trimmed = value.trim()
  if (!trimmed) {
    return ''
  }

  try {
    const parsed = JSON.parse(trimmed)
    return JSON.stringify(parsed)
  } catch {
    return trimmed
  }
}

type JsonValidationOptions = {
  allowEmpty?: boolean
  predicate?: (value: unknown) => boolean
  predicateMessage?: string
}

export type JsonValidationError = {
  type: 'required' | 'structure' | 'syntax'
  line?: number
  column?: number
  position?: number
  missingCommaLine?: number
}

function extractErrorPosition(
  error: unknown,
  jsonString: string
): { line?: number; column?: number; position?: number } {
  if (!(error instanceof Error)) return {}

  const message = error.message

  // Format 1: "Unexpected token } in JSON at position 15"
  const positionMatch = message.match(/at position (\d+)/i)
  if (positionMatch) {
    const position = parseInt(positionMatch[1], 10)
    const lines = jsonString.substring(0, position).split('\n')
    return {
      line: lines.length,
      column: lines[lines.length - 1].length + 1,
      position,
    }
  }

  // Format 2: "JSON.parse: ... at line 2 column 3"
  const lineColMatch = message.match(/at line (\d+) column (\d+)/i)
  if (lineColMatch) {
    return {
      line: parseInt(lineColMatch[1], 10),
      column: parseInt(lineColMatch[2], 10),
    }
  }

  return {}
}

function buildSyntaxError(
  error: unknown,
  jsonString: string
): JsonValidationError {
  if (!(error instanceof Error)) {
    return {
      type: 'syntax',
    } satisfies JsonValidationError
  }

  const position = extractErrorPosition(error, jsonString)
  const message = error.message

  // Check if it's a "missing comma" type error
  const isMissingCommaError =
    message.includes("Expected ','") ||
    message.includes('Expected property name') ||
    message.includes('Unexpected string')

  const missingCommaLine =
    isMissingCommaError && position.line && position.line > 1
      ? position.line - 1
      : undefined

  return {
    type: 'syntax',
    ...position,
    missingCommaLine,
  } satisfies JsonValidationError
}

function formatErrorMessage(error: unknown, jsonString: string): string {
  if (!(error instanceof Error)) return 'Invalid JSON'

  const position = extractErrorPosition(error, jsonString)
  const message = error.message
  const syntaxError = buildSyntaxError(error, jsonString)

  if (position.line && position.column) {
    let hint = ''
    if (syntaxError.missingCommaLine) {
      hint = ` (check line ${syntaxError.missingCommaLine} for missing comma)`
    }
    return `Error at line ${position.line}, column ${position.column}: ${message}${hint}`
  }

  if (position.position !== undefined) {
    return `Error at position ${position.position}: ${message}`
  }

  return message
}

export function validateJsonString(
  value: string,
  options: JsonValidationOptions = {}
) {
  const { allowEmpty = true, predicate, predicateMessage } = options
  const trimmed = value.trim()

  if (!trimmed) {
    return {
      valid: allowEmpty,
      message: allowEmpty ? undefined : 'Value is required',
      error: allowEmpty
        ? undefined
        : ({
            type: 'required',
          } satisfies JsonValidationError),
    }
  }

  try {
    const parsed = JSON.parse(trimmed)
    if (predicate && !predicate(parsed)) {
      return {
        valid: false,
        message: predicateMessage || 'JSON structure is invalid',
        error: {
          type: 'structure',
        } satisfies JsonValidationError,
      }
    }

    return { valid: true }
  } catch (error: unknown) {
    return {
      valid: false,
      message: formatErrorMessage(error, trimmed),
      error: buildSyntaxError(error, trimmed),
    }
  }
}
