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
import { describe, expect, test } from 'vitest'

import { pickTelegramAuthorization } from './telegram-login'

describe('Telegram login authorization', () => {
  test('keeps only fields signed by the Telegram login contract', () => {
    expect(
      pickTelegramAuthorization({
        id: 12345,
        first_name: 'Test',
        last_name: 'User',
        username: 'test_user',
        photo_url: 'https://t.me/i/userpic/320/test.jpg',
        auth_date: 1_900_000_000,
        hash: 'signed-hash',
        lang: 'en',
        admin: true,
        redirect: 'https://attacker.example',
      })
    ).toEqual({
      id: 12345,
      first_name: 'Test',
      last_name: 'User',
      username: 'test_user',
      photo_url: 'https://t.me/i/userpic/320/test.jpg',
      auth_date: 1_900_000_000,
      hash: 'signed-hash',
      lang: 'en',
    })
  })

  test('rejects incomplete or structurally invalid callbacks', () => {
    expect(pickTelegramAuthorization(null)).toBe(null)
    expect(pickTelegramAuthorization({ auth_date: 1, hash: 'hash' })).toBe(null)
    expect(pickTelegramAuthorization({ id: 1, auth_date: 1, hash: '' })).toBe(
      null
    )
    expect(
      pickTelegramAuthorization({ id: {}, auth_date: 1, hash: 'hash' })
    ).toBe(null)
  })
})
