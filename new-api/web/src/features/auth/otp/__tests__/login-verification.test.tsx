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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

import { useAuthRedirect } from '../../hooks/use-auth-redirect'
import type { LoginResult } from '../../secure-verification/types'
import { OtpForm } from '../components/otp-form'

const bundle: AuthBundle = {
  access_token: 'completed-login',
  token_type: 'Bearer',
  access_expires_at: 9999999999,
  user: { id: 42, username: 'verified-user', role: 1 },
  session: {
    sid: 'verified-session',
    current: true,
    login_method: 'password',
    ip: '',
    user_agent: '',
    created_at: 1,
    last_active_at: 1,
    expires_at: 9999999999,
  },
}

function PrimaryLoginHarness(props: { result: LoginResult }) {
  const { handleLoginResult } = useAuthRedirect()
  return (
    <button
      type='button'
      onClick={() => void handleLoginResult(props.result, '/pricing?view=grid')}
    >
      Primary authentication succeeded
    </button>
  )
}

function renderLoginVerification(
  hasChallenge = true,
  primaryResult?: LoginResult
) {
  useAuthStore.getState().auth.reset('complete')
  if (hasChallenge) {
    useAuthStore.getState().auth.setPendingLoginVerification({
      challenge: {
        require_verification: true,
        flow_token: 'login-flow',
        expires_at: Math.floor(Date.now() / 1000) + 300,
        methods: [{ method: '2fa', available: true }],
      },
      redirectTo: '/pricing?view=grid',
    })
  }
  const root = createRootRoute({ component: Outlet })
  const routes = [
    createRoute({
      getParentRoute: () => root,
      path: '/otp',
      component: OtpForm,
    }),
    createRoute({
      getParentRoute: () => root,
      path: '/sign-in',
      component: () => <div>Sign-in page</div>,
    }),
    createRoute({
      getParentRoute: () => root,
      path: '/pricing',
      component: () => <div>Pricing destination</div>,
    }),
    createRoute({
      getParentRoute: () => root,
      path: '/primary-login',
      component: () =>
        primaryResult ? <PrimaryLoginHarness result={primaryResult} /> : null,
    }),
  ]
  const router = createRouter({
    routeTree: root.addChildren(routes),
    history: createMemoryHistory({
      initialEntries: [primaryResult ? '/primary-login' : '/otp'],
    }),
  })
  const view = render(<RouterProvider router={router} />)
  return { router, ...view }
}

function pendingLoginResponse() {
  let resolve!: (value: {
    data: { success: boolean; data: AuthBundle }
  }) => void
  const promise = new Promise<{ data: { success: boolean; data: AuthBundle } }>(
    (finish) => {
      resolve = finish
    }
  )
  return { promise, resolve }
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset('idle')
})

it('routes a primary-authenticated challenge into shared verification without applying authentication', async () => {
  const post = vi.spyOn(api, 'post')
  const { router } = renderLoginVerification(false, {
    require_verification: true,
    flow_token: 'primary-challenge',
    expires_at: Math.floor(Date.now() / 1000) + 300,
    methods: [{ method: '2fa', available: true }],
  })
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', {
      name: 'Primary authentication succeeded',
    })
  )
  expect(
    await screen.findByLabelText('Authenticator code or backup code')
  ).toBeVisible()
  expect(router.state.location.pathname).toBe('/otp')
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(post).not.toHaveBeenCalled()
})

it('accepts a completed Passkey login without opening another verification dialog', async () => {
  const post = vi.spyOn(api, 'post')
  const { router } = renderLoginVerification(false, {
    ...bundle,
    session: { ...bundle.session, login_method: 'passkey' },
  })
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', {
      name: 'Primary authentication succeeded',
    })
  )
  await waitFor(() =>
    expect(router.state.location.href).toBe('/pricing?view=grid')
  )
  expect(useAuthStore.getState().auth.user?.id).toBe(42)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

it('writes authentication only after verification and preserves the original destination', async () => {
  const pending = pendingLoginResponse()
  const post = vi.spyOn(api, 'post').mockReturnValue(pending.promise)
  const { router } = renderLoginVerification()
  const user = userEvent.setup()
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  expect(useAuthStore.getState().auth.pendingLoginVerification).toBeNull()
  expect(useAuthStore.getState().auth.user).toBeNull()
  await user.dblClick(screen.getByRole('button', { name: 'Verify' }))
  expect(post).toHaveBeenCalledTimes(1)
  expect(useAuthStore.getState().auth.user).toBeNull()
  await act(async () => {
    pending.resolve({ data: { success: true, data: bundle } })
    await pending.promise
  })
  await waitFor(() =>
    expect(router.state.location.href).toBe('/pricing?view=grid')
  )
  expect(useAuthStore.getState().auth.user?.id).toBe(42)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})

it('cancels verification with Escape and returns to sign-in without authenticating', async () => {
  const post = vi.spyOn(api, 'post')
  const { router } = renderLoginVerification()
  const user = userEvent.setup()
  await screen.findByLabelText('Authenticator code or backup code')
  await user.keyboard('{Escape}')
  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
  expect(post).not.toHaveBeenCalled()
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(useAuthStore.getState().auth.pendingLoginVerification).toBeNull()
})

it.each(['unmount', 'account switch'] as const)(
  'aborts an in-flight verification on %s and ignores its late result',
  async (change) => {
    const pending = pendingLoginResponse()
    const post = vi.spyOn(api, 'post').mockReturnValue(pending.promise)
    const { router } = renderLoginVerification()
    const user = userEvent.setup()
    await user.type(
      await screen.findByLabelText('Authenticator code or backup code'),
      '123456'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await act(async () => {
      if (change === 'unmount') {
        await router.navigate({ to: '/sign-in' })
      } else {
        useAuthStore.getState().auth.setBundle({
          ...bundle,
          user: { ...bundle.user, id: 99 },
          session: { ...bundle.session, sid: 'other-session' },
        })
      }
    })
    expect(post.mock.calls[0][2]?.signal?.aborted).toBe(true)
    await act(async () => {
      pending.resolve({ data: { success: true, data: bundle } })
      await pending.promise
    })
    expect(useAuthStore.getState().auth.user?.id).toBe(
      change === 'account switch' ? 99 : undefined
    )
    expect(useAuthStore.getState().auth.pendingLoginVerification).toBeNull()
  }
)

it('requires a new sign-in when the verification page has no in-memory challenge', async () => {
  const post = vi.spyOn(api, 'post')
  const { router } = renderLoginVerification(false)
  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
  expect(post).not.toHaveBeenCalled()
})

it('rejects submission after the five-minute challenge deadline without sending a request', async () => {
  const post = vi.spyOn(api, 'post')
  renderLoginVerification()
  const user = userEvent.setup()
  await user.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  const later = Date.now() + 301000
  vi.spyOn(Date, 'now').mockReturnValue(later)
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Login flow expired. Please sign in again.'
  )
  expect(post).not.toHaveBeenCalled()
  expect(useAuthStore.getState().auth.user).toBeNull()
})
