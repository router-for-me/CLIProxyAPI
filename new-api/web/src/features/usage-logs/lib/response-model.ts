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
import type { LogOtherData } from '../types'

export type ResponseModelObservation = NonNullable<
  LogOtherData['response_model']
>

/**
 * Decide whether an upstream response model deserves a mismatch warning.
 *
 * Mirrors relay/common/response_model.go: ignoring case, the returned name is
 * compatible when it starts with the requested or upstream model (dated
 * versions, variants) or ends with it (provider paths such as
 * "deepseek/deepseek-v4.1-flash"). Nothing is stored; every row is judged
 * with the current rule.
 */
export function isResponseModelMismatch(
  observation: ResponseModelObservation | undefined
): boolean {
  if (!observation) return false
  const returned = (observation.returned_model ?? '').toLowerCase()
  if (returned.trim() === '') return false
  for (const candidate of [
    observation.requested_model,
    observation.upstream_model,
  ]) {
    const expected = (candidate ?? '').toLowerCase()
    if (expected === '') continue
    if (returned.startsWith(expected) || returned.endsWith(expected)) {
      return false
    }
  }
  return true
}
