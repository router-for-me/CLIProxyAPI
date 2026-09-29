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
  createRouter,
  createRootRoute,
  createMemoryHistory,
  RouterContextProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { TaskPluginOption } from '../../api'
import {
  CHANNEL_TYPE_OLLAMA,
  CHANNEL_TYPE_SGLANG,
  CHANNEL_TYPE_VLLM,
} from '../../constants'
import { channelSchema, type Channel } from '../../types'
import { ChannelPluginExtensions } from '../channel-plugin-extensions'
import { ChannelsProvider } from '../channels-provider'
import { ChannelMutateDrawer } from '../drawers/channel-mutate-drawer'

const originalAuth = useAuthStore.getState().auth
let client: QueryClient
let editingChannel: Channel
let pluginOptions: TaskPluginOption[]
const plugins: TaskPluginOption[] = [
  {
    key: 'video-a',
    name: 'Video A',
    icon: 'text:VA',
    baseUrl: 'https://a.example',
    models: ['video-a-1'],
    upstreams: ['vendor', 'new_api'],
  },
  {
    key: 'video-b',
    name: 'Video B',
    icon: 'text:VB',
    baseUrl: 'https://b.example',
    models: ['video-b-1'],
    upstreams: ['vendor', 'new_api'],
  },
  {
    // Vendor-only: bindable to a Task Plugin channel, never to a New API one.
    key: 'no-address',
    name: 'No Address',
    icon: 'text:NA',
    models: ['video-c-1'],
  },
]

const soraPlugin: TaskPluginOption = {
  key: 'sora',
  name: 'Sora',
  icon: 'text',
  baseUrl: 'https://video.example',
  models: ['sora-2'],
}

function deferredResponse<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((finish) => {
    resolve = finish
  })
  return { promise, resolve }
}

function ConfigurationHarness(props: {
  initialOpen?: boolean
  currentRow?: Channel
  clearRowOnClose?: boolean
}) {
  const [open, setOpen] = useState(props.initialOpen ?? true)
  const [router] = useState(() =>
    createRouter({
      routeTree: createRootRoute(),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
  )
  return (
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <ChannelsProvider>
          <button type='button' onClick={() => setOpen(true)}>
            Open channel
          </button>
          <ChannelMutateDrawer
            open={open}
            onOpenChange={setOpen}
            currentRow={
              open || !props.clearRowOnClose ? props.currentRow : null
            }
          />
        </ChannelsProvider>
      </RouterContextProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  pluginOptions = plugins
  editingChannel = channelSchema.parse({
    id: 42,
    name: 'Existing channel',
    type: 1,
    key: '',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    models: 'custom-model',
    group: 'default',
    base_url: 'https://saved.example',
  })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/channel/42') {
      return { data: { success: true, data: editingChannel } }
    }
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['upstream-model'] } }
    }
    if (url === '/api/task_plugin_options') {
      return { data: { success: true, data: pluginOptions } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'custom-model' }] } }
    }
    if (url === '/api/channel/default_base_urls') {
      return {
        data: {
          success: true,
          data: {
            22: 'https://fastgpt.server.example/api/openapi',
            24: 'https://gemini.server.example',
            43: 'https://deepseek.server.example',
            45: 'https://volcengine.server.example',
          },
        },
      }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default', 'premium'] } }
    }
    if (url === '/api/prefill_group') {
      return { data: { success: true, data: [] } }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
  window.localStorage.clear()
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: 1024,
  })
})

// Editing opens the sheet on the left, so the sheet spans x = 0..900 and the
// free space for the floating panel is on its right.
function useWideScreen() {
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: 1920,
  })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 900,
    bottom: 900,
    width: 900,
    height: 900,
    toJSON: () => ({}),
  } as DOMRect)
}

test('changing built-in providers updates server-provided URL placeholders without replacing the draft address', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^DeepSeek / }))
  const address = screen.getByRole('textbox', { name: 'Base URL' })
  await waitFor(() =>
    expect(address).toHaveAttribute(
      'placeholder',
      'https://deepseek.server.example'
    )
  )
  expect(address).toHaveValue('')
  await user.type(address, 'https://custom.example')

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^Gemini / }))
  const geminiAddress = screen.getByRole('textbox', { name: 'Base URL' })
  expect(geminiAddress).toHaveAttribute(
    'placeholder',
    'https://gemini.server.example'
  )
  expect(geminiAddress).toHaveValue('https://custom.example')
  await user.clear(geminiAddress)
  expect(geminiAddress).toHaveValue('')

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^New API / }))
  expect(screen.getByRole('textbox', { name: 'Base URL' })).toHaveAttribute(
    'placeholder',
    'Leave empty to use default'
  )
})

test.each([
  {
    type: 43,
    label: /^Base URL$/,
    url: 'https://deepseek.server.example',
    savedUrl: '',
  },
  {
    type: 22,
    label: /^Private Deployment URL$/,
    url: 'https://fastgpt.server.example/api/openapi',
    savedUrl: '',
  },
  {
    type: 45,
    label: /^API Base URL/,
    url: 'https://volcengine.server.example',
    savedUrl: 'https://custom.example',
  },
  {
    type: CHANNEL_TYPE_VLLM,
    label: /^Base URL/,
    url: 'vLLM server address, without /v1',
    savedUrl: 'http://localhost:8000',
  },
  {
    type: CHANNEL_TYPE_SGLANG,
    label: /^Base URL/,
    url: 'SGLang server address, without /v1',
    savedUrl: 'http://localhost:30000',
  },
])(
  'editing type $type keeps the URL placeholder out of the saved address',
  async ({ type, label, url, savedUrl }) => {
    editingChannel.type = type
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    if (type === 45) {
      const addressLabel = screen.getByText('API Base URL')
      for (let click = 0; click < 10; click++) {
        fireEvent.click(addressLabel)
      }
    }
    const address = screen.getByRole('textbox', { name: label })
    await waitFor(() => expect(address).toHaveAttribute('placeholder', url))
    expect(address).toHaveValue('https://saved.example')
    await user.clear(address)
    expect(address).toHaveValue('')
    if (savedUrl) await user.type(address, savedUrl)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, base_url: savedUrl })
  }
)

test('an unavailable default URL endpoint keeps the fallback placeholder and allows saving a custom address', async () => {
  const onInternalServerError = vi.fn()
  client = createAppQueryClient(onInternalServerError)
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/default_base_urls') {
      throw Object.assign(new Error('Endpoint unavailable'), {
        response: { status: 500 },
      })
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const address = screen.getByRole('textbox', { name: 'Base URL' })
  expect(address).toHaveAttribute('placeholder', 'Leave empty to use default')
  expect(address).toHaveValue('https://saved.example')
  await user.clear(address)
  await user.type(address, 'https://custom.example')
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({
    id: 42,
    base_url: 'https://custom.example',
  })
  expect(api.get).toHaveBeenCalledWith('/api/channel/default_base_urls')
  expect(onInternalServerError).not.toHaveBeenCalled()
})

test('model mapping help opens on click, stays open after pointer exit, and closes without dismissing the channel', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  const trigger = screen.getByRole('button', {
    name: 'How model mapping works',
  })
  expect(trigger).toHaveAttribute('type', 'button')
  expect(trigger).toHaveClass('size-4')
  expect(trigger).toHaveAttribute('aria-expanded', 'false')

  await user.click(trigger)
  const help = await screen.findByRole('dialog', { name: 'Request flow' })
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  expect(within(help).getByText('client-model')).toBeVisible()
  expect(within(help).getByText('upstream-model')).toBeVisible()
  await user.unhover(trigger)
  expect(help).toBeVisible()

  await user.keyboard('{Escape}')
  await waitFor(() => expect(help).not.toBeInTheDocument())
  expect(trigger).toHaveFocus()
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(screen.getByRole('dialog', { name: 'Edit Channel' })).toBeVisible()
})

test('model mapping help supports keyboard activation and wraps long model names within the viewport', async () => {
  editingChannel.model_mapping =
    '{"customer-production-reasoning-model-alias":"provider/region/deployment/production-reasoning-model-version"}'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  const trigger = screen.getByRole('button', {
    name: 'How model mapping works',
  })
  trigger.focus()
  await user.keyboard('{Enter}')
  const help = await screen.findByRole('dialog', { name: 'Request flow' })
  expect(help).toHaveClass(
    'flex-col',
    'w-96',
    'max-w-[calc(100vw-2rem)]',
    'text-sm'
  )
  expect(
    within(help).getByText('customer-production-reasoning-model-alias')
  ).toHaveClass('wrap-anywhere')
  expect(
    within(help).getByText(
      'provider/region/deployment/production-reasoning-model-version'
    )
  ).toHaveClass('wrap-anywhere')

  await user.keyboard('{Escape}')
  await waitFor(() => expect(help).not.toBeInTheDocument())
  await user.keyboard(' ')
  expect(
    await screen.findByRole('dialog', { name: 'Request flow' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Request flow' })
    ).not.toBeInTheDocument()
  )
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
})

test('selecting a plugin opens a prefilled channel and creates its explicit binding', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  expect(
    screen.getByRole('combobox', {
      name: 'Search providers, plugins, or type numbers',
    })
  ).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: /^Name\s*\*$/ })
  ).not.toBeInTheDocument()
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByRole('textbox', { name: /^Name\s*\*$/ })).toHaveValue(
    'Video A'
  )
  expect(screen.getByDisplayValue('https://a.example')).toBeVisible()
  expect(screen.queryByLabelText('Task plugin *')).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'channel-secret' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() => expect(post).toHaveBeenCalled())
  const [url, payload] = post.mock.calls[0]
  expect(url).toBe('/api/channel')
  expect(payload).toMatchObject({
    mode: 'single',
    channel: {
      name: 'Video A',
      type: 61,
      key: 'channel-secret',
      models: 'video-a-1',
      base_url: 'https://a.example',
      group: 'default',
    },
  })
  expect(
    JSON.parse((payload as { channel: { setting: string } }).channel.setting)
      .task_plugin_key
  ).toBe('video-a')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Open channel' }))
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByLabelText('API Key *')).toHaveValue('')
})

