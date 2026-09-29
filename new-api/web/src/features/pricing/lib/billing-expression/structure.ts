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
/** Lossless structural helpers: preserve source text even outside the simulated grammar. */
export function splitExpressionAtTopLevel(
  source: string,
  operator: string
): string[] {
  const parts: string[] = []
  let start = 0
  let depth = 0
  let quote = ''
  for (let i = 0; i < source.length; i++) {
    const char = source[i]
    if (quote) {
      if (char === '\\') {
        i++
        continue
      }
      if (char === quote) quote = ''
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      continue
    }
    if (char === '(' || char === '[' || char === '{') depth++
    if (char === ')' || char === ']' || char === '}') depth--
    if (depth !== 0) continue
    if (char === '?') return [source.trim()]
    if (source.startsWith(operator, i)) {
      parts.push(source.slice(start, i).trim())
      start = i + operator.length
      i += operator.length - 1
    }
  }
  parts.push(source.slice(start).trim())
  return parts.filter(Boolean)
}

export function unwrapExpressionParens(source: string): string {
  let expression = source.trim()
  while (expression.startsWith('(') && expression.endsWith(')')) {
    let depth = 0
    let quote = ''
    let closesAtEnd = false
    for (let i = 0; i < expression.length; i++) {
      const char = expression[i]
      if (quote) {
        if (char === '\\') {
          i++
          continue
        }
        if (char === quote) quote = ''
        continue
      }
      if (char === '"' || char === "'") {
        quote = char
        continue
      }
      if (char === '(') depth++
      if (char === ')') depth--
      if (depth === 0) {
        closesAtEnd = i === expression.length - 1
        break
      }
    }
    if (!closesAtEnd) break
    expression = expression.slice(1, -1).trim()
  }
  return expression
}
