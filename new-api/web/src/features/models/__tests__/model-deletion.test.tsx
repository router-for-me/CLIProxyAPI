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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelDeleteDialog } from '../components/dialogs/model-delete-dialog'

const models = [
  { id: 7, model_name: 'example-model', name_rule: 0 },
  { id: 8, model_name: 'another-model', name_rule: 0 },
]
function Fixture(props: { batch?: boolean; onSuccess?: () => void }) {
  const [open, setOpen] = useState(true)
  return (
    <>
      <button type='button' onClick={() => setOpen(true)}>
        Open deletion
      </button>
      {open && (
        <ModelDeleteDialog
          models={props.batch ? models : models.slice(0, 1)}
          onClose={() => setOpen(false)}
          onSuccess={props.onSuccess}
        />
      )}
    </>
  )
}
function mount(batch = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const onSuccess = vi.fn()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  render(
    <QueryClientProvider client={client}>
      <Fixture batch={batch} onSuccess={onSuccess} />
    </QueryClientProvider>
  )
  return { onSuccess, invalidate }
}
afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

describe('model deletion', () => {
  it('defaults to keeping channels and resets the option after cancelling', async () => {
    const remove = vi.spyOn(api, 'delete').mockResolvedValue({
      data: {
        success: true,
        data: { deleted_count: 1, updated_channels: 0 },
      },
    })
    const { onSuccess, invalidate } = mount()
    const user = userEvent.setup()
    expect(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    ).not.toBeChecked()
    await user.click(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(remove).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Open deletion' }))
    expect(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    ).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
    expect(remove).toHaveBeenCalledWith('/api/models/7', {
      params: { remove_from_channels: false, remove_pricing: false },
    })
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ['channels'] })
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('submits one batch with channel removal and preserves selection after failure for retry', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Channel update failed' },
      })
      .mockResolvedValue({
        data: {
          success: true,
          data: { deleted_count: 2, updated_channels: 3 },
        },
      })
    const { onSuccess, invalidate } = mount(true)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    )
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Channel update failed'
    )
    expect(onSuccess).not.toHaveBeenCalled()
    expect(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    ).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
    expect(post).toHaveBeenLastCalledWith('/api/models/delete', {
      model_ids: [7, 8],
      remove_from_channels: true,
      remove_pricing: false,
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['channels'] })
  })

  it('disables dismissal and repeated submission while a single removal is pending', async () => {
    let complete!: (response: unknown) => void
    const remove = vi.spyOn(api, 'delete').mockImplementation(
      () =>
        new Promise((resolve) => {
          complete = resolve
        })
    )
    const { onSuccess } = mount()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    )
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('button', { name: 'Delete' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(
      screen.getByRole('checkbox', { name: 'Also remove from all channels' })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.keyboard('{Escape}')
    expect(screen.getByRole('alertdialog')).toBeVisible()
    expect(remove).toHaveBeenCalledTimes(1)
    expect(remove).toHaveBeenCalledWith('/api/models/7', {
      params: { remove_from_channels: true, remove_pricing: false },
    })
    complete({
      data: { success: true, data: { deleted_count: 1, updated_channels: 2 } },
    })
    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
  })
})

it('lets a super administrator remove pricing independently of channel removal', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { deleted_count: 2, updated_channels: 0 } },
  })
  const { onSuccess, invalidate } = mount(true)
  const user = userEvent.setup()
  expect(
    screen.getByRole('checkbox', { name: 'Also remove pricing' })
  ).not.toBeChecked()
  await user.click(
    screen.getByRole('checkbox', { name: 'Also remove pricing' })
  )
  expect(
    screen.getByText(/Built-in pricing may become effective again/)
  ).toBeVisible()
  expect(screen.queryByText(/Pricing will be retained/)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Delete' }))
  await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
  expect(post).toHaveBeenCalledWith('/api/models/delete', {
    model_ids: [7, 8],
    remove_from_channels: false,
    remove_pricing: true,
  })
  expect(invalidate).toHaveBeenCalledWith({
    queryKey: ['model-pricing-config'],
  })
  expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ['channels'] })
})

it('keeps pricing removal unavailable to an ordinary administrator', () => {
  useAuthStore.getState().auth.setUser({ id: 2, username: 'admin', role: 10 })
  mount()
  expect(
    screen.getByRole('checkbox', { name: 'Also remove pricing' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(
    screen.getByText('Model pricing is managed by a super administrator.')
  ).toBeVisible()
})

it.each([1, 2, 3])(
  'disables channel removal when selection includes matching rule %i',
  (rule) => {
    const client = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <ModelDeleteDialog
          models={[models[1], { ...models[0], name_rule: rule }]}
          onClose={() => {}}
        />
      </QueryClientProvider>
    )
    expect(
      screen.getByRole('checkbox', { name: /Also remove from all channels/ })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByText('Only available for exact matching')).toBeVisible()
  }
)