test('changing plugins preserves credentials and custom settings while applying existing model and address rules', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'keep-secret' },
  })
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'My channel' },
  })
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '7' },
  })
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(
    screen.getByRole('option', { name: 'Video B Plugin video-b' })
  )
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(screen.getByLabelText('API Key *')).toHaveValue('keep-secret')
  expect(screen.getByLabelText('Name *')).toHaveValue('My channel')
  expect(screen.getByDisplayValue('https://b.example')).toBeVisible()
  fireEvent.change(screen.getByDisplayValue('https://b.example'), {
    target: { value: 'https://custom.example' },
  })
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  expect(screen.getByDisplayValue('https://custom.example')).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByLabelText('Priority')).toHaveValue(7)
})

test('canceling provider selection or selecting the same provider preserves adjusted models', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  await user.click(screen.getByRole('button', { name: 'Clear All' }))
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  expect(screen.getByRole('dialog')).toHaveAccessibleDescription(/Video A/)
  expect(screen.getByRole('option', { name: /Video A/ })).toHaveAttribute(
    'aria-current',
    'true'
  )
  expect(
    within(
      screen.getByRole('button', { name: 'Back to configuration' })
    ).getByText('Video A')
  ).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Back to configuration' })
  )
  expect(screen.getByRole('button', { name: 'Change provider' })).toHaveFocus()
  expect(
    screen.getByRole('button', { name: 'Configure Models' })
  ).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  expect(
    screen.getByRole('button', { name: 'Configure Models' })
  ).toBeDisabled()
})

test.each(['Cancel', 'Escape'])(
  '%s while changing an existing provider returns to the same draft and category',
  async (action) => {
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
    fireEvent.change(screen.getByLabelText('Priority'), {
      target: { value: '8' },
    })
    await user.click(screen.getByRole('button', { name: 'Change provider' }))
    if (action === 'Escape') {
      await user.keyboard('{Escape}')
    } else {
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
    }
    expect(screen.getByRole('dialog')).toBeVisible()
    expect(
      screen.getByRole('tab', { name: /Routing & Mapping/ })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText('Priority')).toHaveValue(8)
    const providerControl = screen.getByRole('button', {
      name: 'Change provider',
    })
    expect(providerControl).toHaveFocus()
    expect(providerControl).toHaveTextContent('OpenAI')
  }
)

test('submitting a missing plugin address focuses its field without leaving creation', async () => {
  const post = vi.spyOn(api, 'post')
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /No Address/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(
      screen.getByRole('tab', { name: /Connection & Models/ })
    ).toHaveAttribute('aria-selected', 'true')
  )
  expect(
    screen.getByText('Base URL is required for this channel type')
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  await waitFor(() =>
    expect(
      screen.queryByText('Base URL is required for this channel type')
    ).not.toBeInTheDocument()
  )
})

test('a hidden creation drawer does not request plugin options', async () => {
  render(<ConfigurationHarness initialOpen={false} />)
  expect(api.get).not.toHaveBeenCalledWith('/api/task_plugin_options')
  await userEvent.click(screen.getByRole('button', { name: 'Open channel' }))
  expect(await screen.findByRole('option', { name: /Video A/ })).toBeVisible()
})

test('configuration navigation retains its height when the form content overflows', async () => {
  render(<ConfigurationHarness />)
  await userEvent.click(await screen.findByRole('option', { name: /Video A/ }))
  const navigation = screen.getByRole('tablist', {
    name: 'Channel configuration',
  })
  expect(navigation.parentElement).toHaveClass('shrink-0')
  expect(screen.getByRole('dialog')).toHaveClass('sm:max-w-7xl')
})

test('without plugin binding permission only built-in providers are offered', () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 2,
        username: 'admin',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: { channel: { sensitive_write: true } },
        },
      },
    },
  })
  render(<ConfigurationHarness />)
  expect(screen.getByRole('option', { name: /^OpenAI / })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Plugins' })
  ).not.toBeInTheDocument()
  expect(api.get).not.toHaveBeenCalledWith('/api/task_plugin_options')
})

test('plugin loading failure can be retried while built-in providers remain selectable', async () => {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options' && fail) {
      fail = false
      throw new Error('Offline')
    }
    return originalGet?.(url, config)
  })
  render(<ConfigurationHarness />)
  expect(await screen.findByText('Failed to load plugins')).toBeVisible()
  expect(screen.getByRole('option', { name: /^OpenAI / })).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('option', { name: /Video A/ })).toBeVisible()
})

test('creating a migrated provider uses its plugin binding instead of the legacy type', async () => {
  pluginOptions = [soraPlugin]
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  const plugin = await screen.findByRole('option', { name: 'Sora Plugin sora' })
  expect(
    screen.queryByRole('option', { name: 'Sora Built-in #55' })
  ).not.toBeInTheDocument()
  await user.click(plugin)
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        channel: expect.objectContaining({ type: 61 }),
      }),
      expect.anything()
    )
  )
  const payload = post.mock.calls[0]?.[1] as { channel: { setting: string } }
  expect(JSON.parse(payload.channel.setting)).toMatchObject({
    task_plugin_key: 'sora',
  })
})

