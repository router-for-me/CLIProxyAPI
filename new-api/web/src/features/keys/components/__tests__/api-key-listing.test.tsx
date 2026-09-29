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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  act,
  cleanup,
  render,
  screen,
  within,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { Toaster, toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { apiKeySchema, type ApiKey } from '../../types'
import { ApiKeyQuotaCell } from '../api-key-quota-cell'
import { useApiKeysColumns } from '../api-keys-columns'
import { ApiKeysProvider } from '../api-keys-provider'
import { ApiKeysTable } from '../api-keys-table'

const now = 1_700_000_000_000
const key = apiKeySchema.parse({
  id: 7,
  name: 'production',
  key: 'demo********1234',
  status: 1,
  remain_quota: 40_000_000,
  used_quota: 60_000_000,
  unlimited_quota: false,
  expired_time: -1,
  created_time: 0,
  accessed_time: 0,
  group: 'default',
  model_limits_enabled: false,
})
const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} } },
  initAsync: false,
})
const clients: QueryClient[] = []

function QuotaTable(props: { apiKey: ApiKey }) {
  const columns = useApiKeysColumns(now).filter(
    (column) => column.id === 'quota'
  )
  const table = useReactTable({
    columns,
    data: [props.apiKey],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <table>
      <thead>
        {table.getHeaderGroups().map((group) => (
          <tr key={group.id}>
            {group.headers.map((header) => (
              <th key={header.id}>
                {flexRender(
                  header.column.columnDef.header,
                  header.getContext()
                )}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id}>
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function renderQuota(apiKey: ApiKey = key) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, enabled: false } },
  })
  clients.push(client)
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <QuotaTable apiKey={apiKey} />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: {} } })
})
afterEach(() => {
  cleanup()
  toast.dismiss()
  localStorage.clear()
  clients.splice(0).forEach((client) => client.clear())
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

it('shows desktop remaining and used amounts side by side without labels, with the currency only in the header', () => {
  renderQuota()
  expect(
    screen.getByRole('columnheader', { name: 'Quota ($)' })
  ).toBeInTheDocument()
  const trigger = screen.getByRole('button', {
    name: /Remaining 80; Remaining percentage 40%; Used amount 120/,
  })
  expect(trigger).toHaveTextContent('80120')
  expect(trigger).not.toHaveTextContent(/Remaining|Used amount/)
  expect(
    trigger.querySelector('[data-slot="api-key-quota-values"]')
  ).toHaveClass('grid-cols-2')
  expect(within(trigger).getByText('80')).toHaveClass('text-left')
  expect(within(trigger).getByText('120')).toHaveClass('text-right')
  expect(trigger.parentElement).toHaveClass('max-w-45')
  expect(trigger).not.toHaveTextContent('$')
  expect(trigger.querySelector('svg')).toBeNull()
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '40')
})

it.each([
  ['unused', 500000, 0, 100, 'text-emerald-500'],
  ['low remaining', 150000, 350000, 30, 'text-amber-500'],
  ['critical remaining', 50000, 450000, 10, 'text-rose-500'],
  ['exhausted', 0, 500000, 0, null],
  ['overdrawn', -50000, 500000, 0, null],
  ['zero total', 0, 0, 0, null],
  ['negative total', -500000, 100000, 0, null],
])(
  'renders the %s progress without invalid values or hiding negative balances',
  (_label, remaining, used, percentage, color) => {
    renderQuota({ ...key, remain_quota: remaining, used_quota: used })
    const button = screen.getByRole('button')
    const progress = screen.getByRole('progressbar')
    expect(progress).toHaveAttribute('aria-valuenow', String(percentage))
    if (color) expect(progress).toHaveClass(color)
    if (remaining < 0) {
      expect(
        within(button).getByText(remaining === -500000 ? '-1' : '-0.1')
      ).toHaveClass('text-destructive')
    }
  }
)

it('shows unlimited with cumulative usage and explains it on demand', async () => {
  renderQuota({ ...key, unlimited_quota: true })
  const button = screen.getByRole('button', { name: /Unlimited/ })
  expect(button).toHaveTextContent('Unlimited')
  expect(button).toHaveTextContent('Unlimited120')
  expect(button).not.toHaveTextContent(/Remaining|Used amount/)
  expect(within(button).getByText('Unlimited')).toHaveClass('text-left')
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  await userEvent.click(button)
  const detail = await screen.findByRole('dialog')
  expect(within(detail).getByText('120')).toBeInTheDocument()
  expect(detail).toHaveTextContent(
    'This API key has no quota limit. Requests still require available wallet or subscription quota.'
  )
})

it('keeps small custom-currency amounts exact and shows full values in the detail', async () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CUSTOM',
      customCurrencySymbol: '🐱',
    },
  })
  renderQuota({ ...key, remain_quota: 1900, used_quota: 1100 })
  expect(
    screen.getByRole('columnheader', { name: 'Quota (🐱)' })
  ).toBeInTheDocument()
  const button = screen.getByRole('button')
  expect(button).toHaveTextContent('0.0038')
  expect(button).not.toHaveTextContent('🐱')
  await userEvent.click(button)
  const detail = await screen.findByRole('dialog')
  expect(within(detail).getByText('0.0022')).toBeInTheDocument()
  expect(within(detail).getByText('0.006')).toBeInTheDocument()
})

