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
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

import { ChangePasswordDialog } from '../components/dialogs/change-password-dialog'
import { DeleteAccountDialog } from '../components/dialogs/delete-account-dialog'
import { EmailBindDialog } from '../components/dialogs/email-bind-dialog'
import { TwoFABackupDialog } from '../components/dialogs/two-fa-backup-dialog'
import { TwoFADisableDialog } from '../components/dialogs/two-fa-disable-dialog'

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}))

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, { passkey_rp_ids: ['localhost'] })
})

afterEach(() => {
  cleanup()
  client.clear()
  navigate.mockReset()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.reset('idle')
})

it('requires verification after username confirmation and cancels without deleting the account', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'account.delete',
        methods: [{ method: '2fa', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const remove = vi.spyOn(api, 'delete').mockResolvedValue({
    data: { success: true, data: {} },
  })
  const user = userEvent.setup()
  render(<DeleteAccountDialog open username='user' onOpenChange={vi.fn()} />)
  expect(screen.getByRole('button', { name: 'Delete Account' })).toBeDisabled()
  await user.type(screen.getByRole('textbox'), 'user')
  await user.click(screen.getByRole('button', { name: 'Delete Account' }))
  expect(
    await screen.findByRole('textbox', {
      name: 'Authenticator code or backup code',
    })
  ).toBeVisible()
  expect(remove).not.toHaveBeenCalled()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Delete Account' })).toBeVisible()
  )
  expect(remove).not.toHaveBeenCalled()
  expect(navigate).not.toHaveBeenCalled()
})

it.each(['2fa', 'passkey'])(
  'deletes the account only after %s verification and clears authentication',
  async (method) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'user', role: 1 })
    vi.stubGlobal('PublicKeyCredential', class {})
    vi.stubGlobal('navigator', {
      credentials: {
        get: vi.fn().mockResolvedValue({
          id: 'passkey',
          rawId: new ArrayBuffer(1),
          type: 'public-key',
          response: {
            authenticatorData: new ArrayBuffer(1),
            clientDataJSON: new ArrayBuffer(1),
            signature: new ArrayBuffer(1),
            userHandle: null,
          },
          getClientExtensionResults: () => ({}),
        }),
      },
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          scope: 'account.delete',
          methods: [
            { method: '2fa', available: true },
            { method: 'passkey', available: true },
          ],
          oauth_providers: [],
          password_encryption_enabled: false,
        },
      },
    })
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/user/passkey/verify/begin') {
        return {
          data: {
            success: true,
            data: {
              flow_token: 'passkey-flow',
              options: { publicKey: { challenge: 'Y2hhbGxlbmdl' } },
            },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            proof_token: 'delete-proof',
            scope: 'account.delete',
            method,
            expires_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    })
    const remove = vi
      .spyOn(api, 'delete')
      .mockResolvedValue({ data: { success: true, data: {} } })
    const close = vi.fn()
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <DeleteAccountDialog open username='user' onOpenChange={close} />
      </QueryClientProvider>
    )
    await user.type(screen.getByRole('textbox'), 'user')
    await user.click(screen.getByRole('button', { name: 'Delete Account' }))
    expect(await screen.findByRole('tab', { name: 'Passkey' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    if (method === '2fa') {
      await user.click(screen.getByRole('tab', { name: 'Authenticator code' }))
      await user.type(
        screen.getByRole('textbox', {
          name: 'Authenticator code or backup code',
        }),
        '123456'
      )
    }
    expect(remove).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ to: '/sign-in' })
    )
    expect(remove).toHaveBeenCalledExactlyOnceWith(
      '/api/user/self',
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'delete-proof' },
        singleUseAuthorization: true,
        signal: expect.any(AbortSignal),
      })
    )
    expect(close).toHaveBeenCalledWith(false)
    expect(useAuthStore.getState().auth.user).toBeNull()
    expect(post.mock.calls.map(([url]) => url)).not.toContain(
      '/api/user/logout'
    )
  }
)

