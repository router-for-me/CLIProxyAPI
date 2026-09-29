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
import type { Row } from '@tanstack/react-table'
import { render, screen, waitFor, cleanup, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { pricingOptions } from '@/features/model-pricing/pricing'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { DataTableRowActions } from '../components/data-table-row-actions'
import { ModelMutateDrawer } from '../components/drawers/model-mutate-drawer'
import { ModelsDialogs } from '../components/models-dialogs'
import { ModelsProvider } from '../components/models-provider'
import type { Model } from '../types'

const model = {
  id: 7,
  model_name: 'example-model',
  description: 'Original',
  status: 1,
  sync_official: 1,
  name_rule: 0,
  vendor_id: 3,
  endpoints: '',
  supported_endpoints: ['openai'],
  created_time: 1,
  updated_time: 1,
}

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

function renderModelActions(currentModel: Model = model, role = 100) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelsProvider>
        <DataTableRowActions row={{ original: currentModel } as Row<Model>} />
        <ModelsDialogs />
      </ModelsProvider>
    </QueryClientProvider>
  )
  return client
}

describe('model pricing entry', () => {
  it('saves pricing and opens connections for a channel model without creating metadata', async () => {
    const channelModel = { ...model, id: 0, model_name: 'channel-only' }
    let storedPrice = 1.5
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: 'channel-only',
                  version: 'v1',
                  configured: { ModelPrice: storedPrice },
                  effective: { ModelPrice: storedPrice },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const post = vi.spyOn(api, 'post')
    const put = vi.spyOn(api, 'put')
    const patch = vi.spyOn(api, 'patch').mockImplementation(async () => {
      storedPrice = 0
      return { data: { success: true } }
    })
    const client = renderModelActions(channelModel)
    const user = userEvent.setup()
    expect(screen.getByRole('button', { name: 'Add metadata' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Open menu' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    expect(screen.getByRole('tab', { name: 'Pricing' })).not.toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(
      await screen.findByRole('button', { name: 'Save model prices' })
    ).toBeVisible()
    const price = await screen.findByPlaceholderText('0.01')
    await user.clear(price)
    await user.type(price, '0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'channel-only',
            expected_version: 'v1',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    expect(put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('tab', { name: 'Channels and groups' }))
    expect(
      screen.getByText(
        'Channel availability and group access are derived from enabled channels. Importing metadata does not create a callable channel.'
      )
    ).toBeVisible()
    expect(get).not.toHaveBeenCalledWith('/api/models/0')
    expect(post).not.toHaveBeenCalled()
    client.clear()
  })

  it('opens pricing directly, keeps it selected after metadata loads, and reopens Edit on metadata', async () => {
    let resolveDetail!: (value: Awaited<ReturnType<typeof api.get>>) => void
    const detail = new Promise<Awaited<ReturnType<typeof api.get>>>(
      (resolve) => {
        resolveDetail = resolve
      }
    )
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') return detail
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: model.model_name,
                  version: 'v1',
                  configured: { ModelRatio: 3.25, CompletionRatio: 27 / 6.5 },
                  effective: { ModelRatio: 3.25, CompletionRatio: 27 / 6.5 },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const client = renderModelActions()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    expect(screen.getByRole('tab', { name: 'Pricing' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await act(async () =>
      resolveDetail({ data: { success: true, data: model } })
    )
    expect(screen.getByRole('tab', { name: 'Pricing' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(
      await screen.findByRole('textbox', { name: 'Input price' })
    ).toHaveValue('6.5')
    expect(
      screen.queryByRole('heading', { name: 'Edit model pricing' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('textbox', { name: 'Model name' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: 'Preview' })).toBeVisible()
    for (const tab of ['Model metadata', 'Channels and groups', 'Pricing']) {
      expect(screen.getByRole('tab', { name: tab })).toHaveClass(
        'min-w-0',
        'whitespace-normal'
      )
      await user.click(screen.getByRole('tab', { name: tab }))
      expect(
        screen.getByRole('dialog', { name: model.model_name })
      ).toHaveClass('sm:max-w-[1280px]')
    }
    await user.click(screen.getByRole('button', { name: 'Close' }))
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(await screen.findByLabelText('Description')).toHaveValue('Original')
    expect(screen.getByRole('tab', { name: 'Model metadata' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    client.clear()
  })

  it('does not expose a pricing shortcut to an ordinary administrator', () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [] } },
    })
    const client = renderModelActions(model, 10)
    expect(screen.getByRole('button', { name: 'Edit' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Pricing' })
    ).not.toBeInTheDocument()
    client.clear()
  })

  it('requires a concrete model for matching rules and protects an unsaved price on close', async () => {
    const matchedModel = {
      ...model,
      name_rule: 1,
      matched_models: ['example-concrete'],
    }
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: matchedModel } }
      }
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: 'example-concrete',
                  version: 'v1',
                  configured: { ModelPrice: 1.5 },
                  effective: { ModelPrice: 1.5 },
                },
              ],
              options: pricingOptions({}),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: { items: [] } } }
    })
    const client = renderModelActions(matchedModel)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Pricing' }))
    await user.click(
      await screen.findByRole('combobox', { name: 'Select model' })
    )
    expect(
      get.mock.calls.some(([url]) => url === '/api/option/model_pricing')
    ).toBe(false)
    await user.click(
      await screen.findByRole('option', { name: 'example-concrete' })
    )
    const price = await screen.findByPlaceholderText('0.01')
    await waitFor(() => expect(price).toHaveValue('1.5'))
    await user.clear(price)
    await user.type(price, '2')
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(await screen.findByRole('alertdialog')).toHaveTextContent(
      'Discard unsaved changes?'
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(price).toHaveValue('2')
    client.clear()
  })
})

describe('metadata editing', () => {
  it.each([
    {
      name: 'business rejection',
      response: { success: false, message: '模型名称已存在' },
    },
    { name: 'HTTP rejection', response: null },
  ])(
    'shows the server reason for a $name and preserves the draft for retry',
    async ({ response }) => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'admin', role: 10 })
      vi.spyOn(api, 'get').mockResolvedValue({
        data: { success: true, data: { items: [] } },
      })
      const post = vi.spyOn(api, 'post')
      if (response) {
        post.mockResolvedValueOnce({ data: response })
      } else {
        const error = new AxiosError('Request failed with status code 409')
        error.response = {
          data: { message: '模型名称已存在' },
          status: 409,
          statusText: 'Conflict',
          headers: {},
          config: { headers: {} },
        } as typeof error.response
        post.mockRejectedValueOnce(error)
      }
      post.mockResolvedValue({ data: { success: true } })
      const close = vi.fn()
      const fallbackError = vi.fn()
      const client = new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false, onError: fallbackError },
        },
      })
      render(
        <QueryClientProvider client={client}>
          <ModelMutateDrawer open onOpenChange={close} />
        </QueryClientProvider>
      )
      const user = userEvent.setup()
      await user.type(screen.getByLabelText('Model Name *'), 'duplicate-model')
      await user.type(screen.getByLabelText('Description'), 'Keep this draft')
      await user.click(screen.getByRole('button', { name: 'Save metadata' }))
      expect(await screen.findByRole('alert')).toHaveTextContent(
        '模型名称已存在'
      )
      expect(fallbackError).not.toHaveBeenCalled()
      expect(close).not.toHaveBeenCalled()
      expect(screen.getByLabelText('Model Name *')).toHaveValue(
        'duplicate-model'
      )
      expect(screen.getByLabelText('Description')).toHaveValue(
        'Keep this draft'
      )
      await user.clear(screen.getByLabelText('Model Name *'))
      await user.type(screen.getByLabelText('Model Name *'), 'unique-model')
      await user.click(screen.getByRole('button', { name: 'Save metadata' }))
      await waitFor(() => expect(close).toHaveBeenCalledWith(false))
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(post).toHaveBeenLastCalledWith(
        '/api/models/',
        expect.objectContaining({
          model_name: 'unique-model',
          description: 'Keep this draft',
        }),
        { skipBusinessError: true, skipErrorHandler: true }
      )
      client.clear()
    }
  )

  it('allows an administrator to save metadata without loading or changing system pricing', async () => {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'admin', role: 10 })
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: model } }
      }
      if (url === '/api/vendors/') {
        return {
          data: {
            success: true,
            data: {
              items: [
                { id: 3, name: 'Existing vendor', icon: 'Gemini.Color' },
                { id: 4, name: 'Another vendor', icon: 'Gemini.Color' },
              ],
            },
          },
        }
      }
      return { data: { success: false, message: 'Root only' } }
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: model } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelsProvider>
          <ModelMutateDrawer open onOpenChange={() => {}} currentRow={model} />
        </ModelsProvider>
      </QueryClientProvider>
    )
    const description = await screen.findByLabelText('Description')
    await waitFor(() => expect(description).toHaveValue('Original'))
    expect(screen.getByRole('combobox', { name: 'Vendor' })).toHaveValue(
      'Existing vendor'
    )
    const user = userEvent.setup()
    expect(screen.getByText('Gemini.Color')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Custom model icon' }))
    const icon = screen.getByRole('combobox', { name: 'Icon' })
    await user.type(icon, 'Claude.Avatar')
    await user.keyboard('{Escape}')
    expect(screen.getByText('Claude.Avatar')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Inherit vendor icon' })
    )
    expect(
      screen.queryByRole('combobox', { name: 'Icon' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('Gemini.Color')).toBeVisible()
    const vendorInput = screen.getByRole('combobox', { name: 'Vendor' })
    await user.click(vendorInput)
    await user.type(vendorInput, 'Another')
    await user.click(screen.getByRole('option', { name: 'Another vendor' }))
    expect(vendorInput).toHaveValue('Another vendor')
    await user.clear(description)
    await user.type(description, 'Updated metadata')
    await user.click(
      screen.getByRole('button', { name: /Update Model|Save metadata/ })
    )
    await waitFor(() => expect(put).toHaveBeenCalled())
    expect(
      get.mock.calls.some(([url]) => String(url).startsWith('/api/option'))
    ).toBe(false)
    expect(put.mock.calls.every(([url]) => url === '/api/models/')).toBe(true)
    expect(put.mock.calls[0][1]).toMatchObject({
      description: 'Updated metadata',
      icon: '',
      model_name: 'example-model',
      vendor_id: 4,
      endpoints: '',
    })
  })
  it('keeps metadata drafts while saving pricing independently and preserves the price draft across tabs', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
    let version = 'v1'
    let storedPrice = 1.5
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/7') {
        return { data: { success: true, data: model } }
      }
      if (url === '/api/vendors/') {
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 3, name: 'Existing vendor', icon: 'Gemini.Color' }],
            },
          },
        }
      }
      if (url === '/api/option/model_pricing') {
        return {
          data: {
            success: true,
            data: {
              entries: [
                {
                  model_name: model.model_name,
                  version,
                  configured: { ModelPrice: storedPrice },
                  effective: { ModelPrice: storedPrice },
                },
              ],
              options: pricingOptions({ ModelPrice: '{"example-model":1.5}' }),
              empty_version: 'empty',
            },
          },
        }
      }
      return { data: { success: true, data: [], vendors: [] } }
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, data: model } })
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValueOnce({
        data: {
          success: false,
          message: 'Model pricing changed; reload before saving',
        },
      })
      .mockResolvedValue({ data: { success: true } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelsProvider>
          <ModelMutateDrawer open onOpenChange={() => {}} currentRow={model} />
        </ModelsProvider>
      </QueryClientProvider>
    )
    const description = await screen.findByLabelText('Description')
    await waitFor(() => expect(description).toHaveValue('Original'))
    const user = userEvent.setup()
    await user.clear(description)
    await user.type(description, 'Unsaved metadata draft')
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    const price = await screen.findByPlaceholderText('0.01')
    await waitFor(() => expect(price).toHaveValue('1.5'))
    await user.clear(price)
    await user.type(price, '0')
    await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
    expect(screen.getByLabelText('Description')).toHaveValue(
      'Unsaved metadata draft'
    )
    await user.click(screen.getByRole('tab', { name: 'Pricing' }))
    expect(screen.getByPlaceholderText('0.01')).toHaveValue('0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'example-model',
            expected_version: 'v1',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    version = 'v2'
    storedPrice = 2
    await user.click(
      await screen.findByRole('button', { name: 'Reload pricing' })
    )
    await waitFor(() =>
      expect(screen.getByPlaceholderText('0.01')).toHaveValue('2')
    )
    await user.clear(screen.getByPlaceholderText('0.01'))
    await user.type(screen.getByPlaceholderText('0.01'), '0')
    await user.click(screen.getByRole('button', { name: 'Save model prices' }))
    await waitFor(() =>
      expect(patch).toHaveBeenLastCalledWith('/api/option/model_pricing', {
        changes: [
          {
            model_name: 'example-model',
            expected_version: 'v2',
            pricing: { ModelPrice: 0, 'billing_setting.billing_mode': 'ratio' },
            reset: false,
          },
        ],
      })
    )
    expect(put).not.toHaveBeenCalled()
    expect(
      get.mock.calls.some(([url]) => url === '/api/option/model_pricing')
    ).toBe(true)
    await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
    expect(screen.getByLabelText('Description')).toHaveValue(
      'Unsaved metadata draft'
    )
    client.clear()
  })
})
