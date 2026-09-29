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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { OAUTH_POPUP_CALLBACK_MESSAGE } from '@/features/auth/constants'
import type { UserProfile } from '@/features/profile/types'
import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { AccountBindings } from '../components/account-bindings'
import { PasskeyCard } from '../components/passkey-card'
import { TwoFACard } from '../components/two-fa-card'

const credentialsDescriptor = Object.getOwnPropertyDescriptor(
  navigator,
  'credentials'
)
const expiresAt = () => Math.floor(Date.now() / 1000) + 300
const credential = {
  id: 'credential',
  rawId: new Uint8Array([1, 2, 3]).buffer,
  type: 'public-key',
  response: {
    clientDataJSON: new Uint8Array([1]).buffer,
    authenticatorData: new Uint8Array([2]).buffer,
    signature: new Uint8Array([3]).buffer,
    attestationObject: new Uint8Array([4]).buffer,
    userHandle: null,
  },
  getClientExtensionResults: () => ({}),
}

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.stubGlobal(
    'PublicKeyCredential',
    class {
      static isUserVerifyingPlatformAuthenticatorAvailable() {
        return Promise.resolve(true)
      }
    }
  )
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: {
      get: vi.fn().mockResolvedValue(credential),
      create: vi.fn().mockResolvedValue(credential),
    },
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  if (credentialsDescriptor) {
    Object.defineProperty(navigator, 'credentials', credentialsDescriptor)
  } else Reflect.deleteProperty(navigator, 'credentials')
})

it.each(['2fa', 'passkey'] as const)(
  'blocks %s enrollment and explains missing Telegram configuration',
  async (factor) => {
    const reason =
      'Telegram OAuth is not configured or enabled. Please contact your administrator.'
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/verify/methods'
            ? {
                scope: factor === '2fa' ? '2fa.setup' : 'passkey.register',
                methods: [{ method: 'oauth', available: false, reason }],
                oauth_providers: [],
                password_encryption_enabled: false,
              }
            : { enabled: false, locked: false },
      },
    }))
    const post = vi.spyOn(api, 'post')
    const user = userEvent.setup()
    render(
      factor === '2fa' ? (
        <TwoFACard loading={false} />
      ) : (
        <PasskeyCard loading={false} />
      )
    )
    const enable = await screen.findByRole('button', {
      name: factor === '2fa' ? 'Enable' : 'Enable Passkey',
    })
    await waitFor(() => expect(enable).toBeEnabled())
    await user.click(enable)
    expect(await screen.findByText(reason)).toBeVisible()
    expect(post).not.toHaveBeenCalled()
    expect(navigator.credentials.create).not.toHaveBeenCalled()
  }
)

it('refreshes Telegram bindings from the server result after the callback popup has closed', async () => {
  const popup = {
    closed: false,
    location: { replace: vi.fn() },
    sessionStorage: window.sessionStorage,
    close: vi.fn(),
    postMessage: vi.fn(),
  }
  vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window)
  vi.spyOn(api, 'post').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/verify'
          ? {
              proof_token: 'binding-proof',
              method: 'password',
              scope: 'account.binding.bind',
              expires_at: expiresAt(),
            }
          : {
              flow_token: 'binding-state',
              authorization_url: 'https://oauth.telegram.org/auth?server=pkce',
            },
    },
  }))
  let resolve!: (response: {
    data: { success: boolean; data: { action: string } }
  }) => void
  const response = new Promise<{
    data: { success: boolean; data: { action: string } }
  }>((done) => {
    resolve = done
  })
  const get = vi.spyOn(api, 'get').mockImplementation((url) => {
    if (url === '/api/verify/methods') {
      return Promise.resolve({
        data: {
          success: true,
          data: {
            scope: 'account.binding.bind',
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      })
    }
    return url === '/api/status'
      ? Promise.resolve({
          data: {
            success: true,
            data: { telegram_oauth: true, telegram_oauth_configured: true },
          },
        })
      : response
  })
  const onUpdate = vi.fn()
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <AccountBindings
        profile={{ email: 'bound@example.com' } as UserProfile}
        onUpdate={onUpdate}
      />
    </QueryClientProvider>
  )
  const telegram = (await screen.findByText('Telegram')).closest('li')
  if (!telegram) throw new Error('Telegram binding entry is missing')
  await user.click(within(telegram).getByRole('button', { name: 'Bind' }))
  const verification = await screen.findByRole('dialog', {
    name: 'Security verification',
  })
  await user.type(
    within(verification).getByLabelText('Password', { selector: 'input' }),
    'current-password'
  )
  await user.click(within(verification).getByRole('button', { name: 'Verify' }))
  const continuation = await screen.findByRole('alertdialog', {
    name: 'Continue account binding',
  })
  await user.click(
    within(continuation).getByRole('button', { name: 'Continue' })
  )
  await waitFor(() =>
    expect(popup.location.replace).toHaveBeenCalledWith(
      'https://oauth.telegram.org/auth?server=pkce'
    )
  )
  const message = new MessageEvent('message', {
    origin: window.location.origin,
    data: {
      type: OAUTH_POPUP_CALLBACK_MESSAGE,
      provider: 'telegram',
      intent: 'bind',
      state: 'binding-state',
      code: 'code',
    },
  })
  Object.defineProperty(message, 'source', { value: popup })
  act(() => {
    window.dispatchEvent(message)
    popup.closed = true
  })
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      '/api/oauth/telegram',
      expect.objectContaining({
        params: expect.objectContaining({
          state: 'binding-state',
          code: 'code',
        }),
      })
    )
  )
  expect(onUpdate).not.toHaveBeenCalled()
  expect(
    get.mock.calls.find(([url]) => url === '/api/oauth/telegram')?.[1]?.signal
      ?.aborted
  ).toBe(false)
  await act(async () => {
    resolve({ data: { success: true, data: { action: 'bind' } } })
    await response
  })
  await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(1))
})

