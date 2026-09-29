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
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, type AxiosResponse } from 'axios'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { AuditLogs } from '..'
import { AuditLogViewer } from '../components/audit-log-viewer'

it.each([
  [
    'generic',
    {
      action: 'add_quota',
      target_user_id: 11,
      mode: 'unsupported',
      requested_quota: 500000,
      failure_reason: 'invalid_parameters',
    },
    'Adjust user quota',
    'Requested quota: $1 · Invalid adjustment parameters',
  ],
  [
    'user.quota_add',
    {
      target_user_id: 11,
      target_username: 'quota-owner',
      requested_quota: 500000,
      quota: 500000,
      from: 500000,
      to: 1000000,
    },
    'Increase quota for user “quota-owner”',
    'Requested quota: $1 · $1 → $2',
  ],
  [
    'user.quota_subtract',
    {
      target_user_id: 11,
      target_username: 'quota-owner',
      requested_quota: 500000,
      quota: 500000,
      from: 1000000,
      to: 500000,
    },
    'Decrease quota for user “quota-owner”',
    'Requested quota: $1 · $2 → $1',
  ],
  [
    'user.quota_override',
    {
      target_user_id: 11,
      target_username: 'quota-owner',
      requested_quota: 0,
      from: 500000,
      to: 0,
    },
    'Override quota for user “quota-owner”',
    'Requested quota: $0 · $1 → $0',
  ],
  ['token.create', { id: 11, name: '1' }, 'Create API token “1”', ''],
  [
    'token.update',
    {
      id: 11,
      name: 'production',
      changed_fields: ['remain_quota', 'expired_time'],
    },
    'Update API token “production”',
    'Changed fields: Remaining quota, Expiration Time',
  ],
  [
    'token.status_update',
    { id: 11, name: 'production', from: 1, to: 2 },
    'Update API token “production”',
    'Enabled → Disabled',
  ],
  [
    'token.delete',
    { id: 11, name: 'production' },
    'Delete API token “production”',
    '',
  ],
  [
    'token.key_view',
    { id: 11, name: 'production' },
    'View key for API token “production”',
    '',
  ],
  [
    'token.delete_batch',
    { total: 4, count: 1, requested_ids: [11, 11, 12, 99] },
    'Batch delete API tokens',
    'Requested: 4 · Deleted: 1',
  ],
  [
    'token.key_view_batch',
    { total: 4, count: 0, returned_ids: [] },
    'View API token keys in batch',
    'Requested: 4 · Returned: 0',
  ],
])(
  'shows the target and business outcome for %s directly in the event cell',
  async (action, params, headline, outcome) => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          total: 1,
          items: [
            {
              event_id: 'token-event',
              created_at: 1788600600,
              username: 'root',
              actor_role: 100,
              category: 'security',
              action,
              success: action !== 'generic',
              status: 200,
              other: { op: { action, params } },
            },
          ],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <AuditLogViewer scope='self' />
      </QueryClientProvider>
    )
    const cell = await screen.findByRole('cell', { name: new RegExp(headline) })
    expect(cell).toHaveTextContent(headline)
    if ('id' in params || 'target_user_id' in params) {
      expect(cell).toHaveTextContent('(ID: 11)')
    }
    if (outcome) expect(cell).toHaveTextContent(outcome)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  }
)

it.each([false, true])(
  'keeps the ID outside long-name truncation and shows complete details (mobile=%s)',
  async (mobile) => {
    const matchMedia = window.matchMedia.bind(window)
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...matchMedia(query),
      matches: mobile && query === '(max-width: 640px)',
    }))
    const name = 'production-europe-primary-customer-routing-token'
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          total: 1,
          items: [
            {
              event_id: 'long-token-name',
              created_at: 1788600600,
              username: 'root',
              actor_role: 100,
              category: 'security',
              action: 'token.status_update',
              success: true,
              status: 200,
              other: {
                op: {
                  action: 'token.status_update',
                  params: { id: 11, name, from: 1, to: 2 },
                },
              },
            },
          ],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <AuditLogViewer scope='self' />
      </QueryClientProvider>
    )
    const id = await screen.findByText('(ID: 11)')
    expect(id).toBeVisible()
    expect(id).toHaveClass('shrink-0')
    expect(id.closest('.truncate')).toBeNull()
    expect(screen.getByText('Enabled → Disabled')).toBeVisible()
    if (mobile) expect(screen.queryByRole('table')).not.toBeInTheDocument()
    else expect(screen.getByRole('table')).toBeVisible()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Details' }))
    const dialog = await screen.findByRole('dialog', { name: 'Log Details' })
    expect(
      within(dialog).getByText(`Update API token “${name}” (ID: 11)`)
    ).toBeVisible()
    expect(
      within(dialog).getByText('Token Name').parentElement
    ).toHaveTextContent(name)
  }
)

