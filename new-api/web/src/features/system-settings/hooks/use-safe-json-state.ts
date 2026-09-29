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
import { useMemo } from 'react'

import {
  safeJsonParse,
  safeJsonParseWithValidation,
  type SafeJsonParseOptions,
  type SafeJsonParseWithValidationOptions,
} from '../utils/json-parser'

export function useSafeJsonParse<T>(
  value: string | undefined | null,
  options: Required<Pick<SafeJsonParseOptions<T>, 'fallback' | 'context'>> &
    Omit<SafeJsonParseOptions<T>, 'fallback' | 'context'>
): T {
  return useMemo(
    () =>
      safeJsonParse(value, {
        fallback: options.fallback,
        context: options.context,
        silent: options.silent,
      }),
    [value, options.fallback, options.context, options.silent]
  )
}

export function useSafeJsonParseWithValidation<T>(
  value: string | undefined | null,
  options: SafeJsonParseWithValidationOptions<T>
): T {
  return useMemo(
    () => safeJsonParseWithValidation(value, options),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      value,
      options.fallback,
      options.validator,
      options.validatorMessage,
      options.context,
    ]
  )
}
