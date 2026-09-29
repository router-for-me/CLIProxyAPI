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
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
  vi,
} from 'vitest'

import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { UserBindingDialog } from '../user-binding-dialog'

/**
 * The dialog reads `/api/status` through the shared React Query cache, so it
 * needs a provider. A fresh client per render keeps tests isolated.
 */
const queryClients: QueryClient[] = []

function createQueryClient(): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  queryClients.push(queryClient)
  return queryClient
}

function renderWithQueryClient(
  ui: React.ReactElement,
  queryClient = createQueryClient()
): ReturnType<typeof render> {
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
  )
}

type ApiMethod = (url: string) => Promise<{ data: unknown }>
type MockableApi = {
  get: ApiMethod
  delete: ApiMethod
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalDelete = apiClient.delete
const originalGetAnimations = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'getAnimations'
)

const user = {
  id: 7,
  username: 'bound-user',
  email: 'user@example.com',
  github_id: 'github-user',
  discord_id: 'discord-user',
  wechat_id: 'wechat-user',
  oidc_id: 'oidc-user',
  telegram_id: 'telegram-user',
  linux_do_id: 'linuxdo-user',
}

function findUnbindButton(provider: string): HTMLButtonElement {
  let container = screen.getByText(provider).parentElement
  while (container && !container.querySelector('button')) {
    container = container.parentElement
  }
  const button = container?.querySelector<HTMLButtonElement>('button')
  if (!button) {
    throw new Error(`Expected unbind button for ${provider}`)
  }
  return button
}

beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
})

afterAll(() => {
  if (originalGetAnimations) {
    Object.defineProperty(
      HTMLElement.prototype,
      'getAnimations',
      originalGetAnimations
    )
    return
  }
  Reflect.deleteProperty(HTMLElement.prototype, 'getAnimations')
})