test.each(['create', 'edit'])(
  '%s selects plugin models in the shared model dialog while preserving the built-in connection',
  async (mode) => {
    pluginOptions = [
      {
        ...soraPlugin,
        channelTypes: [1, 55],
        description: { en: 'Video generation' },
        models: [
          'custom-model',
          'sora-2',
          'sora-3',
          'sora-4',
          'sora-5',
          'sora-6',
          'sora-7',
        ],
      },
    ]
    editingChannel = {
      ...editingChannel,
      model_mapping: '{"custom-model":"upstream-model"}',
    }
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true } })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(
      <ConfigurationHarness
        currentRow={mode === 'edit' ? editingChannel : undefined}
      />
    )
    if (mode === 'create') {
      await user.click(
        screen.getByRole('option', { name: 'OpenAI Built-in #1' })
      )
      fireEvent.change(screen.getByLabelText('Name *'), {
        target: { value: 'Combined channel' },
      })
      fireEvent.change(screen.getByLabelText('API Key *'), {
        target: { value: 'channel-key' },
      })
      fireEvent.change(screen.getByLabelText(/Base URL/), {
        target: { value: 'https://channel.example' },
      })
    } else {
      await screen.findByDisplayValue('Existing channel')
    }
    const extensions = await screen.findByRole('group', {
      name: 'Plugin extensions',
    })
    expect(extensions).toHaveClass('flex-wrap')
    expect(
      within(extensions).queryByText('Video generation')
    ).not.toBeInTheDocument()
    expect(within(extensions).queryByText('sora-2')).not.toBeInTheDocument()
    const extension = within(extensions).getByRole('button', {
      name: `Sora Selected ${mode === 'edit' ? 1 : 0} / 7`,
    })
    expect(extension).toHaveAttribute('aria-haspopup', 'dialog')
    expect(
      screen.getByRole('button', { name: 'Configure Models' })
    ).toBeEnabled()
    await user.click(extension)
    const dialog = within(
      screen.getByRole('dialog', { name: 'Configure Models' })
    )
    expect(dialog.getByRole('tab', { name: 'Sora' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(dialog.getByRole('checkbox', { name: 'sora-7' })).not.toBeChecked()
    expect(dialog.getByRole('checkbox', { name: 'sora-2' })).not.toBeChecked()
    expect(
      dialog.getByRole('checkbox', { name: 'custom-model' })
    ).toHaveAttribute('aria-checked', String(mode === 'edit'))
    await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
    await user.click(dialog.getByRole('button', { name: 'Apply' }))
    expect(extension).toHaveAccessibleName(
      `Sora Selected ${mode === 'edit' ? 2 : 1} / 7`
    )
    expect(screen.getByRole('button', { name: 'sora-2' })).toBeVisible()
    const selector = screen.getByRole('combobox', {
      name: 'Select models or add custom ones',
    })
    await user.click(selector)
    expect(
      screen.getAllByRole('option', { name: 'custom-model' })
    ).toHaveLength(1)
    expect(screen.getByRole('option', { name: 'sora-2' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await user.keyboard('{Escape}')
    const submitLabel = mode === 'create' ? 'Create Channel' : 'Update Channel'
    await user.click(screen.getByRole('button', { name: submitLabel }))
    if (mode === 'create') {
      await waitFor(() =>
        expect(post).toHaveBeenCalledWith(
          '/api/channel',
          expect.objectContaining({
            channel: expect.objectContaining({
              type: 1,
              name: 'Combined channel',
              base_url: 'https://channel.example',
              key: 'channel-key',
              models: 'sora-2',
            }),
          }),
          expect.anything()
        )
      )
      const payload = post.mock.calls[0]?.[1] as {
        channel: { setting: string }
      }
      expect(JSON.parse(payload.channel.setting)).not.toHaveProperty(
        'task_plugin_key'
      )
    } else {
      await waitFor(() =>
        expect(put).toHaveBeenCalledWith(
          '/api/channel/',
          expect.objectContaining({
            type: 1,
            base_url: 'https://saved.example',
            models: 'custom-model,sora-2',
            model_mapping: '{"custom-model":"upstream-model"}',
          }),
          expect.anything()
        )
      )
      const payload = put.mock.calls[0]?.[1] as { setting: string }
      expect(JSON.parse(payload.setting)).not.toHaveProperty('task_plugin_key')
    }
  }
)

test('plugin model selection shares the field state, preserves other sources, and discards canceled changes', async () => {
  pluginOptions = [
    {
      ...soraPlugin,
      channelTypes: [1],
      models: ['custom-model', 'sora-2', 'sora-2'],
    },
    { ...plugins[0], channelTypes: [1], models: ['custom-model', 'video-a-1'] },
  ]
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  const extensions = within(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  )
  const sora = extensions.getByRole('button', { name: 'Sora Selected 1 / 2' })
  await user.click(sora)
  let dialog = within(screen.getByRole('dialog', { name: 'Configure Models' }))
  await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
  await user.click(dialog.getByRole('tab', { name: 'Video A' }))
  expect(
    dialog.queryByRole('checkbox', { name: 'sora-2' })
  ).not.toBeInTheDocument()
  expect(dialog.getByRole('checkbox', { name: 'custom-model' })).toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'video-a-1' }))
  await user.click(dialog.getByRole('tab', { name: 'All' }))
  expect(
    dialog.getAllByRole('checkbox', { name: 'custom-model' })
  ).toHaveLength(1)
  expect(dialog.getByRole('checkbox', { name: 'sora-2' })).toBeChecked()
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(sora).toHaveFocus())
  expect(sora).toHaveAccessibleName('Sora Selected 1 / 2')
  expect(
    screen.queryByRole('button', { name: 'sora-2' })
  ).not.toBeInTheDocument()

  await user.click(sora)
  dialog = within(screen.getByRole('dialog', { name: 'Configure Models' }))
  expect(dialog.getByRole('checkbox', { name: 'sora-2' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'custom-model' }))
  await user.click(dialog.getByRole('checkbox', { name: 'sora-2' }))
  await user.click(dialog.getByRole('button', { name: 'Apply' }))
  expect(sora).toHaveAccessibleName('Sora Selected 1 / 2')
  expect(
    extensions.getByRole('button', { name: 'Video A Selected 0 / 2' })
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'custom-model' })
  ).not.toBeInTheDocument()
  const selector = screen.getByRole('combobox', {
    name: 'Select models or add custom ones',
  })
  await user.click(selector)
  await user.click(screen.getByRole('option', { name: 'sora-2' }))
  await user.keyboard('{Escape}')
  expect(sora).toHaveAccessibleName('Sora Selected 0 / 2')
  expect(screen.getByRole('button', { name: 'Configure Models' })).toBeEnabled()
})

test('plugin model shortcuts truncate long names and omit plugins without model candidates', () => {
  const name = 'Production Video Generation — International Extended Models'
  render(
    <ChannelPluginExtensions
      plugins={[
        { ...soraPlugin, name },
        { ...plugins[0], models: [] },
      ]}
      selected={[]}
      onConfigure={vi.fn()}
    />
  )
  const row = screen.getByRole('group', { name: 'Plugin extensions' })
  expect(row).toHaveClass('flex-wrap')
  expect(screen.getByText(name)).toHaveClass('truncate', 'max-w-36')
  const trigger = screen.getByRole('button', { name: `${name} Selected 0 / 1` })
  expect(trigger).toHaveAttribute('title', name)
  expect(trigger).toHaveClass('min-w-0', 'max-w-full')
  expect(screen.getAllByRole('button')).toHaveLength(1)
})

test('retrying extension metadata preserves the editable built-in draft and selected models', async () => {
  pluginOptions = [{ ...soraPlugin, channelTypes: [1, 55] }]
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options' && fail) {
      fail = false
      throw new Error('Plugin metadata unavailable')
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(await screen.findByText('Failed to load plugins')).toBeVisible()
  expect(
    screen.queryByRole('group', { name: 'Plugin extensions' })
  ).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Keep this draft' },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  ).toBeVisible()
  expect(screen.getByLabelText('Name *')).toHaveValue('Keep this draft')
  expect(screen.getByLabelText(/Base URL/)).toHaveValue('https://saved.example')
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'sora-2' })
  ).not.toBeInTheDocument()
})

test('loading a replacement plugin preserves an already selected legacy creation draft', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; data: TaskPluginOption[] }
  }>()
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/task_plugin_options') return reply.promise
    return originalGet?.(url, config)
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: 'Sora Built-in #55' }))
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Legacy draft' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'draft-key' },
  })
  fireEvent.change(screen.getByLabelText(/Base URL/), {
    target: { value: 'https://draft.example' },
  })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'draft-model,'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await act(async () => {
    reply.resolve({ data: { success: true, data: [soraPlugin] } })
    await reply.promise
  })
  expect(
    await screen.findByRole('option', { name: 'Sora Plugin sora' })
  ).toBeVisible()
  const legacy = screen.getByRole('option', { name: 'Sora Built-in #55' })
  expect(legacy).toHaveAttribute('aria-current', 'true')
  await user.click(legacy)
  expect(screen.getByLabelText('Name *')).toHaveValue('Legacy draft')
  expect(screen.getByLabelText('API Key *')).toHaveValue('draft-key')
  expect(screen.getByLabelText(/Base URL/)).toHaveValue('https://draft.example')
  expect(screen.getByRole('button', { name: 'draft-model' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        channel: expect.objectContaining({ type: 55, name: 'Legacy draft' }),
      }),
      expect.anything()
    )
  )
  const payload = post.mock.calls[0]?.[1] as { channel: { setting: string } }
  expect(JSON.parse(payload.channel.setting)).not.toHaveProperty(
    'task_plugin_key'
  )
})

test('an invalid setting in another category is revealed and focused on submission', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'secret' },
  })
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: 'invalid-proxy' },
  })
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Proxy Address')).toHaveFocus()
  )
  expect(screen.getByRole('tab', { name: /Other Settings/ })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByLabelText('Proxy Address')).toHaveAttribute(
    'aria-invalid',
    'true'
  )
})

test.each([
  ['batch', 'Batch Add (one key per line)'],
  ['multi_to_single', 'Multi-Key Mode (multiple keys, one channel)'],
])('plugin creation preserves the %s request contract', async (mode, label) => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  await user.click(screen.getByRole('combobox', { name: 'Add Mode' }))
  await user.click(screen.getByRole('option', { name: label }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'first-key\nsecond-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel',
      expect.objectContaining({
        mode,
        channel: expect.objectContaining({
          key: 'first-key\nsecond-key',
          type: 61,
        }),
      }),
      expect.anything()
    )
  )
})

test('a failed creation keeps its draft and prevents duplicate submission while pending', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; message: string }
  }>()
  const post = vi.spyOn(api, 'post').mockReturnValue(reply.promise)
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'keep-secret' },
  })
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  expect(screen.getByRole('button', { name: 'Create Channel' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Create Channel' }))
  expect(post).toHaveBeenCalledTimes(1)
  await act(async () => {
    reply.resolve({
      data: { success: false, message: 'Upstream configuration rejected' },
    })
    await reply.promise
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Create Channel' })).toBeEnabled()
  )
  expect(screen.getByLabelText('API Key *')).toHaveValue('keep-secret')
  expect(screen.getByLabelText('Name *')).toHaveValue('Video A')
})

test('model discovery discards a response for old credentials and retains manually selected models', async () => {
  const oldReply = deferredResponse<{
    data: { success: boolean; data: string[] }
  }>()
  vi.spyOn(api, 'post')
    .mockReturnValueOnce(oldReply.promise)
    .mockResolvedValueOnce({
      data: { success: true, data: ['current-upstream-model'] },
    })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^OpenAI / }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'first-key' },
  })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'custom-model,'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(await screen.findByText('Fetching models...')).toBeVisible()
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'next-key' },
  })
  await act(async () => {
    oldReply.resolve({ data: { success: true, data: ['old-upstream-model'] } })
    await oldReply.promise
  })
  expect(screen.queryByText('old-upstream-model')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Fetch Models' }))
  await user.click(
    await screen.findByRole('checkbox', { name: 'current-upstream-model' })
  )
  expect(
    screen.getByRole('button', { name: 'current-upstream-model' })
  ).toBeVisible()
  expect(screen.getByText('custom-model')).toBeVisible()
})

