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
import { Toaster, toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { AccessTokenStatus } from '../../api'
import { AccessTokenCard } from '../access-token-card'

let status: AccessTokenStatus
let proofCount: number

function passwordProof(scope: string) {
  proofCount += 1
  return {
    data: {
      success: true,
      data: {
        proof_token: `one-use-proof-${proofCount}`,
        scope,
        method: 'password',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  }
}

async function verifyPassword(user: ReturnType<typeof userEvent.setup>) {
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'current-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
}

beforeEach(() => {
  proofCount = 0
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  })
  status = {
    exists: false,
    token_ref: '',
    created_at: null,
    last_used_at: null,
    last_used_ip: '',
  }
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url === '/api/audit/self') {
      return { data: { success: true, data: { items: [], total: 0 } } }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: config?.params?.scope,
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    return { data: { success: true, data: status } }
  })
  vi.spyOn(api, 'post').mockImplementation(async (url, data) => {
    if (url === '/api/verify') {
      return passwordProof((data as { scope: string }).scope)
    }
    throw new Error(`Unexpected POST ${url}`)
  })
})
afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
  toast.dismiss()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <AccessTokenCard />
      <Toaster />
    </QueryClientProvider>
  )
  return client
}

describe('system access token management', () => {
  it('does not generate a token when identity verification is cancelled', async () => {
    const post = vi.mocked(api.post)
    renderCard()
    const user = userEvent.setup()
    await user.dblClick(await screen.findByRole('button', { name: 'Generate' }))
    await screen.findByLabelText('Password', { selector: 'input' })
    expect(post).not.toHaveBeenCalled()
    expect(
      vi
        .mocked(api.get)
        .mock.calls.filter(([url]) => url === '/api/verify/methods')
    ).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(post).not.toHaveBeenCalled()
    expect(screen.queryByLabelText('Token')).not.toBeInTheDocument()
  })

  it('ignores a successful verification response that arrives after cancellation', async () => {
    let complete!: (value: ReturnType<typeof passwordProof>) => void
    const pending = new Promise<ReturnType<typeof passwordProof>>((resolve) => {
      complete = resolve
    })
    const post = vi.mocked(api.post).mockImplementation(async (url) => {
      if (url === '/api/verify') return pending
      throw new Error(`Unexpected POST ${url}`)
    })
    renderCard()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Generate' }))
    await verifyPassword(user)
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        '/api/verify',
        expect.anything(),
        expect.anything()
      )
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await act(async () => {
      complete(passwordProof('access_token.generate'))
      await pending
    })
    expect(
      post.mock.calls.filter(([url]) => url === '/api/user/token')
    ).toHaveLength(0)
    expect(screen.queryByLabelText('Token')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Generate' })).toBeEnabled()
  })

  it('aborts and ignores a token response for an account that is no longer active', async () => {
    const token = 'previous-account-private-token'
    let complete!: (value: { data: { success: boolean; data: string } }) => void
    const pending = new Promise<{ data: { success: boolean; data: string } }>(
      (resolve) => {
        complete = resolve
      }
    )
    let signal: { readonly aborted: boolean } | undefined
    vi.mocked(api.post).mockImplementation(async (url, data, config) => {
      if (url === '/api/verify') {
        return passwordProof((data as { scope: string }).scope)
      }
      if (url === '/api/user/token') {
        signal = config?.signal
        return pending
      }
      throw new Error(`Unexpected POST ${url}`)
    })
    const client = renderCard()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Generate' }))
    await verifyPassword(user)
    await waitFor(() => expect(signal).toBeDefined())
    await act(async () => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'other-user', role: 100 })
    })
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      complete({ data: { success: true, data: token } })
      await pending
    })
    expect(screen.queryByDisplayValue(token)).not.toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Generate' })
    ).toBeEnabled()
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((entry) => entry.state.data)
      )
    ).not.toContain(token)
  })

  it('generates a missing token and clears the one-time plaintext on Escape', async () => {
    const token = 'one-time-private-token'
    vi.mocked(api.post).mockImplementation(async (url, data) => {
      if (url === '/api/verify') {
        return passwordProof((data as { scope: string }).scope)
      }
      status = {
        ...status,
        exists: true,
        token_ref: 'a'.repeat(64),
        created_at: 1700000000,
      }
      return { data: { success: true, data: token } }
    })
    const client = renderCard()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Generate' }))
    await verifyPassword(user)
    const dialog = await screen.findByRole('dialog', { name: 'Access Token' })
    expect(within(dialog).getByLabelText('Token')).toHaveValue(token)
    expect(api.post).toHaveBeenCalledWith(
      '/api/user/token',
      undefined,
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'one-use-proof-1' },
        singleUseAuthorization: true,
      })
    )
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByDisplayValue(token)).not.toBeInTheDocument()
    )
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((entry) => entry.state.data)
      )
    ).not.toContain(token)
    expect(JSON.stringify(client.getMutationCache().getAll())).not.toContain(
      'one-use-proof-1'
    )
    expect(
      JSON.stringify(
        client
          .getMutationCache()
          .getAll()
          .map((entry) => entry.state.data)
      )
    ).not.toContain(token)
    expect(await screen.findByText('Not used yet')).toBeVisible()
  })

  it('legacy tokens show unknown creation and usage until records exist', async () => {
    status = { ...status, exists: true, token_ref: 'a'.repeat(64) }
    renderCard()
    expect(await screen.findAllByText('Unknown')).toHaveLength(2)
    expect(screen.queryByText('Not used yet')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Generate' })
    ).not.toBeInTheDocument()
  })

  it('status failures offer retry without asserting that a token is missing or unused', async () => {
    vi.mocked(api.get).mockRejectedValueOnce(new Error('offline'))
    renderCard()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Failed to load token status'
    )
    expect(screen.queryByText('Not generated')).not.toBeInTheDocument()
    expect(screen.queryByText('Not used yet')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Generate' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Not generated')).toBeVisible()
  })

  it('rotation requires confirmation and verification, and failure keeps the existing token state', async () => {
    status = { ...status, exists: true, token_ref: 'a'.repeat(64) }
    const post = vi.mocked(api.post).mockImplementation(async (url, data) => {
      if (url === '/api/verify') {
        return passwordProof((data as { scope: string }).scope)
      }
      return { data: { success: false } }
    })
    renderCard()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Regenerate' }))
    const confirmation = await screen.findByRole('alertdialog')
    expect(post).not.toHaveBeenCalled()
    await user.click(
      within(confirmation).getByRole('button', { name: 'Regenerate token' })
    )
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    await verifyPassword(user)
    expect(await screen.findByText('Failed to generate token')).toBeVisible()
    expect(
      screen.queryByRole('dialog', { name: 'Access Token' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('Generated')).toBeVisible()
  })

  it('revocation failures can be retried and success restores the generate action', async () => {
    status = { ...status, exists: true, token_ref: 'a'.repeat(64) }
    vi.spyOn(api, 'delete')
      .mockResolvedValueOnce({ data: { success: false } })
      .mockImplementation(async () => {
        status = { ...status, exists: false, token_ref: '' }
        return { data: { success: true, data: null } }
      })
    renderCard()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    const confirm = within(await screen.findByRole('alertdialog')).getByRole(
      'button',
      { name: 'Revoke' }
    )
    await user.click(confirm)
    await verifyPassword(user)
    expect(await screen.findByText('Failed to revoke token')).toBeVisible()
    expect(api.delete).toHaveBeenLastCalledWith(
      '/api/user/token',
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'one-use-proof-1' },
      })
    )
    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    await user.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', {
        name: 'Revoke',
      })
    )
    await verifyPassword(user)
    expect(api.delete).toHaveBeenLastCalledWith(
      '/api/user/token',
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'one-use-proof-2' },
      })
    )
    expect(
      await screen.findByRole('button', { name: 'Generate' })
    ).toBeVisible()
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
  })

  it('access history opens by keyboard in a full-width mobile sheet and Escape closes it', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'admin', role: 100 })
    renderCard()
    const user = userEvent.setup()
    const trigger = screen.getByRole('button', { name: 'Access records' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const sheet = await screen.findByRole('dialog', { name: 'Access records' })
    expect(sheet).toHaveClass('w-full', 'sm:max-w-5xl')
    await waitFor(() =>
      expect(api.get).toHaveBeenCalledWith('/api/audit/self', {
        params: expect.objectContaining({ category: 'access_token' }),
      })
    )
    expect(api.get).not.toHaveBeenCalledWith('/api/audit', expect.anything())
    expect(
      within(sheet).queryByRole('tablist', { name: 'View scope' })
    ).not.toBeInTheDocument()
    expect(
      within(sheet).getByRole('combobox', { name: 'Token scope' })
    ).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(trigger).toHaveFocus()
  })
})
