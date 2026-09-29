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
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from '@tanstack/react-router'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  render,
  screen,
  within,
  cleanup,
  waitFor,
  act,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import type {
  ModelPricingConfig,
  ModelPricingEntry,
} from '@/features/model-pricing/api'
import { pricingOptions } from '@/features/model-pricing/pricing'
import { usePricingColumns } from '@/features/pricing/components/pricing-columns'
import type { PricingModel } from '@/features/pricing/types'
import fr from '@/i18n/locales/fr.json'
import zhCN from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ModelsDialogs } from '../components/models-dialogs'
import { ModelsProvider } from '../components/models-provider'
import { ModelsTable } from '../components/models-table'
import type { Model } from '../types'

const metadata: Model = {
  id: 7,
  model_name: 'catalog-only',
  square_state: 'unavailable',
  has_metadata: true,
  configured_channel_count: 0,
  name_rule: 0,
  status: 1,
  sync_official: 1,
  created_time: 1,
  updated_time: 1,
}
const channel: Model = {
  ...metadata,
  id: 0,
  model_name: 'channel-only',
  has_metadata: false,
  configured_channel_count: 1,
  status: 0,
  sync_official: 0,
}
const clients: QueryClient[] = []

function Page() {
  return (
    <ModelsProvider>
      <ModelsTable />
      <ModelsDialogs />
    </ModelsProvider>
  )
}

function CatalogPrice(props: { model: PricingModel }) {
  const columns = usePricingColumns({ tokenUnit: 'M' })
  const table = useReactTable({
    data: [props.model],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  const cell = table
    .getRowModel()
    .rows[0].getAllCells()
    .find((item) => item.column.id === 'price')
  return (
    <div role='group' aria-label='Catalog price'>
      {cell && flexRender(cell.column.columnDef.cell, cell.getContext())}
    </div>
  )
}

async function renderList(
  items: Model[] = [
    metadata,
    channel,
    { ...channel, model_name: 'other-channel' },
  ],
  options: {
    pricing?: ModelPricingEntry[] | Promise<ModelPricingConfig>
    waitForPricing?: boolean
    initialUrl?: string
    total?: number
  } = {}
) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/models/' || url === '/api/models/search') {
      return {
        data: {
          success: true,
          data: { items, total: options.total ?? items.length },
        },
      }
    }
    if (url === '/api/models/7') {
      return { data: { success: true, data: metadata } }
    }
    if (url === '/api/option/model_pricing') {
      return {
        data: {
          success: true,
          data:
            options.pricing && !Array.isArray(options.pricing)
              ? await options.pricing
              : {
                  entries: options.pricing ?? [],
                  options: pricingOptions({}),
                  empty_version: 'empty',
                },
        },
      }
    }
    return { data: { success: true, data: { items: [] } } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
  const models = createRoute({
    getParentRoute: () => authenticated,
    path: 'models/$section',
    component: Page,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([models])]),
    history: createMemoryHistory({
      initialEntries: [options.initialUrl ?? '/models/metadata'],
    }),
  })
  await router.load()
  const result = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  if (items.length) {
    await screen.findByRole('button', { name: items[0].model_name })
  } else await screen.findByText('No Models Found')
  if (options.waitForPricing !== false) {
    await waitFor(() => expect(client.isFetching()).toBe(0))
  }
  return { ...result, get, router }
}

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'USD' },
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  i18n.addResourceBundle('fr', 'translation', fr.translation, true, true)
  i18n.addResourceBundle('zhCN', 'translation', zhCN.translation, true, true)
})

afterEach(async () => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  await i18n.changeLanguage('en')
})

