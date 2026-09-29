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
// @vitest-environment-options {"url":"https://www.example.com"}
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import type { AuthBundle } from '@/stores/auth-store'

import { verify, verifyLogin } from '../../secure-verification/api'
import { SecureVerificationDialog } from '../../secure-verification/components/secure-verification-dialog'
import { useSecureVerification } from '../../secure-verification/hooks/use-secure-verification'
import type { SecurityProof } from '../../secure-verification/types'
import { PasskeyDomainSelector } from '../components/passkey-domain-selector'

const storageKey = 'passkey:last-successful-rp-id'
const operation = {
  scope: 'channel.key.read',
  context: { channel_id: 7 },
} as const
const proof: SecurityProof = {
  proof_token: 'verified-proof',
  scope: operation.scope,
  method: 'passkey',
  expires_at: 4102444800,
}
const credential = {
  id: 'credential',
  rawId: new Uint8Array([1, 2, 3]).buffer,
  type: 'public-key',
  response: {
    clientDataJSON: new Uint8Array([1]).buffer,
    authenticatorData: new Uint8Array([2]).buffer,
    signature: new Uint8Array([3]).buffer,
    userHandle: new Uint8Array([4]).buffer,
  },
  getClientExtensionResults: () => ({}),
}

function setupPasskeyNetwork() {
  const get = vi.fn().mockResolvedValue(credential)
  vi.stubGlobal('PublicKeyCredential', class {})
  vi.stubGlobal('navigator', { credentials: { get } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/status'
          ? {
              passkey_rp_ids: ['example.com', 'www.example.com'],
            }
          : {
              scope: operation.scope,
              methods: [{ method: 'passkey', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            },
    },
  }))
  let flow = 0
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (url, data?: unknown) => {
      const request = data as { rp_id?: string } | undefined
      if (url.endsWith('/begin')) {
        return {
          data: {
            success: true,
            data: {
              flow_token: `flow-${++flow}`,
              rp_ids: ['example.com', 'www.example.com'],
              options: {
                publicKey: {
                  challenge: 'AQID',
                  rpId: request?.rp_id ?? 'example.com',
                },
              },
            },
          },
        }
      }
      return { data: { success: true, data: proof } }
    })
  return { get, post }
}