it('shows a retry when the 2FA status query fails instead of offering enrollment', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockRejectedValue(new Error('Status unavailable'))
  const user = userEvent.setup()
  render(<TwoFACard loading={false} />)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Status unavailable'
  )
  expect(
    screen.queryByRole('button', { name: 'Enable' })
  ).not.toBeInTheDocument()
  get.mockResolvedValue({
    data: {
      success: true,
      data: { enabled: false, locked: false, backup_codes_remaining: 0 },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('button', { name: 'Enable' })).toBeEnabled()
})

it('consumes Passkey authorization at setup and activates using only the dedicated flow', async () => {
  let enabled = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/2fa/status') {
      return {
        data: {
          success: true,
          data: { enabled, locked: false, backup_codes_remaining: 4 },
        },
      }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: '2fa.setup',
            methods: [{ method: 'passkey', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  const posts = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/user/passkey/verify/begin') {
      return {
        data: {
          success: true,
          data: {
            flow_token: 'passkey-flow',
            options: { publicKey: { challenge: 'AQID', allowCredentials: [] } },
          },
        },
      }
    }
    if (url === '/api/user/passkey/verify/finish') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'passkey-proof',
            scope: '2fa.setup',
            method: 'passkey',
            expires_at: expiresAt(),
          },
        },
      }
    }
    if (url === '/api/user/2fa/setup') {
      return {
        data: {
          success: true,
          data: {
            secret: 'NEW_SECRET',
            qr_code_data: 'otpauth://totp/account?secret=NEW_SECRET',
            backup_codes: ['ABCD-1234'],
            flow_token: 'setup-flow',
            expires_at: expiresAt(),
          },
        },
      }
    }
    if (url === '/api/user/2fa/enable') {
      enabled = true
      return {
        data: { success: true, data: { access_token: 'rotated-session' } },
      }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
  const success = vi.spyOn(toast, 'success')
  const user = userEvent.setup()
  client.setQueryData(STATUS_QUERY_KEY, { passkey_rp_ids: ['localhost'] })
  render(
    <QueryClientProvider client={client}>
      <TwoFACard loading={false} />
    </QueryClientProvider>
  )
  await user.click(await screen.findByRole('button', { name: 'Enable' }))
  await screen.findByText(
    'We will prompt your device to confirm using biometrics or your hardware key.'
  )
  expect(posts.mock.calls.some(([url]) => url === '/api/user/2fa/setup')).toBe(
    false
  )
  expect(screen.queryByText('NEW_SECRET')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(await screen.findByText('NEW_SECRET')).toBeVisible()
  expect(posts).toHaveBeenCalledWith(
    '/api/user/2fa/setup',
    undefined,
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'passkey-proof' },
    })
  )
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await user.type(screen.getByLabelText('Verification Code'), '123456')
  await user.click(screen.getByRole('button', { name: 'Enable 2FA' }))
  await waitFor(() =>
    expect(success).toHaveBeenCalledExactlyOnceWith(
      'Two-factor authentication enabled successfully!'
    )
  )
  expect(posts).toHaveBeenCalledWith(
    '/api/user/2fa/enable',
    { code: '123456', flow_token: 'setup-flow' },
    expect.objectContaining({
      acceptAuthRotation: true,
    })
  )
  expect(
    posts.mock.calls.find(([url]) => url === '/api/user/2fa/enable')?.[2]
      ?.headers
  ).toBeUndefined()
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})

