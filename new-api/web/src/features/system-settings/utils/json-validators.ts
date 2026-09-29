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
export const isObjectRecord = (
  data: unknown
): data is Record<string, unknown> =>
  typeof data === 'object' && data !== null && !Array.isArray(data)

export const isArray = (data: unknown): data is unknown[] => Array.isArray(data)

export const isStringArray = (data: unknown): data is string[] =>
  Array.isArray(data) && data.every((item) => typeof item === 'string')

export const isNumberArray = (data: unknown): data is number[] =>
  Array.isArray(data) && data.every((item) => typeof item === 'number')

export const isObjectArray = (
  data: unknown
): data is Record<string, unknown>[] =>
  Array.isArray(data) && data.every((item) => isObjectRecord(item))

export function createObjectValidator<T extends Record<string, unknown>>(
  requiredKeys: (keyof T)[]
): (data: unknown) => data is T {
  return (data): data is T => {
    if (!isObjectRecord(data)) return false
    return requiredKeys.every((key) => key in data)
  }
}

export function createArrayValidator<T>(
  itemValidator: (item: unknown) => item is T
): (data: unknown) => data is T[] {
  return (data): data is T[] => {
    if (!Array.isArray(data)) return false
    return data.every(itemValidator)
  }
}