it.each(['unmount', 'account change'])(
  'ignores a late deletion response after %s and prevents duplicate requests',
  async (reason) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'user', role: 1 })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          scope: 'account.delete',
          methods: [{ method: 'password', available: true }],
          oauth_providers: [],
          password_encryption_enabled: false,
        },
      },
    })
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          scope: 'account.delete',
          method: 'password',
          proof_token: 'delete-proof',
          expires_at: Math.floor(Date.now() / 1000) + 60,
        },
      },
    })
    const response = { data: { success: true, data: {} } }
    let resolveDeletion!: (result: typeof response) => void
    const remove = vi.spyOn(api, 'delete').mockReturnValue(
      new Promise<typeof response>((resolve) => {
        resolveDeletion = resolve
      })
    )
    const user = userEvent.setup()
    const view = render(
      <DeleteAccountDialog open username='user' onOpenChange={vi.fn()} />
    )
    await user.type(screen.getByRole('textbox'), 'user')
    await user.click(screen.getByRole('button', { name: 'Delete Account' }))
    await user.type(
      await screen.findByLabelText('Password', { selector: 'input' }),
      'account-password'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() => expect(remove).toHaveBeenCalledOnce())
    const submit = await screen.findByRole('button', { name: 'Deleting...' })
    expect(submit).toBeDisabled()
    await user.dblClick(submit)
    if (reason === 'unmount') {
      view.unmount()
    } else {
      act(() =>
        useAuthStore
          .getState()
          .auth.setUser({ id: 2, username: 'second-user', role: 1 })
      )
      expect(screen.getByRole('textbox')).toHaveValue('')
    }
    expect(remove.mock.calls[0][1]?.signal?.aborted).toBe(true)
    await act(async () => resolveDeletion(response))
    expect(remove).toHaveBeenCalledOnce()
    expect(navigate).not.toHaveBeenCalled()
    expect(useAuthStore.getState().auth.user?.id).toBe(
      reason === 'unmount' ? 1 : 2
    )
  }
)

it('disables 2FA through a Passkey proof without asking for an authenticator code', async () => {
  vi.stubGlobal('PublicKeyCredential', class {})
  const credential = {
    id: 'passkey',
    rawId: new ArrayBuffer(1),
    type: 'public-key',
    response: {
      authenticatorData: new ArrayBuffer(1),
      clientDataJSON: new ArrayBuffer(1),
      signature: new ArrayBuffer(1),
      userHandle: null,
    },
    getClientExtensionResults: () => ({}),
  }
  vi.stubGlobal('navigator', {
    credentials: { get: vi.fn().mockResolvedValue(credential) },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: '2fa.disable',
        methods: [
          { method: '2fa', available: true },
          { method: 'passkey', available: true },
        ],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/user/passkey/verify/begin') {
      return {
        data: {
          success: true,
          data: {
            flow_token: 'passkey-flow',
            options: { publicKey: { challenge: 'Y2hhbGxlbmdl' } },
          },
        },
      }
    }
    if (url === '/api/user/passkey/verify/finish') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'disable-proof',
            scope: '2fa.disable',
            method: 'passkey',
            expires_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    }
    return { data: { success: true, data: {} } }
  })
  const close = vi.fn()
  const success = vi.fn()
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <TwoFADisableDialog open onOpenChange={close} onSuccess={success} />
    </QueryClientProvider>
  )
  await user.click(screen.getByRole('checkbox'))
  await user.click(screen.getByRole('button', { name: 'Disable 2FA' }))
  expect(await screen.findByRole('tab', { name: 'Passkey' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(
    screen.queryByLabelText('Authenticator code or backup code')
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() => expect(success).toHaveBeenCalledTimes(1))
  expect(post).toHaveBeenLastCalledWith(
    '/api/user/2fa/disable',
    {},
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'disable-proof' },
      signal: expect.any(AbortSignal),
      acceptAuthRotation: true,
    })
  )
  expect(close).toHaveBeenCalledWith(false)
})