test('model discovery reports failures inline and allows an empty result to fall back to manual models', async () => {
  vi.spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: { success: false, message: 'Upstream rejected the key' },
    })
    .mockResolvedValueOnce({ data: { success: true, data: [] } })
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: /^OpenAI / }))
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'test-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(await screen.findByText('Upstream rejected the key')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByText('No models returned by the upstream')
  ).toBeVisible()
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'custom-model,'
  )
  await user.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
})

test('quick options and detailed settings share changes across tabs and save the same configuration', async () => {
  editingChannel.setting = JSON.stringify({
    pass_through_body_enabled: true,
    responses_websocket_enabled: true,
  })
  editingChannel.settings = JSON.stringify({
    upstream_model_update_check_enabled: true,
    upstream_model_update_auto_sync_enabled: true,
  })
  editingChannel.auto_ban = 0
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  for (const name of [
    'Pass Through Body',
    'Detect model updates',
    'Responses WebSocket',
  ]) {
    const toggle = quick.getByRole('switch', { name })
    expect(toggle).toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).not.toBeChecked()
  }
  await user.click(quick.getByRole('switch', { name: 'Auto-disable channel' }))

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    screen.getByRole('switch', { name: 'Enable Responses WebSocket' })
  ).not.toBeChecked()
  const passthrough = screen.getByRole('switch', { name: 'Pass Through Body' })
  expect(passthrough).not.toBeChecked()
  await user.click(passthrough)
  expect(passthrough).not.toBeChecked()
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(passthrough).toBeChecked()
  expect(screen.getByRole('switch', { name: 'Force Format' })).toBeVisible()
  expect(
    screen.getByRole('switch', { name: 'Allow service_tier passthrough' })
  ).toBeVisible()

  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  const modelCheck = screen.getByRole('switch', {
    name: 'Upstream Model Update Check',
  })
  expect(modelCheck).not.toBeChecked()
  expect(
    screen.getByRole('switch', { name: 'Auto Sync Upstream Models' })
  ).toHaveAttribute('aria-disabled', 'true')
  await user.click(modelCheck)
  expect(
    screen.getByRole('switch', { name: 'Auto Sync Upstream Models' })
  ).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByRole('switch', { name: 'Auto Ban' })).toBeChecked()
  await user.click(screen.getByRole('switch', { name: 'Auto Ban' }))
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(quick.getByRole('switch', { name: 'Pass Through Body' })).toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Detect model updates' })
  ).toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).not.toBeChecked()
  expect(
    quick.getByRole('switch', { name: 'Responses WebSocket' })
  ).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as {
    auto_ban: number
    setting: string
    settings: string
  }
  expect(payload.auto_ban).toBe(0)
  expect(JSON.parse(payload.setting)).toMatchObject({
    pass_through_body_enabled: true,
    responses_websocket_enabled: false,
  })
  expect(JSON.parse(payload.settings)).toMatchObject({
    upstream_model_update_check_enabled: true,
    upstream_model_update_auto_sync_enabled: true,
  })
})

test('quick options show only applicable shortcuts when the provider changes', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(screen.getByRole('option', { name: 'OpenAI Built-in #1' }))
  let quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(5)
  expect(
    quick.queryByRole('switch', { name: 'Force Format' })
  ).not.toBeInTheDocument()
  expect(
    quick.queryByRole('switch', { name: 'Thinking to Content' })
  ).not.toBeInTheDocument()
  await user.click(quick.getByRole('switch', { name: 'Pass Through Body' }))
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAccessibleName(/Incomplete/)

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^DeepSeek / }))
  quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(4)
  expect(
    quick.queryByRole('switch', { name: 'Responses WebSocket' })
  ).not.toBeInTheDocument()
  expect(quick.getByRole('switch', { name: 'Pass Through Body' })).toBeChecked()

  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /Video A/ }))
  quick = within(screen.getByRole('group', { name: 'Quick options' }))
  expect(quick.getAllByRole('switch')).toHaveLength(1)
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).toBeChecked()
})

test('editing opens the shared configuration and omits an unchanged key on update', async () => {
  const channel = channelSchema.parse({
    id: 42,
    name: 'Existing channel',
    type: 1,
    key: '',
    status: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    models: 'custom-model',
    group: 'default',
  })
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42') {
      return { data: { success: true, data: channel } }
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={channel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.queryByLabelText('Type *')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeVisible()
  expect(screen.getAllByRole('tab')).toHaveLength(4)
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAccessibleName(/Ready/)
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Renamed channel' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({ id: 42, name: 'Renamed channel' }),
      expect.anything()
    )
  )
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
})

test('editing legacy channels retains the full provider list and saves the original type', async () => {
  editingChannel = { ...editingChannel, type: 55 }
  pluginOptions = [
    soraPlugin,
    {
      key: 'doubao',
      name: 'Doubao',
      icon: 'text',
      models: ['doubao-video'],
    },
  ]
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  expect(
    await screen.findByRole('option', { name: 'Sora Plugin sora' })
  ).toBeVisible()
  expect(
    screen.getByRole('option', { name: 'Doubao Built-in #54' })
  ).toBeVisible()
  const legacy = screen.getByRole('option', { name: 'Sora Built-in #55' })
  expect(legacy).toHaveAttribute('aria-current', 'true')
  await user.click(legacy)
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Renamed legacy channel' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({
        id: 42,
        type: 55,
        name: 'Renamed legacy channel',
      }),
      expect.anything()
    )
  )
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting)).not.toHaveProperty('task_plugin_key')
})

test('opening and reselecting an existing plugin preserves its saved configuration', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting: '{"task_plugin_key":"video-a"}',
    priority: 7,
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.queryByLabelText('Task plugin *')).not.toBeInTheDocument()
  const providerControl = screen.getByRole('button', {
    name: 'Change provider',
  })
  await user.click(await within(providerControl).findByText('Video A'))
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  expect(screen.getByText('custom-model')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(
    screen.getByRole('button', { name: 'Back to configuration' })
  )
  expect(screen.getByText('custom-model')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Change provider' }))
  await user.click(screen.getByRole('option', { name: /^Video B Plugin/ }))
  expect(screen.getByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByDisplayValue('https://saved.example')).toBeVisible()
  expect(screen.getByText('video-b-1')).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(screen.getByLabelText('Priority')).toHaveValue(7)
})

test('an unavailable plugin keeps its identifier and binding when other fields are updated', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting: '{"task_plugin_key":"removed-plugin"}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByText('removed-plugin')).toBeVisible()
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Updated name' },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { setting: string }
  expect(JSON.parse(payload.setting).task_plugin_key).toBe('removed-plugin')
})