it('uses the shared log toolbar and opens details in a keyboard-accessible dialog without expanding the row', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            event_id: 'request-1',
            created_at: 1788600600,
            username: 'alice',
            category: 'access_token',
            action: 'access_token.request',
            content: '',
            other: {},
            request_id: 'audit-request-42',
            token_ref: 'a'.repeat(64),
            user_agent: 'Browser client with a long version description',
            method: 'GET',
            route: '/api/user/self',
            ip: '127.0.0.1',
            status: 200,
            success: true,
          },
        ],
      },
    },
  })
  renderViewer()
  expect(screen.getByRole('button', { name: 'Date Range' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Search' })).toBeVisible()
  expect(
    await screen.findByRole('button', { name: 'Go to next page' })
  ).toBeDisabled()
  const trigger = await screen.findByRole('button', { name: 'Details' })
  const row = screen.getByRole('row', { name: /127.0.0.1/ })
  const clientCell = within(row).getByRole('cell', {
    name: 'Browser client with a long version description',
  })
  expect(clientCell.querySelector('.truncate')).toBeInTheDocument()
  expect(row.querySelector('details')).not.toBeInTheDocument()
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  const dialog = await screen.findByRole('dialog', { name: 'Log Details' })
  expect(within(dialog).getByText('audit-request-42')).toBeVisible()
  expect(
    within(dialog).getByText('Browser client with a long version description')
  ).toBeVisible()
  await userEvent.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await waitFor(() => expect(trigger).toHaveFocus())
})

it.each([
  [
    'en',
    en,
    'Updated channel status (ID: 42)',
    'Batch updated channel status (1/2 changed)',
  ],
  ['zh', zh, '更新渠道状态（ID: 42）', '批量更新渠道状态（1/2 个已变更）'],
  ['zh-TW', zhTW, '更新渠道狀態（ID: 42）', '批次更新渠道狀態（1/2 個已變更）'],
  [
    'fr',
    fr,
    'Statut du canal mis à jour (ID : 42)',
    'Statuts des canaux mis à jour par lot (1/2 modifiés)',
  ],
  [
    'ja',
    ja,
    'チャネルの状態を更新（ID: 42）',
    'チャネルの状態を一括更新（1/2 件変更）',
  ],
  [
    'ru',
    ru,
    'Обновлён статус канала (ID: 42)',
    'Массовое обновление статусов каналов (изменено: 1/2)',
  ],
  [
    'vi',
    viLocale,
    'Đã cập nhật trạng thái kênh (ID: 42)',
    'Đã cập nhật trạng thái kênh hàng loạt (1/2 đã thay đổi)',
  ],
] as const)(
  'renders existing channel status audit records in %s',
  async (locale, resources, single, batch) => {
    const i18n = createInstance()
    await i18n.init({
      lng: locale,
      fallbackLng: false,
      resources: { [locale]: resources },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          total: 2,
          items: [
            {
              event_id: 'single-status',
              created_at: 0,
              username: 'alice',
              action: 'channel.status_update',
              content: 'channel.status_update',
              other: {
                op: {
                  action: 'channel.status_update',
                  params: { id: 42, status: 2, changed: true },
                },
              },
            },
            {
              event_id: 'batch-status',
              created_at: 0,
              username: 'alice',
              action: 'channel.status_update_batch',
              content: 'channel.status_update_batch',
              other: {
                op: {
                  action: 'channel.status_update_batch',
                  params: { count: 1, total: 2, status: 1 },
                },
              },
            },
          ],
        },
      },
    })
    render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>
          <AuditLogViewer scope='self' />
        </QueryClientProvider>
      </I18nextProvider>
    )
    expect(await screen.findByRole('cell', { name: single })).toBeVisible()
    expect(screen.getByRole('cell', { name: batch })).toBeVisible()
    expect(
      screen.queryByRole('cell', { name: 'channel.status_update' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('cell', { name: 'channel.status_update_batch' })
    ).not.toBeInTheDocument()
  }
)

