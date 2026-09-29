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
export type TelegramAuthorization = {
  id: string | number
  auth_date: string | number
  hash: string
  first_name?: string
  last_name?: string
  username?: string
  photo_url?: string
  lang?: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}

function readTelegramNumber(value: unknown): string | number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim()) return value
  return null
}

export function pickTelegramAuthorization(
  value: unknown
): TelegramAuthorization | null {
  if (!isRecord(value)) return null

  const id = readTelegramNumber(value.id)
  const authDate = readTelegramNumber(value.auth_date)
  const hash = typeof value.hash === 'string' ? value.hash.trim() : ''
  if (id === null || authDate === null || !hash) return null

  const authorization: TelegramAuthorization = {
    id,
    auth_date: authDate,
    hash,
  }
  const optionalFields = [
    'first_name',
    'last_name',
    'username',
    'photo_url',
    'lang',
  ] as const

  for (const field of optionalFields) {
    const fieldValue = value[field]
    if (typeof fieldValue === 'string' && fieldValue) {
      authorization[field] = fieldValue
    }
  }

  return authorization
}