test('a failed detail request blocks updating until retry loads the saved channel', async () => {
  const originalGet = vi.mocked(api.get).getMockImplementation()
  let fail = true
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42' && fail) {
      fail = false
      throw new Error('Channel details unavailable')
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await waitFor(() =>
    expect(screen.getByText('Failed to load channel')).toBeVisible()
  )
  expect(screen.getByRole('button', { name: 'Update Channel' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeDisabled()
  expect(screen.queryByLabelText('Name *')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Update Channel' })).toBeEnabled()
})

test('restoring routing defaults clears the configured indicator for both the block and category', async () => {
  editingChannel = { ...editingChannel, auto_ban: 0 }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName(/Configured/)
  const status = within(tab).getByRole('img', { name: 'Configured' })
  expect(status).toBeVisible()
  expect(tab).not.toHaveTextContent('Configured')
  await user.hover(status)
  expect(await screen.findByText('Configured')).toBeVisible()
  await user.unhover(status)
  await user.click(tab)
  const block = screen.getByRole('group', { name: 'Routing Strategy' })
  expect(within(block).getByRole('img', { name: 'Configured' })).toBeVisible()
  expect(block).toHaveClass('border-primary/35')
  await user.click(screen.getByRole('switch', { name: 'Auto Ban' }))
  expect(tab).not.toHaveAccessibleName(/Configured/)
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(block).not.toHaveClass('border-primary/35')
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '5' },
  })
  expect(tab).toHaveAccessibleName(/Configured/)
  expect(within(block).getByRole('img', { name: 'Configured' })).toBeVisible()
})

test('request processing configuration does not mark the network category as configured', async () => {
  editingChannel = {
    ...editingChannel,
    setting: '{"thinking_to_content":true}',
    param_override: '{}',
    header_override: '{}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  expect(
    within(screen.getByRole('group', { name: 'Override Rules' })).queryByRole(
      'img',
      { name: 'Configured' }
    )
  ).not.toBeInTheDocument()
  const processing = screen.getByRole('group', { name: 'Request processing' })
  expect(
    within(processing).getByRole('img', { name: 'Configured' })
  ).toBeVisible()
  await user.click(screen.getByRole('switch', { name: 'Thinking to Content' }))
  expect(
    within(processing).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('tab', { name: /Request & Response/ })
  ).not.toHaveAccessibleName(/Configured/)
})

test.each([1, 57, 58, 59, 60])(
  'provider %s marks a saved Responses WebSocket setting in Request & Response and clears the mark when disabled',
  async (type) => {
    editingChannel = {
      ...editingChannel,
      type,
      setting: '{"responses_websocket_enabled":true}',
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
    expect(requestTab).toHaveAccessibleName(/Configured/)
    expect(
      screen.getByRole('tab', { name: /Other Settings/ })
    ).not.toHaveAccessibleName(/Configured/)
    await user.click(requestTab)
    const toggle = screen.getByRole('switch', {
      name: 'Enable Responses WebSocket',
    })
    expect(toggle).toBeChecked()
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
    expect(requestTab).not.toHaveAccessibleName(/Configured/)
  }
)

test.each([
  [58, true],
  [1, false],
])(
  'provider %s explains the native-route limit under the Responses WebSocket toggle: %s',
  async (type, shown) => {
    editingChannel = { ...editingChannel, type }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
    expect(
      screen.getByRole('switch', { name: 'Enable Responses WebSocket' })
    ).toBeVisible()
    const note = screen.queryByText(
      /applies only to \/v1\/responses routes without protocol conversion/
    )
    if (shown) {
      expect(note).toBeVisible()
    } else {
      expect(note).not.toBeInTheDocument()
    }
  }
)

test('configuration from fields unsupported by the selected provider stays unmarked', async () => {
  editingChannel = {
    ...editingChannel,
    type: 61,
    setting:
      '{"task_plugin_key":"video-a","force_format":true,"responses_websocket_enabled":true}',
    settings:
      '{"allow_speed":true,"allow_service_tier":true,"upstream_model_update_check_enabled":true,"ollama_openai_chat":true}',
  }
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.getByRole('tab', { name: /Request & Response/ })
  ).not.toHaveAccessibleName(/Configured/)
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).not.toHaveAccessibleName(/Configured/)
  expect(
    screen.queryByRole('switch', {
      name: 'Use OpenAI-compatible Ollama chat API',
    })
  ).not.toBeInTheDocument()
})

test('an Ollama channel marks a saved OpenAI-compatible chat setting in Request & Response and saves the toggled value', async () => {
  editingChannel = {
    ...editingChannel,
    type: CHANNEL_TYPE_OLLAMA,
    settings: '{"ollama_openai_chat":true}',
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const requestTab = screen.getByRole('tab', { name: /Request & Response/ })
  expect(requestTab).toHaveAccessibleName(/Configured/)
  await user.click(requestTab)
  const toggle = screen.getByRole('switch', {
    name: 'Use OpenAI-compatible Ollama chat API',
  })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  expect(requestTab).not.toHaveAccessibleName(/Configured/)
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { settings: string }
  expect(JSON.parse(payload.settings)).toMatchObject({
    ollama_openai_chat: false,
  })
})

test('an invalid edit switches categories and replaces configured styling with the field error', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: 'invalid-proxy' },
  })
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Proxy Address')).toHaveFocus()
  )
  expect(
    screen.getByRole('tab', { name: /Other Settings/ })
  ).toHaveAccessibleName(/Error/)
  const block = screen.getByRole('group', { name: 'Channel Extra Settings' })
  expect(within(block).getByRole('img', { name: 'Error' })).toBeVisible()
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
  expect(block).toHaveClass('border-destructive/50')
  fireEvent.change(screen.getByLabelText('Proxy Address'), {
    target: { value: '' },
  })
  await waitFor(() =>
    expect(
      within(block).queryByRole('img', { name: 'Error' })
    ).not.toBeInTheDocument()
  )
  expect(
    within(block).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()
})

test('ordinary edits discover models with saved settings and keep removed draft models available for reselection', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  const put = vi.spyOn(api, 'put')
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByDisplayValue('https://saved.example'), {
    target: { value: 'https://draft.example' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'new-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('checkbox', { name: 'upstream-model' })
  )
  expect(api.get).toHaveBeenCalledWith(
    '/api/channel/fetch_models/42',
    expect.anything()
  )
  expect(screen.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'upstream-model' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'New Models (1)' })).toBeVisible()
  expect(screen.getByRole('tab', { name: 'Removed Models (1)' })).toBeVisible()
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'manual-draft,'
  )
  await user.keyboard('{Escape}')
  const removedTab = screen.getByRole('tab', { name: 'Removed Models (2)' })
  await user.click(removedTab)
  for (const model of ['custom-model', 'manual-draft']) {
    await user.click(screen.getByRole('checkbox', { name: model }))
    expect(
      screen.queryByRole('button', { name: model })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: model })).not.toBeChecked()
    expect(removedTab).toHaveAttribute('aria-selected', 'true')
  }
  await user.click(screen.getByRole('checkbox', { name: 'manual-draft' }))
  expect(screen.getByRole('button', { name: 'manual-draft' })).toBeVisible()
  expect(screen.getByRole('checkbox', { name: 'manual-draft' })).toBeChecked()
  expect(screen.getAllByRole('dialog')).toHaveLength(1)
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()
})

test('model configuration replaces the selected-count badge at the right of the model field header', async () => {
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')

  const models = screen.getByRole('group', { name: 'Models' })
  const input = within(models).getByRole('combobox', {
    name: 'Select models or add custom ones',
  })
  const configure = within(models).getByRole('button', {
    name: 'Configure Models',
  })
  expect(configure.parentElement).toHaveClass(
    'flex',
    'items-start',
    'justify-between'
  )
  expect(configure).toHaveClass('shrink-0')
  expect(
    configure.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(screen.queryByText('Selected 1')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('button', { name: 'Configure Models' })
  ).toHaveLength(1)
})

test('model configuration uses only the current form models and persists changes only when the channel is saved', async () => {
  editingChannel.models = 'gpt-one,manual-model,alias'
  const post = vi.spyOn(api, 'post')
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })
  await user.type(
    screen.getByRole('combobox', { name: 'Select models or add custom ones' }),
    'gpt-two,'
  )
  await user.keyboard('{Escape}')
  const models = screen.getByRole('group', { name: 'Models' })
  const manualModelChip = within(models).getByRole('button', {
    name: 'manual-model',
  })
  vi.mocked(api.get).mockClear()

  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  for (const model of ['gpt-one', 'manual-model', 'alias', 'gpt-two']) {
    expect(dialog.getByRole('checkbox', { name: model })).toBeChecked()
  }
  expect(
    dialog.queryByRole('checkbox', { name: 'upstream-model' })
  ).not.toBeInTheDocument()
  expect(
    dialog.queryByRole('checkbox', { name: 'custom-model' })
  ).not.toBeInTheDocument()
  expect(dialog.queryByRole('tab')).not.toBeInTheDocument()
  expect(dialog.getByText('Current models: 4')).toBeVisible()
  await user.click(dialog.getByRole('checkbox', { name: 'manual-model' }))
  expect(manualModelChip).toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()

  await user.click(dialog.getByRole('button', { name: 'Apply' }))

  expect(
    within(models).queryByRole('button', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  await waitFor(() => expect(trigger).toHaveFocus())
  expect(api.get).not.toHaveBeenCalled()
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()
  await user.click(trigger)
  const reopened = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  expect(
    reopened.queryByRole('checkbox', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  expect(reopened.getByRole('checkbox', { name: 'gpt-two' })).toBeChecked()
  await user.click(reopened.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({
    id: 42,
    models: 'gpt-one,alias,gpt-two',
  })
})

test.each(['Cancel', 'Close', 'Escape'])(
  'model configuration discards changes on %s and restores focus to its trigger',
  async (action) => {
    editingChannel.models = 'gpt-one,gpt-two'
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const trigger = screen.getByRole('button', { name: 'Configure Models' })
    trigger.focus()
    await user.keyboard('{Enter}')
    const dialog = within(
      screen.getByRole('dialog', { name: 'Configure Models' })
    )
    const model = dialog.getByRole('checkbox', { name: 'gpt-one' })
    model.focus()
    await user.keyboard(' ')
    expect(model).not.toBeChecked()

    if (action === 'Escape') {
      await user.keyboard('{Escape}')
    } else {
      await user.click(dialog.getByRole('button', { name: action }))
    }

    await waitFor(() => expect(trigger).toHaveFocus())
    expect(screen.getByRole('button', { name: 'gpt-one' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'gpt-two' })).toBeVisible()
    await user.click(trigger)
    expect(
      within(
        screen.getByRole('dialog', { name: 'Configure Models' })
      ).getByRole('checkbox', { name: 'gpt-one' })
    ).toBeChecked()
  }
)

test('model configuration keeps unchecked candidates searchable and supports category selection and applying an empty list', async () => {
  editingChannel.models = 'gpt-one,gpt-two,manual-model'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  const category = dialog.getByRole('checkbox', {
    name: 'Select all models in OpenAI',
  })
  await user.click(category)
  expect(dialog.getByRole('checkbox', { name: 'gpt-one' })).not.toBeChecked()
  expect(dialog.getByRole('checkbox', { name: 'gpt-two' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-one' }))
  expect(category).toHaveAttribute('aria-checked', 'mixed')
  const search = dialog.getByRole('textbox', { name: 'Search models...' })
  await user.type(search, 'gpt-')
  await user.click(
    dialog.getByRole('button', { name: 'Select all matching models' })
  )
  expect(dialog.getByRole('checkbox', { name: 'gpt-two' })).toBeChecked()
  expect(
    dialog.queryByRole('checkbox', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  await user.clear(search)
  await user.type(search, 'missing')
  expect(dialog.getByText('No matching items')).toBeVisible()
  expect(
    dialog.getByRole('button', { name: 'Select all matching models' })
  ).toBeDisabled()
  await user.clear(search)
  expect(dialog.getByRole('checkbox', { name: 'manual-model' })).toBeChecked()
  await user.click(
    dialog.getByRole('checkbox', { name: 'Select all models in OpenAI' })
  )
  await user.click(
    dialog.getByRole('checkbox', { name: 'Select all models in Other' })
  )
  await user.click(dialog.getByRole('button', { name: 'Apply' }))

  expect(
    screen.queryByRole('button', { name: 'manual-model' })
  ).not.toBeInTheDocument()
  expect(trigger).toBeDisabled()
})

test('model configuration is available for a plugin channel without upstream discovery and is disabled when its model list is empty', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness />)
  await user.click(await screen.findByRole('option', { name: /Video A/ }))
  const trigger = screen.getByRole('button', { name: 'Configure Models' })
  expect(trigger).toBeEnabled()
  expect(
    screen.queryByRole('button', { name: 'Fetch from Upstream' })
  ).not.toBeInTheDocument()
  await user.click(trigger)
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  expect(dialog.getByRole('checkbox', { name: 'video-a-1' })).toBeChecked()
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Clear All' }))

  expect(trigger).toBeDisabled()
})

test('advanced custom edits preview draft connection settings with the saved key', async () => {
  editingChannel = {
    ...editingChannel,
    type: 58,
    settings: JSON.stringify({
      advanced_custom: {
        advanced_routes: [
          {
            incoming_path: '/v1/models',
            upstream_path: '/v1/models',
            converter: 'none',
          },
        ],
      },
    }),
  }
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: ['preview-model'] } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByDisplayValue('https://saved.example'), {
    target: { value: 'https://draft.example' },
  })
  fireEvent.change(screen.getByLabelText('API Key *'), {
    target: { value: 'unsaved-key' },
  })
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'preview-model' })
  ).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/fetch_models',
    expect.objectContaining({
      type: 58,
      channel_id: 42,
      base_url: 'https://draft.example',
      key: undefined,
    }),
    expect.anything()
  )
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/fetch_models/42',
    expect.anything()
  )
})