function DomainVerificationHarness(props: {
  onResult: (proof: SecurityProof | null) => void
}) {
  const verification = useSecureVerification()
  return (
    <>
      <button
        type='button'
        onClick={async () =>
          props.onResult(await verification.requestVerification(operation))
        }
      >
        Protected action
      </button>
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}

beforeEach(() => localStorage.clear())
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('Passkey domain compatibility', () => {
  it('binds a login factor to its pending login and the selected domain challenge', async () => {
    const { get, post } = setupPasskeyNetwork()
    const bundle: AuthBundle = {
      access_token: 'login-access',
      token_type: 'Bearer',
      access_expires_at: 4102444800,
      user: { id: 7, username: 'user', role: 1 },
      session: {
        sid: 'login-session',
        current: true,
        login_method: 'passkey',
        ip: '',
        user_agent: '',
        created_at: 1,
        last_active_at: 1,
        expires_at: 4102444800,
      },
    }
    post
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            flow_token: 'domain-flow',
            rp_ids: ['www.example.com'],
            options: {
              publicKey: { challenge: 'AQID', rpId: 'www.example.com' },
            },
          },
        },
      })
      .mockResolvedValueOnce({ data: { success: true, data: bundle } })
    await expect(
      verifyLogin(
        { method: 'passkey', rpID: 'www.example.com' },
        {
          require_verification: true,
          flow_token: 'pending-login',
          expires_at: 4102444800,
          methods: [{ method: 'passkey', available: true }],
        },
        new AbortController().signal
      )
    ).resolves.toEqual(bundle)
    expect(get.mock.calls[0][0].publicKey.rpId).toBe('www.example.com')
    expect(post).toHaveBeenNthCalledWith(
      1,
      '/api/user/login/passkey/begin',
      {
        flow_token: 'pending-login',
        rp_id: 'www.example.com',
      },
      expect.objectContaining({ skipAuthRefresh: true })
    )
    expect(post).toHaveBeenLastCalledWith(
      '/api/user/login/passkey/finish',
      expect.objectContaining({
        flow_token: 'pending-login',
        passkey_flow_token: 'domain-flow',
      }),
      expect.anything()
    )
    expect(localStorage.getItem(storageKey)).toBe('www.example.com')
  })

  it('uses the last successful domain without storing flows or credentials', async () => {
    const { get, post } = setupPasskeyNetwork()
    localStorage.setItem(storageKey, 'www.example.com')
    await expect(
      verify(
        { method: 'passkey' },
        operation,
        false,
        new AbortController().signal
      )
    ).resolves.toEqual(proof)
    expect(post).toHaveBeenNthCalledWith(
      1,
      '/api/user/passkey/verify/begin',
      { ...operation, rp_id: 'www.example.com' },
      expect.anything()
    )
    expect(get).toHaveBeenCalledOnce()
    expect(get.mock.calls[0][0].publicKey.rpId).toBe('www.example.com')
    expect(localStorage.getItem(storageKey)).toBe('www.example.com')
    expect(localStorage.length).toBe(1)
  })

  it('accepts the credential domain chosen by the server over a remembered hint', async () => {
    const { get, post } = setupPasskeyNetwork()
    localStorage.setItem(storageKey, 'example.com')
    post.mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          flow_token: 'known-flow',
          rp_ids: ['www.example.com'],
          options: {
            publicKey: { challenge: 'AQID', rpId: 'www.example.com' },
          },
        },
      },
    })
    await verify(
      { method: 'passkey' },
      operation,
      false,
      new AbortController().signal
    )
    expect(get.mock.calls[0][0].publicKey.rpId).toBe('www.example.com')
    expect(localStorage.getItem(storageKey)).toBe('www.example.com')
  })

  it('discards a removed remembered domain before opening a single browser prompt', async () => {
    const { get, post } = setupPasskeyNetwork()
    localStorage.setItem(storageKey, 'removed.example.com')
    post.mockResolvedValueOnce({
      data: { success: false, code: 'PASSKEY_RP_ID_UNAVAILABLE' },
    })
    await verify(
      { method: 'passkey' },
      operation,
      false,
      new AbortController().signal
    )
    expect(post).toHaveBeenNthCalledWith(
      2,
      '/api/user/passkey/verify/begin',
      operation,
      expect.anything()
    )
    expect(get).toHaveBeenCalledOnce()
    expect(localStorage.getItem(storageKey)).toBe('example.com')
  })

  it('does not remember or replay a failed finish request', async () => {
    const { get, post } = setupPasskeyNetwork()
    localStorage.setItem(storageKey, 'example.com')
    post
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: {
            flow_token: 'flow',
            rp_ids: ['example.com', 'www.example.com'],
            options: {
              publicKey: { challenge: 'AQID', rpId: 'www.example.com' },
            },
          },
        },
      })
      .mockResolvedValueOnce({
        data: { success: false, code: 'SECURITY_VERIFICATION_FAILED' },
      })
    await expect(
      verify(
        { method: 'passkey', rpID: 'www.example.com' },
        operation,
        false,
        new AbortController().signal
      )
    ).rejects.toThrow()
    expect(get).toHaveBeenCalledOnce()
    expect(post).toHaveBeenCalledTimes(2)
    expect(localStorage.getItem(storageKey)).toBe('example.com')
  })

  it('allows a manual domain switch after cancellation with a fresh challenge', async () => {
    const { get, post } = setupPasskeyNetwork()
    get.mockRejectedValueOnce(
      new DOMException('No matching credential', 'NotAllowedError')
    )
    const onResult = vi.fn()
    const user = userEvent.setup()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <DomainVerificationHarness onResult={onResult} />
      </QueryClientProvider>
    )
    await user.click(screen.getByRole('button', { name: 'Protected action' }))
    await user.click(await screen.findByRole('button', { name: 'Verify' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Passkey verification was cancelled or timed out'
    )
    expect(get).toHaveBeenCalledOnce()
    expect(post).toHaveBeenCalledTimes(1)
    expect(onResult).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Use a Passkey from another website' })
    )
    await user.click(
      screen.getByRole('combobox', { name: 'Passkey website domain' })
    )
    await user.click(
      await screen.findByRole('option', { name: 'www.example.com' })
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith(proof))
    expect(get).toHaveBeenCalledTimes(2)
    expect(post).toHaveBeenNthCalledWith(
      2,
      '/api/user/passkey/verify/begin',
      { ...operation, rp_id: 'www.example.com' },
      expect.anything()
    )
    expect(post).toHaveBeenLastCalledWith(
      '/api/user/passkey/verify/finish',
      expect.objectContaining({ flow_token: 'flow-2' }),
      expect.anything()
    )
  })

  it('supports verification when browser storage is blocked', async () => {
    const { get } = setupPasskeyNetwork()
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('Blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('Blocked')
    })
    await expect(
      verify(
        { method: 'passkey' },
        operation,
        false,
        new AbortController().signal
      )
    ).resolves.toEqual(proof)
    expect(get).toHaveBeenCalledOnce()
  })

  it('does not submit an assertion after its operation is aborted', async () => {
    const { get, post } = setupPasskeyNetwork()
    const controller = new AbortController()
    get.mockImplementation(async () => {
      controller.abort()
      return credential
    })
    await expect(
      verify({ method: 'passkey' }, operation, false, controller.signal)
    ).rejects.toThrow()
    expect(post).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem(storageKey)).toBeNull()
  })
})

it('keeps case-sensitive historical domains selectable and labels the distinction', async () => {
  setupPasskeyNetwork()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: {
        passkey_rp_ids: ['www.example.com', 'WWW.example.com'],
      },
    },
  })
  const changed = vi.fn()
  const user = userEvent.setup()
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <PasskeyDomainSelector value='www.example.com' onChange={changed} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', {
      name: 'Use a Passkey from another website',
    })
  )
  await user.click(screen.getByRole('combobox'))
  expect(
    await screen.findByRole('option', { name: 'www.example.com' })
  ).toBeVisible()
  await user.click(
    screen.getByRole('option', {
      name: /WWW.example.com.*Historical capitalization/,
    })
  )
  expect(changed).toHaveBeenCalledExactlyOnceWith('WWW.example.com')
})

it.each([
  { rpIDs: ['api.example.com'], canSwitch: false },
  { rpIDs: ['example.com'], canSwitch: false },
  { rpIDs: ['example.com', 'www.example.com'], canSwitch: true },
])('does not display origins from cached status with $rpIDs', async (test) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, {
    passkey_rp_ids: test.rpIDs,
    passkey_origins: 'https://www.example.com,https://api.example.com',
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <PasskeyDomainSelector onChange={vi.fn()} />
    </QueryClientProvider>
  )

  if (test.canSwitch) {
    await user.click(screen.getByRole('button'))
    expect(screen.getByRole('combobox')).toBeVisible()
  } else {
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  }
  expect(screen.queryAllByRole('link')).toHaveLength(0)
  expect(screen.queryByText('https://api.example.com')).not.toBeInTheDocument()
})