it('regenerates backup codes through scoped verification and keeps the result visible until dismissed', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: '2fa.backup_codes.regenerate',
        methods: [{ method: '2fa', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'backup-proof',
            scope: '2fa.backup_codes.regenerate',
            method: '2fa',
            expires_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: { backup_codes: ['ABCD-1234', 'EFGH-5678'] },
      },
    }
  })
  const close = vi.fn()
  const success = vi.fn()
  const user = userEvent.setup()
  render(<TwoFABackupDialog open onOpenChange={close} onSuccess={success} />)
  await user.click(screen.getByRole('button', { name: 'Generate New Codes' }))
  const input = await screen.findByRole('textbox', {
    name: 'Authenticator code',
  })
  expect(input).toHaveAttribute('maxlength', '6')
  expect(
    screen.queryByLabelText('Authenticator code or backup code')
  ).not.toBeInTheDocument()
  await user.type(input, '123456')
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(await screen.findByText('ABCD-1234')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Copy all backup codes' })
  ).toBeEnabled()
  expect(post).toHaveBeenLastCalledWith(
    '/api/user/2fa/backup_codes',
    {},
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'backup-proof' },
      acceptAuthRotation: true,
    })
  )
  expect(success).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Done' }))
  expect(close).toHaveBeenCalledWith(false)
  expect(success).toHaveBeenCalledTimes(1)
})

it('allows a common new password after verifying the current password once', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'account.password.change',
        methods: [{ method: 'password', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'account.password.change',
        method: 'password',
        proof_token: 'password-proof',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { has_password: true } },
  })
  const user = userEvent.setup()
  const onOpenChange = vi.fn()
  render(
    <ChangePasswordDialog open username='user' onOpenChange={onOpenChange} />
  )
  expect(screen.getByText('Use 8–128 characters.')).toBeVisible()
  await user.type(screen.getByLabelText('Current Password'), 'current-password')
  await user.type(screen.getByLabelText('New Password'), 'password123')
  await user.type(screen.getByLabelText('Confirm New Password'), 'password123')
  await user.click(screen.getByRole('button', { name: 'Change Password' }))
  await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  expect(get).toHaveBeenCalledWith(
    '/api/verify/methods',
    expect.objectContaining({ params: { scope: 'account.password.change' } })
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      method: 'password',
      password: 'current-password',
      scope: 'account.password.change',
    }),
    expect.anything()
  )
  expect(put).toHaveBeenCalledWith(
    '/api/user/self',
    expect.objectContaining({
      original_password: 'current-password',
      password: 'password123',
    }),
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'password-proof' },
      singleUseAuthorization: true,
    })
  )
})

it.each(['button', 'escape'])(
  'cancels additional verification and restores focus using %s',
  async (method) => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          scope: 'account.password.change',
          methods: [{ method: '2fa', available: true }],
          oauth_providers: [],
          password_encryption_enabled: false,
        },
      },
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ChangePasswordDialog open username='user' onOpenChange={vi.fn()} />)
    await user.type(
      screen.getByLabelText('Current Password'),
      'current-password'
    )
    await user.type(
      screen.getByLabelText('New Password'),
      'account-password!42'
    )
    await user.type(
      screen.getByLabelText('Confirm New Password'),
      'account-password!42'
    )
    await user.click(screen.getByRole('button', { name: 'Change Password' }))
    const verification = await screen.findByRole('dialog', {
      name: 'Security verification',
    })
    if (method === 'button') {
      await user.click(
        within(verification).getByRole('button', { name: 'Cancel' })
      )
    } else {
      await user.keyboard('{Escape}')
    }
    expect(await screen.findByLabelText('Current Password')).toBeVisible()
    await waitFor(() =>
      expect(
        screen.getByRole('dialog', { name: 'Change Password' })
      ).toContainElement(document.activeElement as HTMLElement)
    )
    expect(put).not.toHaveBeenCalled()
  }
)