beforeEach(() => {
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => undefined,
    removeItem: () => undefined,
  })
})
afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
function renderViewer(scope: 'all' | 'self' = 'self') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <AuditLogViewer
        scope={scope}
        accessOnly
        currentTokenRef={'a'.repeat(64)}
      />
    </QueryClientProvider>
  )
}

it('filters own access history by result, generation and time and resets pagination', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 45 } },
  })
  renderViewer()
  const user = userEvent.setup()
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeEnabled()
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
      params: expect.objectContaining({ p: 2, category: 'access_token' }),
    })
  )
  await user.click(screen.getByRole('combobox', { name: 'Result' }))
  await user.click(await screen.findByRole('option', { name: 'Failed' }))
  await user.click(screen.getByRole('combobox', { name: 'Token scope' }))
  await user.click(
    await screen.findByRole('option', { name: 'Historical tokens' })
  )
  await user.click(screen.getByRole('button', { name: 'Date Range' }))
  fireEvent.change(screen.getByLabelText('Start Time'), {
    target: { value: '2026-09-01T12:00' },
  })
  await user.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
      params: expect.objectContaining({
        p: 1,
        success: 'false',
        exclude_token_ref: 'a'.repeat(64),
        start_timestamp: expect.any(Number),
      }),
    })
  )
  await user.click(screen.getByRole('combobox', { name: 'Token scope' }))
  await user.click(await screen.findByRole('option', { name: 'Current token' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
      params: expect.objectContaining({ token_ref: 'a'.repeat(64) }),
    })
  )
  expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
  expect(screen.getByRole('table').style.minWidth).toMatch(/max\(100%, \d+px\)/)
})

it('a failed history query exposes retry and no empty history claim', async () => {
  vi.spyOn(api, 'get')
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
  renderViewer()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Failed to load audit records'
  )
  expect(screen.queryByText('No records')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No records')).toBeVisible()
})

it('administrator scope uses the admin endpoint and exposes the username filter', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 0 } },
  })
  renderViewer('all')
  await userEvent.click(screen.getByRole('button', { name: 'Expand' }))
  fireEvent.change(screen.getByLabelText('Username'), {
    target: { value: 'alice' },
  })
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit', {
      params: expect.objectContaining({ username: 'alice' }),
    })
  )
})

it('changing rows per page resets pagination and sends the selected page size', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 80 } },
  })
  renderViewer()
  const user = userEvent.setup()
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeEnabled()
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
      params: expect.objectContaining({ p: 2 }),
    })
  )
  await user.click(screen.getByRole('combobox', { name: '' }))
  await user.click(await screen.findByRole('option', { name: '50' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
      params: expect.objectContaining({ p: 1, page_size: 50 }),
    })
  )
})