beforeEach(() => {
  window.localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

afterEach(() => {
  cleanup()
  queryClients.splice(0).forEach((client) => client.clear())
  window.localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  apiClient.get = originalGet
  apiClient.delete = originalDelete
})

describe('UserBindingDialog built-in bindings', () => {
  test('submits every built-in provider type accepted by the backend', async () => {
    const deletedUrls: string[] = []
    apiClient.get = async (url) => {
      switch (url) {
        case '/api/user/7':
          return { data: { success: true, data: user } }
        case '/api/user/7/oauth/bindings':
          return { data: { success: true, data: [] } }
        case '/api/status':
          return {
            data: {
              success: true,
              data: {
                github_oauth: true,
                discord_oauth: true,
                wechat_login: true,
                oidc_enabled: true,
                telegram_oauth: true,
                linuxdo_oauth: true,
              },
            },
          }
        default:
          throw new Error(`Unexpected GET ${url}`)
      }
    }
    apiClient.delete = async (url) => {
      deletedUrls.push(url)
      return { data: { success: true, message: 'success' } }
    }

    renderWithQueryClient(
      <UserBindingDialog open userId={7} onOpenChange={() => undefined} />
    )

    const expectedBindings = [
      ['Email', 'email'],
      ['GitHub', 'github'],
      ['Discord', 'discord'],
      ['WeChat', 'wechat'],
      ['OIDC', 'oidc'],
      ['Telegram', 'telegram'],
      ['LinuxDO', 'linuxdo'],
    ] as const

    await screen.findByText('bound-user (ID: 7)')
    for (const [provider, bindingType] of expectedBindings) {
      fireEvent.click(findUnbindButton(provider))
      fireEvent.click(screen.getByRole('button', { name: 'Confirm Unbind' }))
      await waitFor(() => {
        expect(deletedUrls.at(-1)).toBe(`/api/user/7/bindings/${bindingType}`)
      })
      await waitFor(() => {
        expect(
          screen.queryByRole('button', { name: 'Confirm Unbind' })
        ).not.toBeInTheDocument()
      })
    }

    expect(deletedUrls).toHaveLength(expectedBindings.length)
  })
})

describe('UserBindingDialog shared status updates', () => {
  test('updates provider availability and the provider list after a background refresh', async () => {
    const queryClient = createQueryClient()
    queryClient.setQueryData(
      STATUS_QUERY_KEY,
      { github_oauth: false },
      { updatedAt: Date.now() - 600_000 }
    )
    let resolveStatus!: (value: { data: unknown }) => void
    const response = new Promise<{ data: unknown }>((resolve) => {
      resolveStatus = resolve
    })
    apiClient.get = async (url) => {
      if (url === '/api/status') return response
      if (url === '/api/user/7') return { data: { success: true, data: user } }
      if (url === '/api/user/7/oauth/bindings') {
        return { data: { success: true, data: [] } }
      }
      throw new Error(`Unexpected GET ${url}`)
    }
    renderWithQueryClient(
      <UserBindingDialog open userId={7} onOpenChange={() => undefined} />,
      queryClient
    )
    await screen.findByText('bound-user (ID: 7)')
    expect(screen.getByText('GitHub').parentElement).toHaveTextContent(
      'Disabled'
    )
    fireEvent.click(screen.getByRole('button', { name: 'Show All' }))
    await act(async () => {
      resolveStatus({
        data: {
          success: true,
          data: {
            github_oauth: true,
            custom_oauth_providers: [{ id: 9, name: 'New provider' }],
          },
        },
      })
    })
    await screen.findByText('New provider')
    expect(screen.getByText('GitHub').parentElement).not.toHaveTextContent(
      'Disabled'
    )
  })

  test.each(['success', 'failure'] as const)(
    'waits for the initial status request and shows user data on %s',
    async (outcome) => {
      let resolveStatus!: (value: { data: unknown }) => void
      let rejectStatus!: (error: Error) => void
      const response = new Promise<{ data: unknown }>((resolve, reject) => {
        resolveStatus = resolve
        rejectStatus = reject
      })
      const get = vi.fn(async (url: string) => {
        if (url === '/api/status') return response
        if (url === '/api/user/7') {
          return { data: { success: true, data: user } }
        }
        if (url === '/api/user/7/oauth/bindings') {
          return { data: { success: true, data: [] } }
        }
        throw new Error(`Unexpected GET ${url}`)
      })
      apiClient.get = get
      renderWithQueryClient(
        <UserBindingDialog open userId={7} onOpenChange={() => undefined} />
      )
      await waitFor(() => expect(get).toHaveBeenCalledWith('/api/status'))
      expect(screen.queryByText('bound-user (ID: 7)')).not.toBeInTheDocument()
      await act(async () => {
        if (outcome === 'success') {
          resolveStatus({
            data: { success: true, data: { github_oauth: true } },
          })
        } else rejectStatus(new Error('Status unavailable'))
      })
      await screen.findByText('bound-user (ID: 7)')
      expect(screen.getByText('github-user')).toBeInTheDocument()
    }
  )

  test('retains cached provider state when background refresh fails', async () => {
    const queryClient = createQueryClient()
    queryClient.setQueryData(
      STATUS_QUERY_KEY,
      { github_oauth: true },
      { updatedAt: Date.now() - 600_000 }
    )
    apiClient.get = async (url) => {
      if (url === '/api/status') throw new Error('Status unavailable')
      if (url === '/api/user/7') return { data: { success: true, data: user } }
      if (url === '/api/user/7/oauth/bindings') {
        return { data: { success: true, data: [] } }
      }
      throw new Error(`Unexpected GET ${url}`)
    }
    renderWithQueryClient(
      <UserBindingDialog open userId={7} onOpenChange={() => undefined} />,
      queryClient
    )
    await screen.findByText('bound-user (ID: 7)')
    await waitFor(() =>
      expect(queryClient.getQueryState(STATUS_QUERY_KEY)?.status).toBe('error')
    )
    expect(screen.getByText('GitHub').parentElement).not.toHaveTextContent(
      'Disabled'
    )
  })

  test.each([
    { open: false, userId: 7 },
    { open: true, userId: null },
  ])(
    'does not request status when open=$open and userId=$userId',
    async (props) => {
      const get = vi.fn(async () => {
        throw new Error('Unexpected request')
      })
      apiClient.get = get
      await act(async () => {
        renderWithQueryClient(
          <UserBindingDialog {...props} onOpenChange={() => undefined} />
        )
      })
      expect(get).not.toHaveBeenCalled()
    }
  )
})