test('an operator without sensitive write permission can discover saved models and update routing', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 10,
        username: 'operator',
        role: ROLE.ADMIN,
        permissions: {
          admin_permissions: {
            channel: { read: true, write: true, operate: true },
          },
        },
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(screen.getByRole('button', { name: 'Change provider' })).toBeDisabled()
  expect(screen.getByLabelText('API Key *')).toBeDisabled()
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  for (const name of [
    'Pass Through Body',
    'Detect model updates',
    'Responses WebSocket',
  ]) {
    const toggle = quick.getByRole('switch', { name })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
  }
  expect(
    quick.getByRole('switch', { name: 'Auto-disable channel' })
  ).not.toHaveAttribute('aria-disabled', 'true')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'upstream-model' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const thinking = screen.getByRole('switch', { name: 'Thinking to Content' })
  expect(thinking).toHaveAttribute('aria-disabled', 'true')
  await user.click(thinking)
  expect(thinking).not.toBeChecked()
  await user.click(screen.getByRole('tab', { name: /Other Settings/ }))
  expect(screen.getByLabelText('Proxy Address')).toBeDisabled()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  fireEvent.change(screen.getByLabelText('Priority'), {
    target: { value: '8' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, priority: 8 })
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('setting')
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
})

test.each([
  ['random', 'Random', 'polling', 'Polling'],
  ['polling', 'Polling', 'random', 'Random'],
] as const)(
  'multi-key editing switches from %s without replacing keys',
  async (initialMode, initialLabel, nextMode, nextLabel) => {
    editingChannel.channel_info = {
      ...editingChannel.channel_info,
      is_multi_key: true,
      multi_key_size: 2,
      multi_key_mode: initialMode,
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    const strategy = screen.getByRole('combobox', {
      name: 'Multi-Key Strategy',
    })
    expect(strategy).toHaveTextContent(initialLabel)
    await user.click(strategy)
    await user.click(screen.getByRole('option', { name: nextLabel }))
    expect(strategy).toHaveTextContent(nextLabel)
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({
      id: 42,
      multi_key_mode: nextMode,
    })
    expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key')
    expect(put.mock.calls[0]?.[1]).not.toHaveProperty('key_mode')
  }
)

test('single-key editing omits the multi-key strategy control and update field', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(
    screen.queryByRole('combobox', { name: 'Multi-Key Strategy' })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).not.toHaveProperty('multi_key_mode')
})

test.each(['append', 'replace'])(
  'multi-key editing submits the selected %s mode with new keys',
  async (mode) => {
    editingChannel = {
      ...editingChannel,
      channel_info: {
        ...editingChannel.channel_info,
        is_multi_key: true,
        multi_key_size: 2,
      },
    }
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    if (mode === 'replace') {
      await user.click(
        screen.getByRole('combobox', { name: 'Key Update Mode' })
      )
      await user.click(
        screen.getByRole('option', { name: 'Replace all existing keys' })
      )
    }
    fireEvent.change(screen.getByLabelText('API Key *'), {
      target: { value: 'next-key' },
    })
    await user.click(screen.getByRole('button', { name: 'Update Channel' }))
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(put.mock.calls[0]?.[1]).toMatchObject({
      key: 'next-key',
      key_mode: mode,
    })
  }
)

test('a failed update retains the draft through a background detail refetch', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Update failed' },
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  fireEvent.change(screen.getByLabelText('Name *'), {
    target: { value: 'Unsaved name' },
  })
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Update Channel' })).toBeEnabled()
  )
  await act(async () => {
    await client.refetchQueries({ queryKey: ['channels'] })
  })
  expect(screen.getByDisplayValue('Unsaved name')).toBeVisible()
})

test('switching edited channels discards a pending model list from the previous channel', async () => {
  const reply = deferredResponse<{
    data: { success: boolean; data: string[] }
  }>()
  const otherChannel = { ...editingChannel, id: 43, name: 'Second channel' }
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/fetch_models/42') return reply.promise
    if (url === '/api/channel/43') {
      return { data: { success: true, data: otherChannel } }
    }
    if (url === '/api/channel/fetch_models/43') {
      return { data: { success: true, data: ['second-model'] } }
    }
    return originalGet?.(url, config)
  })
  const user = userEvent.setup()
  const view = render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  view.rerender(<ConfigurationHarness currentRow={otherChannel} />)
  await screen.findByDisplayValue('Second channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  expect(
    await screen.findByRole('checkbox', { name: 'second-model' })
  ).toBeVisible()
  await act(async () => {
    reply.resolve({ data: { success: true, data: ['first-model'] } })
  })
  expect(
    screen.queryByRole('checkbox', { name: 'first-model' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: 'second-model' })).toBeVisible()
})

test('an unknown saved type remains editable without selecting a new provider', async () => {
  editingChannel = { ...editingChannel, type: 999 }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(<ConfigurationHarness currentRow={editingChannel} />)
  expect(await screen.findByDisplayValue('Existing channel')).toBeVisible()
  expect(screen.getByText('#999')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0]?.[1]).toMatchObject({ id: 42, type: 999 })
})

test('a background refresh updates untouched values without moving the selected category', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  editingChannel = { ...editingChannel, priority: 5 }
  await act(async () => {
    await client.refetchQueries({ queryKey: ['channels'] })
  })
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  await waitFor(() => expect(screen.getByLabelText('Priority')).toHaveValue(5))
})

test('closing an edited channel retains its left exit direction after the parent clears the row', async () => {
  const animation = deferredResponse<void>()
  const originalGetAnimations = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    'getAnimations'
  )
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
    configurable: true,
    value(this: HTMLElement) {
      return this.hasAttribute('data-ending-style')
        ? [{ finished: animation.promise }]
        : []
    },
  })
  try {
    const user = userEvent.setup()
    const view = render(
      <ConfigurationHarness currentRow={editingChannel} clearRowOnClose />
    )
    await screen.findByDisplayValue('Existing channel')
    const drawer = screen.getByRole('dialog')
    expect(drawer).toHaveAttribute('data-side', 'left')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(drawer).toHaveAttribute('data-ending-style'))
    expect(drawer).toBeInTheDocument()
    expect(drawer).toHaveAttribute('data-side', 'left')
    expect(drawer).toHaveClass('left-0')
    expect(drawer).not.toHaveClass('right-0')
    await act(async () => {
      animation.resolve()
    })
    await waitFor(() => expect(drawer).not.toBeInTheDocument())

    view.rerender(<ConfigurationHarness clearRowOnClose />)
    await user.click(screen.getByRole('button', { name: 'Open channel' }))
    expect(screen.getByRole('dialog')).toHaveAttribute('data-side', 'right')
  } finally {
    animation.resolve()
    if (originalGetAnimations) {
      Object.defineProperty(
        HTMLElement.prototype,
        'getAnimations',
        originalGetAnimations
      )
    } else {
      Reflect.deleteProperty(HTMLElement.prototype, 'getAnimations')
    }
  }
})

