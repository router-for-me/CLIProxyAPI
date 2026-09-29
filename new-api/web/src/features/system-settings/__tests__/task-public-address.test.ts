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
import assert from 'node:assert/strict'
import { describe, test } from 'vitest'

import { isValidTaskPublicAddress } from '../general/task-public-address'

describe('async task public address', () => {
  test('allows an empty fallback or an absolute HTTP(S) media base URL', () => {
    for (const value of [
      '',
      'https://media.example.com',
      'https://media.example.com/task-content',
      'http://127.0.0.1:8080/nginx/tasks',
      'http://localhost:3000/media',
    ]) {
      assert.equal(isValidTaskPublicAddress(value), true, value)
    }
  })

  test('rejects credentials, query parameters, fragments, and non-HTTP URLs', () => {
    for (const value of [
      'media.example.com/tasks',
      '/media/tasks',
      'ftp://media.example.com/tasks',
      'https://user:secret@media.example.com/tasks',
      'https://@media.example.com/tasks',
      'https://media.example.com/tasks?token=secret',
      'https://media.example.com/tasks#preview',
      ' https://media.example.com/tasks',
      'https://media.example.com/tasks\n',
      'https:\\\\media.example.com\\tasks',
      'https://',
    ]) {
      assert.equal(isValidTaskPublicAddress(value), false, value)
    }
  })
})