it.each([10, 100])(
  'role %i defaults to all audit records and can switch both ways',
  async (role) => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'admin',
      role,
      permissions: { admin_permissions: { audit: { read: role === 10 } } },
    })
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <AuditLogs />
      </QueryClientProvider>
    )
    expect(screen.getByRole('tab', { name: 'All' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/audit', expect.anything())
    )
    expect(get).not.toHaveBeenCalledWith('/api/audit/self', expect.anything())
    await userEvent.click(screen.getByRole('tab', { name: 'Only Mine' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith('/api/audit/self', expect.anything())
    )
    expect(screen.getByRole('tab', { name: 'Only Mine' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await userEvent.click(screen.getByRole('tab', { name: 'All' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith('/api/audit', expect.anything())
    )
    expect(screen.getByRole('tab', { name: 'All' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  }
)

it.each([1, 10])(
  'role %i without an audit grant queries only its own records without scope tabs',
  async (role) => {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'alice', role })
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <AuditLogs />
      </QueryClientProvider>
    )
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/audit/self', expect.anything())
    )
    expect(get).not.toHaveBeenCalledWith('/api/audit', expect.anything())
    expect(
      screen.queryByRole('tablist', { name: 'View scope' })
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'All' })).not.toBeInTheDocument()
  }
)

it('clears global records and open details on revocation, falls back to self, and refreshes permissions', async () => {
  const admin = {
    id: 1,
    username: 'admin',
    role: 10,
    permissions: { admin_permissions: { audit: { read: true } } },
  }
  useAuthStore.getState().auth.setUser(admin)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const forbidden = new AxiosError(
    'Forbidden',
    'ERR_BAD_REQUEST',
    undefined,
    undefined,
    { status: 403 } as AxiosResponse
  )
  let revoked = false
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/self') {
      return {
        data: {
          success: true,
          data: {
            ...admin,
            permissions: { admin_permissions: { audit: { read: false } } },
          },
        },
      }
    }
    if (url === '/api/audit' && revoked) throw forbidden
    return {
      data: {
        success: true,
        data: {
          total: url === '/api/audit' ? 1 : 0,
          items:
            url === '/api/audit'
              ? [
                  {
                    event_id: 'private-event',
                    user_id: 2,
                    username: 'other-account',
                    actor_role: 10,
                    created_at: 1788600600,
                    category: 'operation',
                    action: 'channel.update',
                    content: 'Private operation',
                    other: {},
                    request_id: 'private-request',
                    token_ref: '',
                    user_agent: '',
                    method: 'PUT',
                    route: '/api/channel/',
                    ip: '',
                    status: 200,
                    success: true,
                  },
                ]
              : [],
        },
      },
    }
  })
  client.setQueryData(['audit', 99, 'all'], { items: ['separate-session'] })
  render(
    <QueryClientProvider client={client}>
      <AuditLogs />
    </QueryClientProvider>
  )
  await screen.findByText('other-account')
  await userEvent.click(screen.getByRole('button', { name: 'Details' }))
  expect(
    await screen.findByRole('dialog', { name: 'Log Details' })
  ).toBeVisible()
  revoked = true
  await client.invalidateQueries({ queryKey: ['audit', 1, 'all'] })
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith('/api/audit/self', expect.anything())
  )
  await waitFor(() => expect(get).toHaveBeenCalledWith('/api/user/self'))
  expect(screen.queryByText('other-account')).not.toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: 'All' })).not.toBeInTheDocument()
  expect(client.getQueriesData({ queryKey: ['audit', 1, 'all'] })).toHaveLength(
    0
  )
  expect(client.getQueryData(['audit', 99, 'all'])).toEqual({
    items: ['separate-session'],
  })
  expect(
    useAuthStore.getState().auth.user?.permissions?.admin_permissions?.audit
      .read
  ).toBe(false)
})

it('mobile access history keeps pagination visible and puts result filters in a drawer', async () => {
  const pointerCapture = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    'setPointerCapture'
  )
  // JSDOM does not implement the pointer-capture API used by the mobile drawer.
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: () => undefined,
  })
  try {
    const matchMedia = window.matchMedia.bind(window)
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...matchMedia(query),
      matches: query === '(max-width: 640px)',
    }))
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    renderViewer()
    const user = userEvent.setup()
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeVisible()
    expect(
      screen.queryByRole('combobox', { name: 'Result' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    const drawer = await screen.findByRole('dialog', { name: 'Filter' })
    await user.click(within(drawer).getByRole('combobox', { name: 'Result' }))
    await user.click(await screen.findByRole('option', { name: 'Failed' }))
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith('/api/audit/self', {
        params: expect.objectContaining({ success: 'false', p: 1 }),
      })
    )
    await user.click(within(drawer).getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeVisible()
  } finally {
    if (pointerCapture) {
      Object.defineProperty(
        HTMLElement.prototype,
        'setPointerCapture',
        pointerCapture
      )
    } else {
      Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
    }
  }
})