it('requests channel models and distinguishes catalog policy from availability using compact labels', async () => {
  const { get } = await renderList()
  expect(get).toHaveBeenCalledWith('/api/models/', {
    params: expect.objectContaining({ include_channel_models: true }),
  })
  expect(screen.getAllByText('Unavailable')).toHaveLength(3)
  const warning = screen
    .getAllByText('Unavailable')[0]
    .closest('[data-slot="status-badge"]')
  expect(warning).toHaveClass('text-warning')
  expect(warning?.querySelector('svg')).toBeInTheDocument()
  expect(warning).not.toHaveClass('text-success')
  expect(screen.getAllByText('Missing metadata')).toHaveLength(2)
  expect(screen.getAllByText('Channels 0 · Groups 0')).toHaveLength(3)
  expect(
    screen.queryByText(
      'No channel is configured. This model will not appear in the model square.'
    )
  ).not.toBeInTheDocument()
  const user = userEvent.setup()
  const trigger = screen.getAllByRole('button', {
    name: 'Unavailable',
  })[0]
  expect(trigger).toHaveAttribute(
    'title',
    'No channel is configured. This model will not appear in the model square.'
  )
  await user.click(trigger)
  expect(
    await screen.findByRole('dialog', { name: 'Unavailable' })
  ).toHaveTextContent(
    'No channel is configured. This model will not appear in the model square.'
  )
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Unavailable' })
    ).not.toBeInTheDocument()
  )
  expect(trigger).toHaveFocus()
  await user.click(screen.getByRole('button', { name: 'catalog-only' }))
  await user.click(screen.getByRole('tab', { name: 'Channels and groups' }))
  expect(
    screen.getByText(
      'No channel is configured. This model will not appear in the model square.'
    )
  ).toBeVisible()
})

it('keeps channel rows individually selectable and disables metadata mutations for mixed selection', async () => {
  await renderList()
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('checkbox', { name: 'Select channel-only' })
  )
  expect(
    screen.getByRole('checkbox', { name: 'Select channel-only' })
  ).toBeChecked()
  expect(
    screen.getByRole('checkbox', { name: 'Select other-channel' })
  ).not.toBeChecked()
  await user.click(
    screen.getByRole('checkbox', { name: 'Select catalog-only' })
  )
  expect(
    screen.getByRole('checkbox', { name: 'Select channel-only' })
  ).toBeChecked()
  const toolbar = screen.getByRole('toolbar', { name: /Bulk actions/ })
  for (const name of [
    'Change vendor',
    'Clear vendor',
    'Show selected models in model square',
    'Hide selected models from model square',
    'Delete selected models',
  ]) {
    expect(within(toolbar).getByRole('button', { name })).toBeDisabled()
  }
  expect(
    within(toolbar).getByRole('button', { name: 'Copy model names' })
  ).toBeEnabled()
  await user.click(
    screen.getByRole('checkbox', { name: 'Select channel-only' })
  )
  expect(
    within(toolbar).getByRole('button', { name: 'Delete selected models' })
  ).toBeEnabled()
})

it('keeps long model names and translated channel labels truncated inside their existing cells', async () => {
  const longName =
    'provider/very-long-channel-model-with-detailed-version-and-context-window'
  await renderList([
    { ...channel, model_name: longName },
    { ...metadata, status: 0, square_state: 'hidden' },
  ])
  expect(screen.getByText(longName)).toHaveClass('truncate')
  expect(screen.getByText('Add metadata')).toHaveClass('truncate')
  await act(async () => {
    await i18n.changeLanguage('fr')
  })
  const label = screen.getAllByText(
    i18n.t('Channels {{channels}} · Groups {{groups}}', {
      channels: 0,
      groups: 0,
    })
  )[0]
  expect(label).toHaveClass('whitespace-normal', 'sm:truncate')
  expect(label).toHaveAttribute('tabindex', '0')
  await act(async () => {
    await i18n.changeLanguage('zhCN')
  })
  expect(screen.getByText('缺元数据')).toBeVisible()
  expect(screen.getByText('已隐藏')).toBeVisible()
  expect(screen.getAllByText('渠道 0 · 分组 0')).toHaveLength(2)
})

