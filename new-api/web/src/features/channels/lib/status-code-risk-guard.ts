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
const NON_REDIRECTABLE_STATUS_CODES = new Set([504, 524])

function parseStatusCodeKey(rawKey: string): number | null {
  const normalized = rawKey.trim()
  if (!/^[1-5]\d{2}$/.test(normalized)) return null
  return Number.parseInt(normalized, 10)
}

function parseStatusCodeMappingTarget(rawValue: unknown): number | null {
  if (typeof rawValue === 'number' && Number.isInteger(rawValue)) {
    return rawValue >= 100 && rawValue <= 599 ? rawValue : null
  }
  if (typeof rawValue === 'string') {
    const normalized = rawValue.trim()
    if (!/^[1-5]\d{2}$/.test(normalized)) return null
    const code = Number.parseInt(normalized, 10)
    return code >= 100 && code <= 599 ? code : null
  }
  return null
}

export function collectInvalidStatusCodeEntries(
  statusCodeMappingStr: string
): string[] {
  if (!statusCodeMappingStr?.trim()) return []

  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(statusCodeMappingStr)
  } catch {
    return []
  }

  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return []

  const invalid: string[] = []
  for (const [rawKey, rawValue] of Object.entries(parsed)) {
    const fromCode = parseStatusCodeKey(rawKey)
    const toCode = parseStatusCodeMappingTarget(rawValue)
    if (fromCode === null || toCode === null) {
      invalid.push(`${rawKey} → ${rawValue}`)
    }
  }
  return invalid
}

export function collectDisallowedStatusCodeRedirects(
  statusCodeMappingStr: string
): string[] {
  if (!statusCodeMappingStr?.trim()) return []

  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(statusCodeMappingStr)
  } catch {
    return []
  }

  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return []

  const riskyMappings: string[] = []
  for (const [rawFrom, rawTo] of Object.entries(parsed)) {
    const fromCode = parseStatusCodeKey(rawFrom)
    const toCode = parseStatusCodeMappingTarget(rawTo)
    if (fromCode === null || toCode === null) continue
    if (!NON_REDIRECTABLE_STATUS_CODES.has(fromCode)) continue
    if (fromCode === toCode) continue
    riskyMappings.push(`${fromCode} -> ${toCode}`)
  }

  return [...new Set(riskyMappings)].sort()
}

export function collectNewDisallowedStatusCodeRedirects(
  originalStr: string,
  currentStr: string
): string[] {
  const currentRisky = collectDisallowedStatusCodeRedirects(currentStr)
  if (currentRisky.length === 0) return []

  const originalRiskySet = new Set(
    collectDisallowedStatusCodeRedirects(originalStr)
  )
  return currentRisky.filter((mapping) => !originalRiskySet.has(mapping))
}