test('the model list summarizes redirects, counts them on the routing tab, and jumps to the mapping editor', async () => {
  editingChannel = {
    ...editingChannel,
    model_mapping: '{"custom-model":"upstream-model"}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  expect(screen.getByText('1 model(s) redirected')).toBeVisible()
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName(/^Routing & Mapping\s?1 model mapping\(s\)$/)
  expect(
    within(tab).queryByRole('img', { name: 'Configured' })
  ).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Edit mapping' }))
  expect(tab).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('custom-model')
})

test('without mappings the model list offers to set up redirects and the routing tab shows no count', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const tab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  expect(tab).toHaveAccessibleName('Routing & Mapping')
  expect(screen.queryByText(/model\(s\) redirected/)).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('button', { name: 'Set up model redirects' })
  )
  expect(tab).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('button', { name: 'Add Mapping' })).toBeVisible()
})

test('mapping guardrail alerts link to the tab that holds the fix', async () => {
  editingChannel = {
    ...editingChannel,
    model_mapping: '{"alias":"custom-model"}',
  }
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const routingTab = screen.getByRole('tab', { name: /Routing & Mapping/ })
  const connectionTab = screen.getByRole('tab', {
    name: /Connection & Models/,
  })
  await user.click(screen.getByRole('button', { name: 'View mapping' }))
  expect(routingTab).toHaveAttribute('aria-selected', 'true')
  await user.click(screen.getByRole('button', { name: 'View models' }))
  expect(connectionTab).toHaveAttribute('aria-selected', 'true')
})

test('redirecting a model from the model picker publishes it, adds a mapping row, and focuses its upstream field', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Configure Models' }))
  const dialog = within(
    screen.getByRole('dialog', { name: 'Configure Models' })
  )
  await user.click(
    dialog.getByRole('button', { name: 'Redirect custom-model' })
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Configure Models' })
    ).not.toBeInTheDocument()
  )
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('custom-model')
  await waitFor(() =>
    expect(
      screen.getByRole('combobox', { name: 'Upstream Model Name' })
    ).toHaveFocus()
  )
})