it.each(['unmount', 'account change'])(
  'aborts a pending password operation on %s without duplicate submission',
  async (reason) => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'first-user', role: 1 })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          scope: 'account.password.change',
          methods: [{ method: 'password', available: true }],
          oauth_providers: [],
          password_encryption_enabled: false,
        },
      },
    })
    const proofResponse = {
      data: {
        success: true,
        data: {
          scope: 'account.password.change',
          method: 'password',
          proof_token: 'pending-proof',
          expires_at: Math.floor(Date.now() / 1000) + 60,
        },
      },
    }
    let resolveProof!: (response: typeof proofResponse) => void
    const pending = new Promise<typeof proofResponse>((resolve) => {
      resolveProof = resolve
    })
    const post = vi.spyOn(api, 'post').mockReturnValue(pending)
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: {} } })
    const user = userEvent.setup()
    const view = render(
      <ChangePasswordDialog open username='user' onOpenChange={vi.fn()} />
    )
    await user.type(
      screen.getByLabelText('Current Password'),
      'current-password'
    )
    await user.type(
      screen.getByLabelText('New Password'),
      'account-password!42'
    )
    await user.type(
      screen.getByLabelText('Confirm New Password'),
      'account-password!42'
    )
    const submit = screen.getByRole('button', { name: 'Change Password' })
    await user.click(submit)
    await waitFor(() => expect(post).toHaveBeenCalledOnce())
    expect(submit).toBeDisabled()
    await user.dblClick(submit)
    if (reason === 'unmount') {
      view.unmount()
    } else {
      act(() =>
        useAuthStore
          .getState()
          .auth.setUser({ id: 2, username: 'second-user', role: 1 })
      )
      expect(screen.getByLabelText('Current Password')).toHaveValue('')
      expect(screen.getByLabelText('New Password')).toHaveValue('')
    }
    expect(post.mock.calls[0][2]?.signal?.aborted).toBe(true)
    await act(async () => {
      resolveProof(proofResponse)
    })
    expect(post).toHaveBeenCalledOnce()
    expect(put).not.toHaveBeenCalled()
  }
)

it('sets a first password after verifying an existing factor without asking for a current password', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'account.password.set',
        methods: [{ method: '2fa', available: true }],
        oauth_providers: [],
        password_encryption_enabled: false,
      },
    },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        scope: 'account.password.set',
        method: '2fa',
        proof_token: 'first-password-proof',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { has_password: true } },
  })
  const user = userEvent.setup()
  render(
    <ChangePasswordDialog
      open
      username='user'
      hasPassword={false}
      onOpenChange={vi.fn()}
    />
  )
  expect(screen.queryByLabelText('Current Password')).not.toBeInTheDocument()
  await user.type(
    screen.getByLabelText('New Password'),
    'first-account-password!42'
  )
  await user.type(
    screen.getByLabelText('Confirm New Password'),
    'first-account-password!42'
  )
  await user.click(screen.getByRole('button', { name: 'Set Password' }))
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code', {
      selector: 'input',
    }),
    '123456'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  expect(post).toHaveBeenCalledTimes(1)
  expect(put.mock.calls[0][1]).toEqual({
    password: 'first-account-password!42',
  })
})

it('confirms both email addresses after identity verification and freezes the submitted address', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
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
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            scope: 'account.binding.bind',
            method: 'password',
            proof_token: 'email-proof',
            expires_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    }
    if (url === '/api/oauth/email/bind/start') {
      return {
        data: {
          success: true,
          data: {
            flow_token: 'email-flow',
            email: 'new@example.com',
            current_email: 'o***@example.com',
            old_email_required: true,
            expires_at: Math.floor(Date.now() / 1000) + 600,
            resend_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    }
    return { data: { success: true, data: {} } }
  })
  const onSuccess = vi.fn()
  const user = userEvent.setup()
  render(
    <EmailBindDialog
      open
      currentEmail='old@example.com'
      onOpenChange={vi.fn()}
      onSuccess={onSuccess}
    />
  )
  await user.type(screen.getByLabelText('Email Address'), 'new@example.com')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'current-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(await screen.findByLabelText('Email Address')).toBeDisabled()
  await user.type(
    await screen.findByLabelText('New email verification code'),
    '123456'
  )
  await user.type(
    screen.getByLabelText('Current email verification code'),
    '654321'
  )
  await user.click(screen.getByRole('button', { name: 'Confirm email' }))
  await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
  expect(post).toHaveBeenCalledWith(
    '/api/oauth/email/bind',
    { flow_token: 'email-flow', new_code: '123456', old_code: '654321' },
    expect.objectContaining({ singleUseAuthorization: true })
  )
})