it('prefills and creates metadata only when the user explicitly saves it', async () => {
  await renderList([channel])
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { ...channel, id: 9, has_metadata: true } },
  })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Add metadata' }))
  expect(screen.getByRole('textbox', { name: 'Model Name *' })).toHaveValue(
    'channel-only'
  )
  expect(post).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Save metadata' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/models/',
      expect.objectContaining({ model_name: 'channel-only' }),
      expect.anything()
    )
  )
})

it('keeps prices and channel explanations readable in the mobile card and detail drawer', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query === '(max-width: 640px)',
  }))
  await renderList([metadata], {
    pricing: [
      {
        model_name: metadata.model_name,
        version: 'v1',
        configured: {},
        effective: { ModelRatio: 0.625, CompletionRatio: 8 },
      },
    ],
  })
  const price = screen.getByRole('button', {
    name: 'View pricing for catalog-only',
  })
  expect(within(price).getByText('1.25')).toHaveClass('whitespace-normal')
  expect(within(price).getByText('USD / 1M tokens')).toHaveClass(
    'whitespace-normal'
  )
  expect(screen.getByText('Channels 0 · Groups 0')).toHaveClass(
    'whitespace-normal'
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'catalog-only' }))
  await user.click(screen.getByRole('tab', { name: 'Channels and groups' }))
  expect(
    screen.getByText(
      'No channel is configured. This model will not appear in the model square.'
    )
  ).toBeVisible()
})

it('uses backend square states for success, warning, hidden, and partial rows including models without metadata', async () => {
  const activeChannels = [{ name: 'Active', type: 1 }]
  await renderList([
    { ...metadata, model_name: 'catalog-only' },
    {
      ...metadata,
      id: 8,
      model_name: 'enabled-model',
      square_state: 'visible',
      configured_channel_count: 1,
      bound_channels: activeChannels,
    },
    {
      ...metadata,
      id: 9,
      model_name: 'hidden-model',
      status: 0,
      square_state: 'hidden',
      configured_channel_count: 1,
      bound_channels: activeChannels,
    },
    {
      ...metadata,
      id: 10,
      model_name: 'partial-rule-',
      name_rule: 1,
      square_state: 'partial',
      configured_channel_count: 2,
      bound_channels: activeChannels,
    },
    {
      ...channel,
      model_name: 'bare-visible',
      square_state: 'visible',
      bound_channels: activeChannels,
    },
    {
      ...channel,
      model_name: 'bare-hidden-by-rule',
      square_state: 'hidden',
      bound_channels: activeChannels,
    },
  ])
  expect(screen.getByRole('button', { name: 'Display policy' })).toBeVisible()
  for (const label of ['Unavailable', 'Partly shown']) {
    const badge = screen.getByText(label).closest('[data-slot="status-badge"]')
    expect(badge).toHaveClass('text-warning')
    expect(badge?.querySelector('svg')).toBeInTheDocument()
    expect(badge).not.toHaveClass('text-success')
  }
  const displayed = screen.getAllByText('Displayed')
  expect(displayed).toHaveLength(2)
  for (const label of displayed) {
    expect(label.closest('[data-slot="status-badge"]')).toHaveClass(
      'text-success'
    )
  }
  const hidden = screen.getAllByText('Listing hidden')
  expect(hidden).toHaveLength(2)
  for (const label of hidden) {
    expect(label.closest('[data-slot="status-badge"]')).toHaveClass(
      'text-muted-foreground'
    )
  }
  await act(async () => {
    await i18n.changeLanguage('zhCN')
  })
  expect(screen.getByText('无法展示')).toBeVisible()
  expect(screen.getByText('部分展示')).toBeVisible()
  expect(screen.getAllByText('正常展示')).toHaveLength(2)
  expect(screen.getAllByText('已隐藏')).toHaveLength(2)
  for (const label of ['无法展示', '部分展示']) {
    expect(screen.getByText(label)).toHaveClass('truncate')
  }
})