it('reports registration failure without treating a successful password check as a successful binding', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/passkey') {
      return { data: { success: true, data: { enabled: false } } }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'passkey.register',
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  const posts = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'password-proof',
            scope: 'passkey.register',
            method: 'password',
            expires_at: expiresAt(),
          },
        },
      }
    }
    if (url === '/api/user/passkey/register/begin') {
      return {
        data: {
          success: true,
          data: {
            flow_token: 'registration-flow',
            options: {
              publicKey: {
                challenge: 'AQID',
                rp: { name: 'Test', id: 'localhost' },
                user: { id: 'AQID', name: 'user', displayName: 'User' },
                pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
              },
            },
          },
        },
      }
    }
    if (url === '/api/user/passkey/register/finish') {
      return { data: { success: false, message: 'Registration rejected' } }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
  const success = vi.spyOn(toast, 'success')
  const failure = vi.spyOn(toast, 'error')
  const user = userEvent.setup()
  render(<PasskeyCard loading={false} />)
  const enable = await screen.findByRole('button', { name: 'Enable Passkey' })
  await waitFor(() => expect(enable).toBeEnabled())
  await user.click(enable)
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() =>
    expect(failure).toHaveBeenCalledWith('Registration rejected')
  )
  expect(success).not.toHaveBeenCalled()
  expect(posts).toHaveBeenCalledWith(
    '/api/user/passkey/register/begin',
    undefined,
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'password-proof' },
    })
  )
  expect(posts).toHaveBeenCalledWith(
    '/api/user/passkey/register/finish',
    expect.objectContaining({ flow_token: 'registration-flow' }),
    expect.objectContaining({
      acceptAuthRotation: true,
    })
  )
  expect(
    posts.mock.calls.find(
      ([url]) => url === '/api/user/passkey/register/finish'
    )?.[2]?.headers
  ).toBeUndefined()
})

it.each(['proof', 'setup'] as const)(
  'handles an expired %s without reusing a proof or replacing an active setup',
  async (expired) => {
    const issuedAt = Date.now()
    const clock = vi.spyOn(Date, 'now').mockReturnValue(issuedAt)
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/user/2fa/status') {
        return {
          data: { success: true, data: { enabled: false, locked: false } },
        }
      }
      if (url === '/api/verify/methods') {
        return {
          data: {
            success: true,
            data: {
              scope: '2fa.setup',
              methods: [{ method: 'password', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            },
          },
        }
      }
      throw new Error(`Unexpected GET ${url}`)
    })
    let proofCount = 0
    let setupCount = 0
    const posts = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/verify') {
        proofCount += 1
        return {
          data: {
            success: true,
            data: {
              proof_token: `proof-${proofCount}`,
              scope: '2fa.setup',
              method: 'password',
              expires_at: Math.floor(Date.now() / 1000) + 60,
            },
          },
        }
      }
      if (url === '/api/user/2fa/setup') {
        setupCount += 1
        return {
          data: {
            success: true,
            data: {
              secret: setupCount === 1 ? 'OLD_SECRET' : 'NEW_SECRET',
              qr_code_data: 'otpauth://totp/account?secret=SECRET',
              backup_codes: [setupCount === 1 ? 'OLD-CODE' : 'NEW-CODE'],
              flow_token: `setup-${setupCount}`,
              expires_at: expiresAt(),
            },
          },
        }
      }
      if (url === '/api/user/2fa/enable') {
        return { data: { success: true, data: {} } }
      }
      throw new Error(`Unexpected POST ${url}`)
    })
    const user = userEvent.setup()
    const info = vi.spyOn(toast, 'info')
    const success = vi.spyOn(toast, 'success')
    render(<TwoFACard loading={false} />)
    await user.click(await screen.findByRole('button', { name: 'Enable' }))
    await user.type(
      await screen.findByLabelText('Password', { selector: 'input' }),
      'password'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    expect(await screen.findByText('OLD_SECRET')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await user.type(screen.getByLabelText('Verification Code'), '123456')

    clock.mockReturnValue(issuedAt + (expired === 'proof' ? 150_000 : 301_000))
    await user.click(screen.getByRole('button', { name: 'Enable 2FA' }))
    if (expired === 'proof') {
      await waitFor(() =>
        expect(success).toHaveBeenCalledExactlyOnceWith(
          'Two-factor authentication enabled successfully!'
        )
      )
      expect(posts).toHaveBeenCalledWith(
        '/api/user/2fa/enable',
        { code: '123456', flow_token: 'setup-1' },
        expect.objectContaining({ acceptAuthRotation: true })
      )
      expect(proofCount).toBe(1)
      expect(
        screen.queryByLabelText('Password', { selector: 'input' })
      ).not.toBeInTheDocument()
      expect(
        posts.mock.calls.filter(([url]) => url === '/api/user/2fa/setup')
      ).toHaveLength(1)
    } else {
      const password = await screen.findByLabelText('Password', {
        selector: 'input',
      })
      expect(password).toHaveValue('')
      expect(
        posts.mock.calls.filter(([url]) => url === '/api/user/2fa/enable')
      ).toHaveLength(0)
      await user.type(password, 'password')
      await user.click(screen.getByRole('button', { name: 'Verify' }))
      expect(await screen.findByText('NEW_SECRET')).toBeVisible()
      expect(screen.queryByText('OLD_SECRET')).not.toBeInTheDocument()
      expect(info).toHaveBeenCalledWith(
        'Setup expired. Scan the new QR code and save the new backup codes.'
      )
      expect(
        posts.mock.calls.filter(([url]) => url === '/api/user/2fa/enable')
      ).toHaveLength(0)
      await user.click(screen.getByRole('button', { name: 'Next' }))
      expect(await screen.findByText('NEW-CODE')).toBeVisible()
      expect(screen.queryByText('OLD-CODE')).not.toBeInTheDocument()
    }
  }
)

