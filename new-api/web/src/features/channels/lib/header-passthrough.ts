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

/**
 * The header override rule key the relay treats as "forward every client
 * request header" (see processHeaderOverride in relay/channel/api_request.go).
 * The quick "Pass Through Request Headers" switch is a shortcut for this rule.
 */
export const HEADER_PASSTHROUGH_ALL_KEY = '*'

export type HeaderPassthroughState = 'enabled' | 'disabled' | 'invalid'

function parseHeaderOverrideObject(
  headerOverride: string | undefined
): Record<string, unknown> | null {
  const trimmed = headerOverride?.trim()
  if (!trimmed) return {}
  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // Fall through: invalid JSON cannot be edited structurally.
  }
  return null
}

function isPassthroughAllKey(key: string): boolean {
  return key.trim() === HEADER_PASSTHROUGH_ALL_KEY
}

/**
 * Reports whether the header override JSON contains the wildcard passthrough
 * rule. `invalid` means the text is not a JSON object and cannot be toggled.
 */
export function getHeaderPassthroughState(
  headerOverride: string | undefined
): HeaderPassthroughState {
  const parsed = parseHeaderOverrideObject(headerOverride)
  if (parsed === null) return 'invalid'
  return Object.keys(parsed).some(isPassthroughAllKey) ? 'enabled' : 'disabled'
}

/**
 * Adds or removes the wildcard passthrough rule while preserving every other
 * header override entry. Invalid JSON is returned unchanged; removing the last
 * entry yields an empty string so the field reads as unconfigured.
 */
export function setHeaderPassthrough(
  headerOverride: string | undefined,
  enabled: boolean
): string {
  const parsed = parseHeaderOverrideObject(headerOverride)
  if (parsed === null) return headerOverride ?? ''
  const rest = Object.fromEntries(
    Object.entries(parsed).filter(([key]) => !isPassthroughAllKey(key))
  )
  if (!enabled) {
    return Object.keys(rest).length === 0 ? '' : JSON.stringify(rest, null, 2)
  }
  return JSON.stringify(
    { [HEADER_PASSTHROUGH_ALL_KEY]: true, ...rest },
    null,
    2
  )
}
