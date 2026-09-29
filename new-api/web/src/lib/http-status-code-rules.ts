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
export type StatusCodeRange = {
  start: number
  end: number
}

export type ParsedHttpStatusCodeRules = {
  ok: boolean
  ranges: StatusCodeRange[]
  tokens: string[]
  normalized: string
  invalidTokens: string[]
}

export function parseHttpStatusCodeRules(
  input: unknown
): ParsedHttpStatusCodeRules {
  const raw = (input ?? '').toString().trim()
  if (raw.length === 0) {
    return {
      ok: true,
      ranges: [],
      tokens: [],
      normalized: '',
      invalidTokens: [],
    }
  }

  const sanitized = raw.replace(/[，]/g, ',')
  const segments = sanitized
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const ranges: StatusCodeRange[] = []
  const invalidTokens: string[] = []

  for (const segment of segments) {
    const parsed = parseToken(segment)
    if (!parsed) {
      invalidTokens.push(segment)
    } else {
      ranges.push(parsed)
    }
  }

  if (invalidTokens.length > 0) {
    return {
      ok: false,
      ranges: [],
      tokens: [],
      normalized: raw,
      invalidTokens,
    }
  }

  const merged = mergeRanges(ranges)
  const tokens = merged.map((r) =>
    r.start === r.end ? `${r.start}` : `${r.start}-${r.end}`
  )
  const normalized = tokens.join(',')

  return {
    ok: true,
    ranges: merged,
    tokens,
    normalized,
    invalidTokens: [],
  }
}

function parseToken(token: string): StatusCodeRange | null {
  const cleaned = token.trim().replace(/\s/g, '')
  if (!cleaned) return null

  const isValidCode = (code: number) =>
    Number.isFinite(code) && code >= 100 && code <= 599

  if (cleaned.includes('-')) {
    const [a, b] = cleaned.split('-')
    if (!isNumber(a) || !isNumber(b)) return null

    const start = Number.parseInt(a, 10)
    const end = Number.parseInt(b, 10)
    if (!isValidCode(start) || !isValidCode(end) || start > end) return null

    return { start, end }
  }

  if (!isNumber(cleaned)) return null
  const code = Number.parseInt(cleaned, 10)
  if (!isValidCode(code)) return null

  return { start: code, end: code }
}

function isNumber(s: string) {
  return typeof s === 'string' && /^\d+$/.test(s)
}

function mergeRanges(ranges: StatusCodeRange[]): StatusCodeRange[] {
  if (ranges.length === 0) return []

  const sorted = [...ranges].sort((a, b) =>
    a.start !== b.start ? a.start - b.start : a.end - b.end
  )

  return sorted.reduce<StatusCodeRange[]>((merged, current) => {
    const last = merged[merged.length - 1]

    if (!last || current.start > last.end + 1) {
      merged.push({ ...current })
    } else {
      last.end = Math.max(last.end, current.end)
    }

    return merged
  }, [])
}
