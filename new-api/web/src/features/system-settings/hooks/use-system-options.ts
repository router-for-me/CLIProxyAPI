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
import { useQuery } from '@tanstack/react-query'

import { requireServerSuccess } from '@/lib/server-error-message'

import { getSystemOptions } from '../api'

export function useSystemOptions() {
  return useQuery({
    queryKey: ['system-options'],
    queryFn: async () => requireServerSuccess(await getSystemOptions()),
    staleTime: 5 * 60 * 1000,
  })
}

type ParseResult<T> =
  | { success: true; value: T }
  | { success: false; error: string; fallback: T }

function parseOptionValueSafe<T>(
  value: string,
  defaultValue: T
): ParseResult<T> {
  if (typeof defaultValue === 'boolean') {
    return {
      success: true,
      value: (value === 'true' || value === '1') as T,
    }
  }

  if (typeof defaultValue === 'number') {
    const trimmed = value.trim()
    if (trimmed === '') {
      return {
        success: false,
        error: 'Empty string for number field',
        fallback: defaultValue,
      }
    }
    const parsed = Number(trimmed)
    if (Number.isNaN(parsed)) {
      return {
        success: false,
        error: `Invalid number: "${value}"`,
        fallback: defaultValue,
      }
    }
    return { success: true, value: parsed as T }
  }

  if (Array.isArray(defaultValue)) {
    try {
      const parsed = JSON.parse(value)
      if (!Array.isArray(parsed)) {
        return {
          success: false,
          error: 'Expected array but got non-array JSON',
          fallback: defaultValue,
        }
      }

      if (defaultValue.length > 0) {
        const expectedType = typeof defaultValue[0]
        const invalidElement = parsed.find((el) => typeof el !== expectedType)
        if (invalidElement !== undefined) {
          return {
            success: false,
            error: `Array element type mismatch: expected ${expectedType}, got ${typeof invalidElement}`,
            fallback: defaultValue,
          }
        }
      }

      return { success: true, value: parsed as T }
    } catch (e) {
      return {
        success: false,
        error: `JSON parse failed: ${e instanceof Error ? e.message : String(e)}`,
        fallback: defaultValue,
      }
    }
  }

  return { success: true, value: value as T }
}

export function getOptionValue<
  T extends Record<string, string | number | boolean | unknown[]>,
>(options: Array<{ key: string; value: string }> | undefined, defaults: T): T {
  if (!options) return defaults

  const result = { ...defaults }
  const errors: Array<{ key: string; error: string }> = []

  options.forEach((option) => {
    if (option.key in defaults) {
      const parseResult = parseOptionValueSafe(
        option.value,
        defaults[option.key as keyof T]
      )

      if (parseResult.success) {
        result[option.key as keyof T] = parseResult.value as T[keyof T]
      } else {
        result[option.key as keyof T] = parseResult.fallback as T[keyof T]
        errors.push({ key: option.key, error: parseResult.error })
      }
    }
  })

  if (errors.length > 0 && import.meta.env.DEV) {
    // eslint-disable-next-line no-console
    console.warn('[System Options] Parsing errors detected:', errors)
  }

  return result
}