test('fetched upstream models can be redirected in bulk with the model list synced', async () => {
  editingChannel.models = 'custom-model,upstream-model'
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('tab', { name: 'Existing Models (1)' })
  )
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(screen.getByRole('button', { name: 'Batch redirect' }))
  const dialog = within(
    screen.getByRole('dialog', { name: 'Batch add mappings' })
  )
  expect(
    dialog.getByRole('tab', { name: /Upstream model list/ })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    dialog.getByRole('checkbox', { name: 'upstream-model' })
  ).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'upstream-model' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-model')
  await user.click(dialog.getByRole('button', { name: 'Add 1 mapping(s)' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Batch add mappings' })
    ).not.toBeInTheDocument()
  )

  expect(screen.getByText('1 model(s) redirected')).toBeVisible()
  expect(screen.getByRole('button', { name: 'upstream' })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'upstream-model' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('upstream')
  expect(
    screen.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')
})

test('a fetched upstream model can be given a request name from its own row', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(
    screen.getByRole('button', { name: 'Redirect upstream-model' })
  )
  expect(
    screen.getByRole('tab', { name: /Routing & Mapping/ })
  ).toHaveAttribute('aria-selected', 'true')
  await waitFor(() =>
    expect(
      screen.getByRole('combobox', { name: 'Request Model Name' })
    ).toHaveFocus()
  )
  expect(
    screen.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')
})

test('on wide screens redirecting a fetched model opens the floating panel beside the drawer and syncs the model list as the alias is typed', async () => {
  useWideScreen()
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await screen.findByRole('checkbox', { name: 'upstream-model' })

  await user.click(
    screen.getByRole('button', { name: 'Redirect upstream-model' })
  )
  const panelElement = await screen.findByRole('complementary', {
    name: 'Model redirects',
  })
  const panel = within(panelElement)
  expect(panelElement).toHaveStyle({
    position: 'fixed',
    left: '916px',
    top: '16px',
    width: '420px',
  })
  // The window floats; the sheet keeps its full width.
  expect(document.querySelector('[data-slot="sheet-content"]')).toHaveClass(
    'sm:max-w-7xl'
  )
  expect(
    screen.getByRole('tab', { name: /Connection & Models/ })
  ).toHaveAttribute('aria-selected', 'true')
  const request = panel.getByRole('combobox', { name: 'Request Model Name' })
  await waitFor(() => expect(request).toHaveFocus())
  expect(
    panel.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('upstream-model')

  await user.type(request, 'upstream')
  const picker = within(screen.getByRole('group', { name: 'Models' }))
  expect(
    picker.queryByRole('button', { name: 'upstream' })
  ).not.toBeInTheDocument()
  await user.keyboard('{Enter}')
  expect(screen.getByLabelText('Published as upstream')).toBeVisible()
  expect(picker.getByRole('button', { name: 'upstream' })).toBeVisible()
  expect(picker.getByRole('button', { name: 'custom-model' })).toBeVisible()
  expect(picker.queryByRole('button', { name: 'ups' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('tab', { name: /Routing & Mapping/ }))
  expect(
    screen.getByText('Model mappings are being edited in the floating panel.')
  ).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Close the panel to edit here' })
  )
  expect(
    screen.queryByRole('complementary', { name: 'Model redirects' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('combobox', { name: 'Request Model Name' })
  ).toHaveValue('upstream')
})

test.each(['current session', 'saved preference'])(
  'quick redirect expands a panel collapsed in the %s and focuses the new request name',
  async (collapsedIn) => {
    useWideScreen()
    editingChannel.model_mapping = '{"existing-alias":"existing-upstream"}'
    if (collapsedIn === 'saved preference') {
      window.localStorage.setItem(
        'floating-window:channel-model-redirects',
        JSON.stringify({
          x: 1000,
          y: 80,
          width: 380,
          height: 360,
          collapsed: true,
        })
      )
    }
    const user = userEvent.setup()
    render(<ConfigurationHarness currentRow={editingChannel} />)
    await screen.findByDisplayValue('Existing channel')
    await user.click(
      screen.getByRole('button', { name: 'Fetch from Upstream' })
    )
    await screen.findByRole('checkbox', { name: 'upstream-model' })
    if (collapsedIn === 'current session') {
      await user.click(screen.getByRole('button', { name: 'Redirect panel' }))
      await user.click(screen.getByRole('button', { name: 'Collapse panel' }))
      expect(screen.getByRole('button', { name: 'Expand panel' })).toBeVisible()
    }

    await user.click(
      screen.getByRole('button', { name: 'Redirect upstream-model' })
    )
    let panelElement = await screen.findByRole('complementary', {
      name: 'Model redirects',
    })
    let panel = within(panelElement)
    await waitFor(() =>
      expect(panel.getByDisplayValue('upstream-model')).toBeVisible()
    )
    const request = panel.getAllByRole('combobox', {
      name: 'Request Model Name',
    })[1]
    await waitFor(() => expect(request).toHaveFocus())
    expect(request).toHaveValue('')
    expect(panel.getByDisplayValue('existing-alias')).toBeVisible()
    expect(panel.getByDisplayValue('existing-upstream')).toBeVisible()
    expect(screen.getByText('1 model(s) redirected')).toBeVisible()
    if (collapsedIn === 'saved preference') {
      expect(panelElement).toHaveStyle({
        left: '1000px',
        top: '80px',
        width: '380px',
        height: '360px',
      })
    }

    // Handling the draft must not prevent collapse or swallow the next shortcut for the same model.
    await user.click(panel.getByRole('button', { name: 'Collapse panel' }))
    expect(
      screen.queryByRole('complementary', { name: 'Model redirects' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Redirect upstream-model' })
    )
    panelElement = await screen.findByRole('complementary', {
      name: 'Model redirects',
    })
    panel = within(panelElement)
    await waitFor(() =>
      expect(panel.getByDisplayValue('upstream-model')).toBeVisible()
    )
    await waitFor(() =>
      expect(
        panel.getAllByRole('combobox', { name: 'Request Model Name' })[1]
      ).toHaveFocus()
    )
  }
)

test('redirecting a recognized upstream name opens an empty alias draft without changing the model list', async () => {
  useWideScreen()
  const baseGet = vi.mocked(api.get).getMockImplementation()
  vi.spyOn(api, 'get').mockImplementation(async (url, ...rest) => {
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['gpt-4o-all'] } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'gpt-4o' }] } }
    }
    return baseGet?.(url, ...rest)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('button', { name: 'Redirect gpt-4o-all' })
  )
  const panel = within(
    await screen.findByRole('complementary', { name: 'Model redirects' })
  )
  const request = panel.getByRole('combobox', { name: 'Request Model Name' })
  await waitFor(() => expect(request).toHaveFocus())
  expect(request).toHaveValue('')
  expect(
    panel.getByRole('combobox', { name: 'Upstream Model Name' })
  ).toHaveValue('gpt-4o-all')
  expect(screen.queryByLabelText('Published as gpt-4o')).not.toBeInTheDocument()
  expect(
    within(screen.getByRole('group', { name: 'Models' })).getByRole('button', {
      name: 'custom-model',
    })
  ).toBeVisible()
  expect(screen.queryByText('1 model(s) redirected')).not.toBeInTheDocument()
})

test('the redirect panel batch action starts empty and only applies explicitly selected models after preview', async () => {
  useWideScreen()
  editingChannel.models = 'custom-model,gpt-4o-all,o3-all'
  const baseGet = vi.mocked(api.get).getMockImplementation()
  vi.spyOn(api, 'get').mockImplementation(async (url, ...rest) => {
    if (url === '/api/channel/fetch_models/42') {
      return { data: { success: true, data: ['gpt-4o-all', 'o3-all'] } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [{ id: 'gpt-4o' }] } }
    }
    return baseGet?.(url, ...rest)
  })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('button', { name: 'Fetch from Upstream' }))
  await user.click(
    await screen.findByRole('tab', { name: 'Existing Models (2)' })
  )
  await screen.findByRole('checkbox', { name: 'gpt-4o-all' })

  await user.click(screen.getByRole('button', { name: 'Redirect panel' }))
  const panel = within(
    await screen.findByRole('complementary', { name: 'Model redirects' })
  )
  expect(panel.queryByText('Suggested rules')).not.toBeInTheDocument()
  await user.click(panel.getByRole('button', { name: 'Batch Add' }))
  let dialog = within(
    screen.getByRole('dialog', { name: 'Batch add mappings' })
  )
  expect(dialog.getByRole('checkbox', { name: 'gpt-4o-all' })).not.toBeChecked()
  expect(dialog.getByRole('checkbox', { name: 'o3-all' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-all')
  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  const picker = within(screen.getByRole('group', { name: 'Models' }))
  expect(picker.getByRole('button', { name: 'gpt-4o-all' })).toBeVisible()
  expect(
    picker.queryByRole('button', { name: 'gpt-4o' })
  ).not.toBeInTheDocument()

  await user.click(panel.getByRole('button', { name: 'Batch Add' }))
  dialog = within(screen.getByRole('dialog', { name: 'Batch add mappings' }))
  expect(dialog.getByRole('checkbox', { name: 'gpt-4o-all' })).not.toBeChecked()
  await user.click(dialog.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.type(dialog.getByRole('textbox', { name: 'Suffix' }), '-all')
  expect(dialog.getByRole('list', { name: 'Preview' })).toHaveTextContent(
    'gpt-4ogpt-4o-all'
  )
  await user.click(dialog.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(
    panel.getAllByRole('combobox', { name: 'Request Model Name' })
  ).toHaveLength(1)
  expect(picker.getByRole('button', { name: 'gpt-4o' })).toBeVisible()
  expect(picker.getByRole('button', { name: 'o3-all' })).toBeVisible()
  expect(picker.queryByRole('button', { name: 'o3' })).not.toBeInTheDocument()
  expect(screen.getByText('1 model(s) redirected')).toBeVisible()

  await user.click(panel.getByRole('button', { name: 'Collapse panel' }))
  expect(
    screen.queryByRole('complementary', { name: 'Model redirects' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Expand panel' }))
  expect(
    screen.getByRole('complementary', { name: 'Model redirects' })
  ).toBeVisible()
})

test('on wide screens the quick options move into the header as a compact toggle strip and leave the form', async () => {
  useWideScreen()
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = screen.getByRole('group', { name: 'Quick options' })
  expect(quick.closest('[data-slot="sheet-header"]')).not.toBeNull()
  expect(within(quick).getAllByRole('switch')).toHaveLength(5)
  // Second header row: status badge and description on the left, toggles right.
  const statusRow = quick.parentElement
  if (!statusRow) throw new Error('status row missing')
  expect(within(statusRow).getByText('Enabled')).toBeVisible()
  expect(
    within(statusRow).queryByRole('heading', { name: /Edit Channel/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText(
      'Preserve upstream-specific fields when API formats match; bypasses model redirect, parameter override and format conversion'
    )
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Quick actions')).toBeVisible()

  const passthrough = within(quick).getByRole('switch', {
    name: 'Pass Through Body',
  })
  expect(passthrough).toHaveAttribute('data-size', 'sm')
  expect(passthrough).not.toBeChecked()
  await user.click(passthrough)
  expect(passthrough).not.toBeChecked()
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(passthrough).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const all = screen.getAllByRole('switch', { name: 'Pass Through Body' })
  expect(all).toHaveLength(2)
  for (const toggle of all) expect(toggle).toBeChecked()
})

test('header passthrough quick option confirms before writing the wildcard rule and clears it without asking', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  const quick = within(screen.getByRole('group', { name: 'Quick options' }))
  const headers = quick.getByRole('switch', {
    name: 'Pass Through Request Headers',
  })
  expect(headers).not.toBeChecked()

  await user.click(headers)
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Enable request header passthrough?',
  })
  expect(dialog).toHaveTextContent(
    'Entries configured in Request Header Override still take priority'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(headers).not.toBeChecked()

  await user.click(headers)
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  expect(headers).toBeChecked()

  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const editor = screen.getByLabelText(
    'Request Header Override'
  ) as HTMLTextAreaElement
  expect(JSON.parse(editor.value)).toEqual({ '*': true })

  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  await user.click(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  )
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  ).not.toBeChecked()

  await user.click(
    quick.getByRole('switch', { name: 'Pass Through Request Headers' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as { header_override: string }
  expect(JSON.parse(payload.header_override)).toEqual({ '*': true })
})

test('the header override passthrough template button confirms before filling the wildcard rule', async () => {
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={editingChannel} />)
  await screen.findByDisplayValue('Existing channel')
  await user.click(screen.getByRole('tab', { name: /Request & Response/ }))
  const editor = screen.getByLabelText(
    'Request Header Override'
  ) as HTMLTextAreaElement
  expect(editor.value).toBe('')

  await user.click(screen.getByRole('button', { name: 'Passthrough Template' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Enable request header passthrough?',
  })
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(editor.value).toBe('')

  await user.click(screen.getByRole('button', { name: 'Passthrough Template' }))
  await user.click(
    await screen.findByRole('button', { name: 'Enable passthrough' })
  )
  await waitFor(() => expect(JSON.parse(editor.value)).toEqual({ '*': true }))
  await user.click(screen.getByRole('tab', { name: /Connection & Models/ }))
  expect(
    within(screen.getByRole('group', { name: 'Quick options' })).getByRole(
      'switch',
      { name: 'Pass Through Request Headers' }
    )
  ).toBeChecked()
})

test('a New API channel binds upstream task plugins and publishes their models', async () => {
  const channel = channelSchema.parse({
    ...editingChannel,
    type: 60,
    base_url: 'https://gateway.example',
    models: 'gpt-5',
    setting: JSON.stringify({ task_extend_plugin_keys: ['video-a'] }),
  })
  const originalGet = vi.mocked(api.get).getMockImplementation()
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url === '/api/channel/42') {
      return { data: { success: true, data: channel } }
    }
    return originalGet?.(url, config)
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  render(<ConfigurationHarness currentRow={channel} />)
  const extensions = within(
    await screen.findByRole('group', { name: 'Plugin extensions' })
  )
  expect(
    extensions.getByRole('button', { name: 'Video A Selected 0 / 1' })
  ).toBeVisible()
  expect(
    extensions.queryByRole('button', { name: /^Video B/ })
  ).not.toBeInTheDocument()

  const selector = screen.getByRole('combobox', {
    name: 'Select the task plugins installed on the upstream gateway',
  })
  await user.click(selector)
  expect(
    screen.queryByRole('option', { name: /^No Address/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('option', { name: /^Video B/ }))
  await user.keyboard('{Escape}')
  expect(
    extensions.getByRole('button', { name: 'Video B Selected 1 / 1' })
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'video-b-1' })).toBeVisible()

  await user.click(selector)
  await user.click(screen.getByRole('option', { name: /^Video A/ }))
  await user.keyboard('{Escape}')
  expect(
    extensions.queryByRole('button', { name: /^Video A/ })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'gpt-5' })).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Update Channel' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  const payload = put.mock.calls[0]?.[1] as {
    setting?: string
    models?: string
  }
  const setting = JSON.parse(payload.setting ?? '{}')
  expect(setting).toMatchObject({ task_extend_plugin_keys: ['video-b'] })
  expect(setting).not.toHaveProperty('task_plugin_key')
  expect(payload.models?.split(',').sort()).toEqual(['gpt-5', 'video-b-1'])
})
