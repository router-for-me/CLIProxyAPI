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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { VendorMutateDialog } from '../components/dialogs/vendor-mutate-dialog'
import { VendorOperationDialog } from '../components/dialogs/vendor-operation-dialog'
import { VendorLinkedModels } from '../components/vendor-linked-models'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))

const vendor = {
  id: 3,
  name: 'Google',
  icon: 'Gemini.Color',
  description: 'Original',
  status: 0,
  created_time: 10,
  updated_time: 20,
  model_count: 2,
  version: 'vendor-v1',
}
const target = { ...vendor, id: 4, name: 'Target', description: 'Preserved' }
const preview = {
  action: 'assign',
  sources: [vendor],
  target: null,
  models: [
    {
      id: 7,
      model_name: 'example-model',
      name_rule: 0,
      vendor_id: 3,
      vendor_name: 'Google',
      updated_time: 20,
    },
  ],
  version: 'preview-v1',
}
function renderWithClient(component: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{component}</QueryClientProvider>)
  return client
}
function conflict() {
  return new AxiosError('Conflict', '409', undefined, undefined, {
    data: { success: false, code: 'VENDOR_CONFLICT' },
    status: 409,
    statusText: 'Conflict',
    headers: {},
    config: { headers: {} },
  } as never)
}

describe('vendor management', () => {
  it('edits only vendor metadata using the saved version and protects unsaved changes', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: vendor },
    })
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, data: { ...vendor, name: 'Updated' } },
    })
    const close = vi.fn()
    renderWithClient(
      <VendorMutateDialog open currentVendor={vendor} onOpenChange={close} />
    )
    const name = await screen.findByLabelText('Vendor Name *')
    await waitFor(() => expect(name).toHaveValue('Google'))
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Icon' })).toHaveValue(
      'Gemini.Color'
    )
    const user = userEvent.setup()
    await user.clear(name)
    await user.type(name, '  Updated  ')
    await user.click(screen.getAllByRole('button', { name: 'Close' })[0])
    expect(await screen.findByText('Discard unsaved changes?')).toBeVisible()
    expect(close).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Save metadata' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/vendors/', {
        id: 3,
        name: 'Updated',
        description: 'Original',
        icon: 'Gemini.Color',
        version: 'vendor-v1',
      })
    )
    await waitFor(() => expect(close).toHaveBeenCalledWith(false))
  })
  it('requires a preview and a fresh version after a conflict when clearing model assignments', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [vendor, target] } },
    })
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce({ data: { success: true, data: preview } })
      .mockRejectedValueOnce(conflict())
      .mockResolvedValueOnce({
        data: { success: true, data: { ...preview, version: 'preview-v2' } },
      })
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { updated_models: [7], deleted_vendors: [] },
        },
      })
    const close = vi.fn()
    const client = renderWithClient(
      <VendorOperationDialog
        selection={{ action: 'assign', model_ids: [7], target_vendor_id: 0 }}
        onClose={close}
      />
    )
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    expect(post).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Apply changes' })
    ).not.toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Preview changes' }))
    expect(await screen.findByText('example-model')).toBeVisible()
    expect(post).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Apply changes' }))
    expect(
      await screen.findByText(
        'Vendor data changed. Preview again before applying.'
      )
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Apply changes' })).toBeDisabled()
    expect(close).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Preview again' }))
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Apply changes' })
      ).toBeEnabled()
    )
    await user.click(screen.getByRole('button', { name: 'Apply changes' }))
    await waitFor(() => expect(close).toHaveBeenCalled())
    expect(post.mock.calls.at(-1)).toEqual([
      '/api/vendors/operations',
      {
        action: 'assign',
        model_ids: [7],
        target_vendor_id: 0,
        expected_version: 'preview-v2',
      },
    ])
    for (const key of ['vendors', 'models', 'pricing']) {
      expect(invalidate).toHaveBeenCalledWith({ queryKey: [key] })
    }
  })
  it('requires an explicit merge target and previews only the remaining source vendors', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [vendor, target] } },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          ...preview,
          action: 'merge',
          target: vendor,
          sources: [target],
        },
      },
    })
    renderWithClient(
      <VendorOperationDialog
        selection={{ action: 'merge', vendor_ids: [3, 4] }}
        onClose={() => {}}
      />
    )
    expect(
      screen.getByRole('button', { name: 'Preview changes' })
    ).toBeDisabled()
    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: 'Keep this vendor' }))
    await user.click(await screen.findByRole('option', { name: 'Google' }))
    await user.click(screen.getByRole('button', { name: 'Preview changes' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/vendors/operations/preview', {
        action: 'merge',
        vendor_ids: [4],
        target_vendor_id: 3,
      })
    )
    expect(post).toHaveBeenCalledTimes(1)
  })
  it('renders and searches linked records without conflating rule coverage with record count', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          items: [
            {
              id: 7,
              model_name: 'example-*',
              name_rule: 1,
              matched_count: 24,
              vendor_id: 3,
            },
          ],
          total: 1,
        },
      },
    })
    renderWithClient(
      <VendorLinkedModels
        vendor={{ ...vendor, model_count: 1 }}
        onNavigate={() => {}}
      />
    )
    expect(
      await screen.findByRole('button', { name: 'example-*' })
    ).toBeVisible()
    expect(screen.getByText('1 linked model records')).toBeVisible()
    expect(screen.getByRole('cell', { name: '24' })).toBeVisible()
    const user = userEvent.setup()
    await user.type(
      screen.getByPlaceholderText('Filter by model name...'),
      'example'
    )
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith('/api/models/search', {
        params: { vendor: '3', keyword: 'example', p: 1, page_size: 10 },
      })
    )
  })
  it('keeps the requested linked-model page while its response is pending', async () => {
    let finishPage: ((value: unknown) => void) | undefined
    const get = vi
      .spyOn(api, 'get')
      .mockImplementation(async (_url, config) => {
        if (config?.params?.p === 2) {
          return new Promise((resolve) => {
            finishPage = resolve
          })
        }
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 1, model_name: 'first-page-model', name_rule: 0 }],
              total: 11,
            },
          },
        }
      })
    renderWithClient(
      <VendorLinkedModels
        vendor={{ ...vendor, model_count: 11 }}
        onNavigate={() => {}}
      />
    )
    expect(
      await screen.findByRole('button', { name: 'first-page-model' })
    ).toBeVisible()
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Go to next page' }))
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/models/search', {
        params: { vendor: '3', keyword: '', p: 2, page_size: 10 },
      })
    )
    finishPage?.({
      data: {
        success: true,
        data: {
          items: [{ id: 11, model_name: 'second-page-model', name_rule: 0 }],
          total: 11,
        },
      },
    })
    expect(
      await screen.findByRole('button', { name: 'second-page-model' })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'first-page-model' })
    ).not.toBeInTheDocument()
  })
  it('shows linked record counts when the server refuses batch deletion', async () => {
    vi.spyOn(api, 'post').mockRejectedValue(
      new AxiosError('Referenced', '409', undefined, undefined, {
        data: { code: 'VENDOR_REFERENCED', reference_counts: { 3: 2 } },
        status: 409,
      } as never)
    )
    renderWithClient(
      <VendorOperationDialog
        selection={{ action: 'delete', vendor_ids: [3, 4] }}
        onClose={() => {}}
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Preview changes' }))
    expect(
      await screen.findByText(
        'Vendors still have 2 linked model records. Transfer or clear their assignments first.'
      )
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Apply changes' })
    ).not.toBeInTheDocument()
  })
})