it.each(['{Enter}', ' '])(
  'opens the reason with %s and returns focus to the status after Escape',
  async (key) => {
    await renderList([metadata])
    const user = userEvent.setup()
    const trigger = screen.getByRole('button', {
      name: 'Unavailable',
    })
    act(() => trigger.focus())
    await user.keyboard(key)
    expect(
      await screen.findByRole('dialog', { name: 'Unavailable' })
    ).toHaveTextContent('No channel is configured.')
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
    )
    expect(trigger).toHaveFocus()
  }
)

it('opens the warning icon’s own model reason and closes it on an outside click without selecting the row', async () => {
  await renderList([metadata, channel])
  const user = userEvent.setup()
  const triggers = screen.getAllByRole('button', {
    name: 'Unavailable',
  })
  const icon = triggers[1].querySelector('svg')
  expect(icon).not.toBeNull()
  await user.click(icon as SVGElement)
  const popup = await screen.findByRole('dialog', { name: 'Unavailable' })
  expect(popup).toHaveTextContent('No channel is currently available.')
  expect(popup).not.toHaveTextContent('No channel is configured.')
  expect(
    screen.getByRole('checkbox', { name: 'Select channel-only' })
  ).not.toBeChecked()
  await user.click(screen.getByPlaceholderText('Filter by model name...'))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Unavailable' })
    ).not.toBeInTheDocument()
  )
  expect(triggers[1]).toHaveAttribute('aria-expanded', 'false')
  await user.click(triggers[0])
  expect(
    await screen.findByRole('dialog', { name: 'Unavailable' })
  ).toHaveTextContent('No channel is configured.')
})

it('opens a long translated reason by touch in the mobile card without requiring the detail drawer', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query === '(max-width: 640px)',
  }))
  await renderList([metadata])
  await act(async () => {
    await i18n.changeLanguage('fr')
  })
  const user = userEvent.setup()
  const trigger = screen.getByRole('button', {
    name: i18n.t('Unavailable'),
  })
  await user.pointer([
    { keys: '[TouchA>]', target: trigger },
    { keys: '[/TouchA]' },
  ])
  const popup = await screen.findByRole('dialog', {
    name: i18n.t('Unavailable'),
  })
  expect(popup).toHaveTextContent(
    i18n.t(
      'No channel is configured. This model will not appear in the model square.'
    )
  )
  expect(popup).toHaveClass('whitespace-normal', 'break-words')
  expect(popup).toHaveClass('max-w-[calc(100vw-2rem)]')
  expect(
    screen.queryByRole('dialog', { name: metadata.model_name })
  ).not.toBeInTheDocument()
})

it.each([
  {
    name: 'legacy',
    effective: { ModelRatio: 1.5, CompletionRatio: 5, CacheRatio: 0.1 },
    catalog: { model_ratio: 1.5, completion_ratio: 5 },
    text: 'Input3Output15',
  },
  {
    name: 'single-expression',
    effective: {
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr': 'tier("base", p * 3 + c * 15 + cr * 0.3)',
    },
    catalog: {
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", p * 3 + c * 15 + cr * 0.3)',
    },
    text: 'Input3Output15',
  },
  {
    name: 'tier-expression',
    effective: {
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr':
        'len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long", p * 6 + c * 22.5)',
    },
    catalog: {
      billing_mode: 'tiered_expr',
      billing_expr:
        'len <= 200000 ? tier("standard", p * 3 + c * 15) : tier("long", p * 6 + c * 22.5)',
    },
    text: 'Input3Output15',
  },
  {
    name: 'free-request',
    effective: { ModelPrice: 0 },
    catalog: { quota_type: 1, model_price: 0 },
    text: 'Per-request0USD/request',
  },
  {
    name: 'free-tokens',
    effective: { ModelRatio: 0, CompletionRatio: 2 },
    catalog: { model_ratio: 0, completion_ratio: 2 },
    text: 'Input0Output0',
  },
  {
    name: 'free-expression',
    effective: {
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr': 'tier("free", p * 0 + c * 0)',
    },
    catalog: {
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("free", p * 0 + c * 0)',
    },
    text: 'Input0Output0',
  },
])(
  'shows $name effective pricing like the catalog without requiring a listed model',
  async ({ name, effective, catalog, text }) => {
    const model = { ...channel, model_name: name }
    await renderList([model], {
      pricing: [{ model_name: name, version: 'v1', configured: {}, effective }],
    })
    const button = screen.getByRole('button', {
      name: `View pricing for ${name}`,
    })
    expect(button.textContent?.replaceAll(/\s/g, '')).toContain(text)
    expect(button).not.toHaveTextContent('Unset price')
    expect(button).not.toHaveTextContent('Cache')
    render(
      <CatalogPrice
        model={{
          id: 1,
          model_name: name,
          quota_type: 0,
          model_ratio: 0,
          completion_ratio: 0,
          enable_groups: [],
          ...catalog,
        }}
      />
    )
    expect(button.textContent).toBe(
      screen.getByRole('group', { name: 'Catalog price' }).textContent
    )
  }
)

