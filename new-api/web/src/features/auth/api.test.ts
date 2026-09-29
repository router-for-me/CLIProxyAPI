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
import { act, renderHook } from '@testing-library/react'
import { toast } from 'sonner'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api, type RefreshOutcome } from '@/lib/api'
import type { AuthBundle } from '@/stores/auth-store'

import { executeLogout } from './api'
import { useOAuthLogin } from './hooks/use-oauth-login'
import { consumeOAuthLoginRedirect } from './lib/oauth-callback-mode'

afterEach(() => vi.restoreAllMocks())

test.each([true, false])(
  'starts Telegram OAuth only when configuration is ready: %s',
  async (configured) => {
    vi.spyOn(window, 'localStorage', 'get').mockReturnValue(
      window.sessionStorage
    )
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/oauth/state') {
        return {
          data: {
            success: true,
            data: {
              flow_token: 'telegram-state',
              authorization_url: 'https://oauth.telegram.org/auth?server=pkce',
            },
          },
        }
      }
      if (url === '/api/user/auth/logout') return { data: { success: true } }
      throw new Error(`Unexpected POST ${url}`)
    })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const error = vi.spyOn(toast, 'error')
    const { result } = renderHook(() =>
      useOAuthLogin(
        { telegram_oauth: true, telegram_oauth_configured: configured },
        '/console/personal'
      )
    )
    await act(() => result.current.handleTelegramLogin())
    if (configured) {
      expect(post.mock.calls.map(([url]) => url)).toEqual([
        '/api/oauth/state',
        '/api/user/auth/logout',
      ])
      expect(post).toHaveBeenCalledWith(
        '/api/oauth/state',
        expect.objectContaining({ provider: 'telegram', intent: 'login' }),
        expect.anything()
      )
      expect(open).toHaveBeenCalledWith(
        'https://oauth.telegram.org/auth?server=pkce',
        '_self'
      )
      expect(consumeOAuthLoginRedirect('telegram-state')).toBe(
        '/console/personal'
      )
    } else {
      expect(post).not.toHaveBeenCalled()
      expect(open).not.toHaveBeenCalled()
      expect(error).toHaveBeenCalledWith(
        'Telegram OAuth is not configured or enabled. Please contact your administrator.'
      )
    }
  }
)

const bundle: AuthBundle = {
  access_token: 'access-token',
  token_type: 'Bearer',
  access_expires_at: 1_900_000_000,
  user: { id: 1, username: 'test-user', role: 1 },
  session: {
    sid: 'session-b',
    current: true,
    login_method: 'password',
    ip: '127.0.0.1',
    user_agent: 'test',
    created_at: 1,
    last_active_at: 1,
    expires_at: 1_900_000_000,
  },
}

function mismatchError() {
  return {
    isAxiosError: true,
    response: {
      status: 409,
      data: { code: 'AUTH_SESSION_MISMATCH' },
    },
  }
}

describe('logout coordination', () => {
  test('returns an unsuccessful response without pretending to sign out', async () => {
    let refreshCount = 0
    const result = await executeLogout({
      getExpectedSID: () => 'session-a',
      request: async () => ({ success: false, message: 'not revoked' }),
      refresh: async () => {
        refreshCount += 1
        return { kind: 'anonymous' }
      },
    })

    expect(result).toEqual({ success: false, message: 'not revoked' })
    expect(refreshCount).toBe(0)
  })

  test('recovers a cookie mismatch and retries with the refreshed SID', async () => {
    let sid = 'session-a'
    const requestedSIDs: Array<string | undefined> = []
    const result = await executeLogout({
      getExpectedSID: () => sid,
      request: async (expectedSID) => {
        requestedSIDs.push(expectedSID)
        if (requestedSIDs.length === 1) throw mismatchError()
        return { success: true, message: '' }
      },
      refresh: async () => {
        sid = bundle.session.sid
        return { kind: 'authenticated', bundle }
      },
    })

    expect(result).toEqual({ success: true, message: '' })
    expect(requestedSIDs).toEqual(['session-a', 'session-b'])
  })

  test('treats a mismatch that refresh confirms anonymous as signed out', async () => {
    const result = await executeLogout({
      getExpectedSID: () => 'session-a',
      request: async () => {
        throw mismatchError()
      },
      refresh: async () => ({ kind: 'anonymous' }),
    })

    expect(result).toEqual({ success: true, message: '' })
  })

  test('preserves the active session when mismatch recovery is temporary', async () => {
    const originalError = mismatchError()
    const transient: RefreshOutcome = {
      kind: 'transient_error',
      error: new Error('offline'),
    }

    await expect(
      executeLogout({
        getExpectedSID: () => 'session-a',
        request: async () => {
          throw originalError
        },
        refresh: async () => transient,
      })
    ).rejects.toBe(originalError)
  })
})
