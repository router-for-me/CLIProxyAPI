import type { TFunction } from 'i18next'
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

import { loginMethodLabel, sessionDevice } from '../login-session-utils'

const translate = ((key: string) => key) as TFunction

describe('login session presentation', () => {
  test('labels built-in and provider OAuth login methods', () => {
    expect(loginMethodLabel('password', translate)).toBe('Password')
    expect(loginMethodLabel('2fa', translate)).toBe('Two-factor Authentication')
    expect(loginMethodLabel('oauth:github', translate)).toBe('OAuth · GitHub')
    expect(loginMethodLabel('oauth:custom-provider', translate)).toBe(
      'OAuth · custom-provider'
    )
  })

  test('labels iPad Safari as iOS when its user agent also mentions Mac OS X', () => {
    const userAgent =
      'Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1'

    expect(sessionDevice(userAgent, 'Unknown device', 'Browser')).toBe(
      'Safari · iOS'
    )
  })

  test('labels a touch-capable current iPad session as iOS when its desktop user agent says Macintosh', () => {
    const userAgent =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15'

    expect(sessionDevice(userAgent, 'Unknown device', 'Browser', 5)).toBe(
      'Safari · iOS'
    )
  })

  test('keeps touch-capable Windows Chrome sessions identifiable', () => {
    const userAgent =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36'

    expect(sessionDevice(userAgent, 'Unknown device', 'Browser', 10)).toBe(
      'Chrome · Windows'
    )
  })

  test('keeps Android Chrome sessions identifiable when their user agent mentions Linux', () => {
    const userAgent =
      'Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36'

    expect(sessionDevice(userAgent, 'Unknown device', 'Browser', 5)).toBe(
      'Chrome · Android'
    )
  })

  test('keeps genuine macOS Safari sessions identifiable', () => {
    const userAgent =
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15'

    expect(sessionDevice(userAgent, 'Unknown device', 'Browser')).toBe(
      'Safari · macOS'
    )
  })

  test('falls back to the unknown-device label for an empty user agent', () => {
    expect(sessionDevice('', 'Unknown device', 'Browser')).toBe(
      'Unknown device'
    )
  })
})