it('shows task tier ranges in the schema unit and converts site currency only once', async () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7,
    },
  })
  const expression =
    'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)'
  const schema = {
    seconds: { type: 'number' as const, unit: 'second' as const },
    mode: { enum: ['std', 'pro'] },
  }
  await renderList([channel], {
    pricing: [
      {
        model_name: channel.model_name,
        version: 'v1',
        configured: {},
        effective: {
          'billing_setting.billing_mode': 'tiered_expr',
          'billing_setting.billing_expr': expression,
        },
        usage_schema: schema,
      },
    ],
  })
  const button = screen.getByRole('button', {
    name: 'View pricing for channel-only',
  })
  expect(button).toHaveTextContent(/2.8.*5.6/)
  expect(button).toHaveTextContent('/s')
  expect(button).toHaveTextContent('CNY')
  expect(button).not.toHaveTextContent('¥')
  expect(button).not.toHaveTextContent('1M tokens')
})

it('opens the effective expression breakdown from the price without creating metadata', async () => {
  const expression =
    '(len <= 200000 ? tier("standard", p * 3 + c * 15 + cr * 0.3) : tier("long", p * 6 + c * 22.5 + cr * 0.6)) * (header("x-priority") == "high" ? 2 : 1)'
  await renderList([channel], {
    pricing: [
      {
        model_name: channel.model_name,
        version: 'v1',
        configured: {},
        effective: {
          'billing_setting.billing_mode': 'tiered_expr',
          'billing_setting.billing_expr': expression,
        },
      },
    ],
  })
  const write = vi.spyOn(api, 'post')
  await userEvent.click(
    screen.getByRole('button', { name: 'View pricing for channel-only' })
  )
  const preview = await screen.findByRole('region', { name: 'Current Billing' })
  expect(preview).toHaveTextContent('standard')
  expect(preview).toHaveTextContent('long')
  expect(preview).toHaveTextContent('0.3')
  expect(preview).toHaveTextContent('0.6')
  expect(preview).toHaveTextContent('x-priority')
  expect(write).not.toHaveBeenCalled()
})

it('shows an unrecognized expression as special and retains its full source in pricing details', async () => {
  const expression = 'tier("custom", max(p * 2 + c * 8, 100))'
  await renderList([channel], {
    pricing: [
      {
        model_name: channel.model_name,
        version: 'v1',
        configured: {},
        effective: {
          'billing_setting.billing_mode': 'tiered_expr',
          'billing_setting.billing_expr': expression,
        },
      },
    ],
  })
  const button = screen.getByRole('button', {
    name: 'View pricing for channel-only',
  })
  expect(button).toHaveTextContent('Special billing expression')
  expect(button).not.toHaveTextContent('$2')
  expect(button).not.toHaveTextContent(expression)
  await userEvent.click(button)
  const preview = await screen.findByRole('region', { name: 'Current Billing' })
  expect(preview).toHaveTextContent(expression)
})