it.each([
  ['disabled', { status: 2 }],
  ['expired status', { status: 3 }],
  ['exhausted status', { status: 4 }],
  ['expired timestamp', { expired_time: now / 1000 - 1 }],
])('renders the %s progress bar in a neutral color', (_label, overrides) => {
  renderQuota({ ...key, ...overrides })
  expect(screen.getByRole('progressbar')).toHaveClass(
    'text-muted-foreground/60'
  )
})

it('recalculates the progress when remaining quota is edited', () => {
  const { rerender } = render(
    <I18nextProvider i18n={i18n}>
      <ApiKeyQuotaCell apiKey={key} now={now} />
    </I18nextProvider>
  )
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '40')
  rerender(
    <I18nextProvider i18n={i18n}>
      <ApiKeyQuotaCell
        apiKey={{ ...key, remain_quota: 90_000_000 }}
        now={now}
      />
    </I18nextProvider>
  )
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '60')
  expect(screen.getByText('180')).toBeInTheDocument()
})

it('opens details with the keyboard and restores focus when Escape closes them', async () => {
  renderQuota()
  const user = userEvent.setup()
  const button = screen.getByRole('button')
  act(() => button.focus())
  await user.keyboard('{Enter}')
  const detail = await screen.findByRole('dialog')
  expect(within(detail).getByText('80')).toBeInTheDocument()
  expect(within(detail).getByText('120')).toBeInTheDocument()
  expect(within(detail).getByText('200')).toBeInTheDocument()
  expect(within(detail).getByText('Remaining percentage')).toBeInTheDocument()
  expect(within(detail).getByText('40%')).toBeInTheDocument()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(button).toHaveFocus()
})

it('keeps a long amount within its column while showing the full amount in details', async () => {
  renderQuota({ ...key, remain_quota: 123456789000000, used_quota: 0 })
  const button = screen.getByRole('button')
  expect(button).toHaveClass('w-full', 'min-w-0')
  expect(within(button).getByText('246,913,578')).toHaveClass('truncate')
  await userEvent.click(button)
  expect(
    within(await screen.findByRole('dialog')).getAllByText('246,913,578')
  ).toHaveLength(2)
})

function KeysPage() {
  return (
    <ApiKeysProvider>
      <ApiKeysTable />
      <Toaster />
    </ApiKeysProvider>
  )
}

async function renderKeysPage(status = 1, overrides: Partial<ApiKey> = {}) {
  let currentKey = { ...key, status, ...overrides }
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url.startsWith('/api/token/')) {
      return {
        data: { success: true, data: { items: [currentKey], total: 1 } },
      }
    }
    return { data: { success: true, data: { default: { ratio: 1 } } } }
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { key: 'fake-key-for-test-only' } },
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, data) => {
    const update = data as { id: number; status: number }
    currentKey = { ...currentKey, status: update.status }
    return { data: { success: true, data: currentKey } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], {})
  clients.push(client)
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const keysRoute = createRoute({
    getParentRoute: () => auth,
    path: 'keys/',
    component: KeysPage,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([keysRoute])]),
    history: createMemoryHistory({ initialEntries: ['/keys/'] }),
  })
  await router.load()
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  await screen.findByText(currentKey.name)
  return { post, put }
}

it('combines creation and last use while keeping expiry, models and IP restrictions separate', async () => {
  await renderKeysPage()
  for (const name of ['Name', 'API Key', 'Group', 'Models', 'IP Restriction']) {
    expect(screen.getByRole('columnheader', { name })).toBeInTheDocument()
  }
  expect(screen.getByRole('columnheader', { name: 'Time' })).toBeInTheDocument()
  expect(
    screen.getByRole('columnheader', { name: 'Expires' })
  ).toBeInTheDocument()
  const timeCell = screen.getByRole('cell', { name: /Created.*Last Used/ })
  expect(within(timeCell).getByText('Last Used')).toBeInTheDocument()
  const quotaHeader = screen.getByRole('columnheader', { name: 'Quota ($)' })
  const quotaTrigger = screen.getByRole('button', {
    name: /Remaining 80; Remaining percentage 40%; Used amount 120/,
  })
  expect(quotaHeader).not.toHaveClass('pr-8')
  expect(quotaTrigger.closest('td')).not.toHaveClass('pr-8')
})

it('restores dates hidden by the old default and preserves unrelated column preferences', async () => {
  localStorage.setItem(
    'api-keys:column-visibility',
    JSON.stringify({
      created_time: false,
      accessed_time: false,
      expired_time: false,
      model_limits: false,
    })
  )
  await renderKeysPage()
  expect(screen.getByRole('columnheader', { name: 'Time' })).toBeInTheDocument()
  expect(
    screen.getByRole('columnheader', { name: 'Expires' })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('columnheader', { name: 'Models' })
  ).not.toBeInTheDocument()
})