it.each(['wrong code', 'response lost'] as const)(
  'handles %s during activation without reusing the setup proof',
  async (failure) => {
    let enabled = false
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/user/2fa/status') {
        return { data: { success: true, data: { enabled, locked: false } } }
      }
      if (url === '/api/verify/methods') {
        return {
          data: {
            success: true,
            data: {
              scope: '2fa.setup',
              methods: [{ method: 'password', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            },
          },
        }
      }
      throw new Error(`Unexpected GET ${url}`)
    })
    let enableAttempts = 0
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/verify') {
        return {
          data: {
            success: true,
            data: {
              proof_token: 'setup-proof',
              scope: '2fa.setup',
              method: 'password',
              expires_at: expiresAt(),
            },
          },
        }
      }
      if (url === '/api/user/2fa/setup') {
        return {
          data: {
            success: true,
            data: {
              secret: 'SETUP_SECRET',
              qr_code_data: 'otpauth://totp/account?secret=SETUP_SECRET',
              backup_codes: ['ABCD-1234'],
              flow_token: 'setup-flow',
              expires_at: expiresAt(),
            },
          },
        }
      }
      if (url === '/api/user/2fa/enable') {
        enableAttempts++
        if (failure === 'response lost') {
          enabled = true
          throw new Error('Connection lost')
        }
        if (enableAttempts === 1) {
          return { data: { success: false, code: 'TWOFA_CODE_INVALID' } }
        }
        enabled = true
        return { data: { success: true, data: {} } }
      }
      throw new Error(`Unexpected POST ${url}`)
    })
    const user = userEvent.setup()
    render(<TwoFACard loading={false} />)
    await user.click(await screen.findByRole('button', { name: 'Enable' }))
    await user.type(
      await screen.findByLabelText('Password', { selector: 'input' }),
      'password'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await screen.findByText('SETUP_SECRET')
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await user.click(screen.getByRole('button', { name: 'Next' }))
    await user.type(screen.getByLabelText('Verification Code'), '123456')
    await user.click(screen.getByRole('button', { name: 'Enable 2FA' }))
    if (failure === 'wrong code') {
      expect(
        await screen.findByText('The authenticator code is incorrect.')
      ).toBeVisible()
      await user.clear(screen.getByLabelText('Verification Code'))
      await user.type(screen.getByLabelText('Verification Code'), '654321')
      await user.click(screen.getByRole('button', { name: 'Enable 2FA' }))
    }
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await waitFor(() =>
      expect(
        get.mock.calls.filter(([url]) => url === '/api/user/2fa/status')
      ).toHaveLength(2)
    )
    expect(
      post.mock.calls.filter(([url]) => url === '/api/verify')
    ).toHaveLength(1)
    expect(
      post.mock.calls.filter(([url]) => url === '/api/user/2fa/setup')
    ).toHaveLength(1)
    const activations = post.mock.calls.filter(
      ([url]) => url === '/api/user/2fa/enable'
    )
    expect(activations).toHaveLength(failure === 'wrong code' ? 2 : 1)
    for (const [, body, config] of activations) {
      expect(body).toMatchObject({ flow_token: 'setup-flow' })
      expect(config?.headers).toBeUndefined()
    }
    expect(
      screen.queryByRole('button', { name: 'Enable' })
    ).not.toBeInTheDocument()
  }
)