it('distinguishes pending and failed pricing requests from an unset price', async () => {
  let rejectPricing: (reason: Error) => void = () => {}
  const pricing = new Promise<ModelPricingConfig>((_resolve, reject) => {
    rejectPricing = reject
  })
  await renderList([channel], { pricing, waitForPricing: false })
  expect(screen.getByText('Loading...')).toBeVisible()
  expect(screen.queryByText('Unset price')).not.toBeInTheDocument()
  await act(async () => rejectPricing(new Error('pricing unavailable')))
  expect(await screen.findByText('Failed to load model pricing')).toBeVisible()
  expect(screen.queryByText('Unset price')).not.toBeInTheDocument()
})

it('filters actual visibility independently of policy and restores filters through browser history', async () => {
  const { get, router } = await renderList([channel], {
    initialUrl: '/models/metadata?page=3&status=%5B%22enabled%22%5D',
    total: 100,
  })
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Model square visibility' })
  )
  await user.click(screen.getByRole('option', { name: 'Partly shown' }))
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith('/api/models/search', {
      params: expect.objectContaining({
        status: 'enabled',
        square_state: 'partial',
        p: 1,
      }),
    })
  )
  expect(router.state.location.search).toMatchObject({
    status: ['enabled'],
    square_state: ['partial'],
  })
  await user.keyboard('{Escape}')
  await act(async () => {
    router.history.back()
  })
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('square_state')
  )
  await act(async () => {
    router.history.forward()
  })
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      square_state: ['partial'],
    })
  )
  await user.click(
    screen.getByRole('button', { name: /Model square visibility/ })
  )
  await user.click(screen.getByRole('option', { name: 'Clear filters' }))
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('square_state')
  )
  expect(router.state.location.search).toMatchObject({ status: ['enabled'] })
})

it('keeps all columns while collapsing tags and connection counts', async () => {
  await renderList([
    {
      ...channel,
      tags: 'Tools,Files,Vision',
      bound_channels: [{ name: 'Main', type: 1 }],
      enable_groups: ['default', 'premium'],
    },
  ])
  expect(screen.getByText('Channels 1 · Groups 2')).toBeVisible()
  expect(
    screen.getByRole('columnheader', { name: 'Sync policy' })
  ).toBeVisible()
  expect(screen.getByRole('columnheader', { name: 'Tags' })).toBeVisible()
  expect(screen.getByText('Tools')).toBeVisible()
  expect(screen.queryByText('Files')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Show all tags' }))
  expect(await screen.findByText('Files')).toBeVisible()
  expect(screen.getByText('Vision')).toBeVisible()
})

it('keeps an active visibility filter when its server result is empty', async () => {
  const { get } = await renderList([], {
    initialUrl: '/models/metadata?square_state=%5B%22unavailable%22%5D',
  })
  expect(screen.getByText('No Models Found')).toBeVisible()
  expect(screen.getByText('Try adjusting your search')).toBeVisible()
  expect(
    screen.getByRole('button', { name: /Model square visibility.*Unavailable/ })
  ).toBeVisible()
  expect(get).toHaveBeenCalledWith('/api/models/search', {
    params: expect.objectContaining({ square_state: 'unavailable', p: 1 }),
  })
})

it.each([
  { type: 'CUSTOM' as const, caption: '🐱 / 1M tokens' },
  { type: 'TOKENS' as const, caption: 'USD / 1M tokens' },
])(
  'uses one currency caption in $type mode without replacing prices with quota counts',
  async ({ type, caption }) => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: type,
        customCurrencySymbol: '🐱',
        customCurrencyExchangeRate: 1,
      },
    })
    await renderList([channel], {
      pricing: [
        {
          model_name: channel.model_name,
          version: 'v1',
          configured: {},
          effective: { ModelRatio: 0.25, CompletionRatio: 2 },
        },
      ],
    })
    const button = screen.getByRole('button', {
      name: 'View pricing for channel-only',
    })
    expect(button.textContent?.replaceAll(/\s/g, '')).toContain(
      'Input0.5Output1'
    )
    expect(within(button).getByText(caption)).toBeVisible()
    if (type === 'CUSTOM') {
      expect(button.textContent?.match(/🐱/g)).toHaveLength(1)
    }
  }
)