it.each([
  [1, 'Disable', 2, 'Disabled'],
  [2, 'Enable', 1, 'Enabled'],
])(
  'keeps status %s toggling at its original row button without fetching a full key',
  async (status, action, nextStatus, nextLabel) => {
    const { post, put } = await renderKeysPage(status)
    const user = userEvent.setup()
    const button = screen.getByRole('button', { name: action })
    act(() => button.focus())
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/token/?status_only=true', {
        id: 7,
        status: nextStatus,
      })
    )
    await screen.findByText(nextLabel)
    expect(post).not.toHaveBeenCalled()
  }
)

it('keeps expired status when the server refuses reactivation', async () => {
  const { put, post } = await renderKeysPage(3)
  put.mockResolvedValue({ data: { success: false, message: 'Token expired' } })
  await userEvent.click(screen.getByRole('button', { name: 'Enable' }))
  await screen.findByText('Token expired')
  expect(screen.getByText('Expired')).toBeInTheDocument()
  expect(screen.queryByText('Enabled')).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

it.each([true, false])(
  'fetches a full key only on explicit copy and honors permission success=%s',
  async (success) => {
    const user = userEvent.setup()
    const { post } = await renderKeysPage()
    post.mockResolvedValue(
      success
        ? { data: { success: true, data: { key: 'fake-key-for-test-only' } } }
        : { data: { success: false, message: 'Verification required' } }
    )
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    await user.click(screen.getByRole('button', { name: 'Open menu' }))
    expect(post).not.toHaveBeenCalled()
    await user.click(screen.getByRole('menuitem', { name: 'Copy Key' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/token/7/key'))
    if (success) {
      await waitFor(() =>
        expect(copy).toHaveBeenCalledWith('sk-fake-key-for-test-only')
      )
    } else {
      await screen.findByText('Verification required')
      expect(copy).not.toHaveBeenCalled()
    }
  }
)

it('keeps full mobile information without group or quota section headings', async () => {
  const matchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...matchMedia(query),
    matches: query.includes('max-width'),
  }))
  i18n.addResourceBundle('zh', 'translation', zh.translation)
  await i18n.changeLanguage('zh')
  try {
    await renderKeysPage()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.queryByText('额度 ($)')).not.toBeInTheDocument()
    expect(screen.getByText('($)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /剩余 80;/ })).toBeInTheDocument()
    expect(screen.getByText('80')).toBeInTheDocument()
    expect(screen.getByText('120')).toBeInTheDocument()
    expect(screen.getByText(zh.translation['Created'])).toBeInTheDocument()
    expect(screen.getByText(zh.translation['Last Used'])).toBeInTheDocument()
    expect(screen.getByText(zh.translation['Expires'])).toBeInTheDocument()
    expect(
      screen.queryByText(zh.translation['Group'], { exact: true })
    ).not.toBeInTheDocument()
    expect(screen.getByText('default')).toBeInTheDocument()
    expect(screen.getByText('1x')).toBeInTheDocument()
    expect(screen.getByText(zh.translation['Models'])).toBeInTheDocument()
    expect(
      screen.getByText(zh.translation['IP Restriction'])
    ).toBeInTheDocument()
  } finally {
    await i18n.changeLanguage('en')
  }
})

it('keeps mobile quota readable and opens complete model and IP restrictions by tapping', async () => {
  const matchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...matchMedia(query),
    matches: query.includes('max-width'),
  }))
  await renderKeysPage(1, {
    name: 'production-with-a-long-key-name',
    used_quota: 2245080000,
    unlimited_quota: true,
    model_limits_enabled: true,
    model_limits: 'model-alpha,model-beta-with-a-long-name',
    allow_ips: '192.0.2.1\n2001:db8::1',
  })
  const quota = screen.getByRole('button', {
    name: /Unlimited; Used amount 4,490.16/,
  })
  expect(quota).toHaveTextContent('Remaining($)UnlimitedUsed amount4,490.16')
  expect(quota.parentElement).toHaveClass('w-full')
  expect(quota.parentElement).not.toHaveClass('max-w-45')
  expect(quota.querySelector('[data-slot="api-key-quota-values"]')).toHaveClass(
    'grid-cols-[auto_minmax(0,1fr)]'
  )
  expect(within(quota).getByText('Unlimited')).toHaveClass(
    'text-right',
    'text-sm',
    'font-normal'
  )
  expect(within(quota).getByText('4,490.16')).toHaveClass(
    'tabular-nums',
    'text-sm',
    'font-normal',
    'text-right'
  )
  await userEvent.click(screen.getByRole('button', { name: /Models: 2 model/ }))
  let details = await screen.findByRole('dialog')
  expect(within(details).getByText('model-alpha')).toBeVisible()
  expect(within(details).getByText('model-beta-with-a-long-name')).toBeVisible()
  await userEvent.keyboard('{Escape}')
  await userEvent.click(
    screen.getByRole('button', { name: /IP Restriction: 2 IP/ })
  )
  details = await screen.findByRole('dialog')
  expect(within(details).getByText('192.0.2.1')).toBeVisible()
  expect(within(details).getByText('2001:db8::1')).toBeVisible()
})
