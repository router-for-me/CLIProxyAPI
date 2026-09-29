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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { PluginsTable } from '../components/plugins-table'
import type { TaskPluginListItem } from '../types'

vi.mock('@/lib/lobe-icon', () => ({ getLobeIcon: () => null }))

const clients: QueryClient[] = []
afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  localStorage.removeItem('task-plugins-view-mode')
})

function renderPlugins(
  enabled: boolean,
  view: 'card' | 'table' = 'card',
  source: TaskPluginListItem['source'] = 'override'
) {
  localStorage.setItem('task-plugins-view-mode', view)
  const item: TaskPluginListItem = {
    meta: {
      key: 'example',
      name: 'Example',
      version: '1.0.0',
      apiVersion: 1,
      author: { name: 'Example' },
      models: [],
      fetchMode: 'per_task',
    },
    source,
    enabled,
    active: true,
    source_hash: '',
    remark: '',
    runtime_status: enabled ? 'registered' : 'disabled',
    channel_count: 0,
    in_flight_count: 0,
  }
  if (source === 'override_over_factory') {
    item.factory_meta = { ...item.meta, version: '0.9.0' }
  }
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  })
  clients.push(client)
  client.setQueryData(['task-plugins'], [item])
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [{ ...item, enabled: !enabled }] },
  })
  render(
    <QueryClientProvider client={client}>
      <PluginsTable onDetails={() => undefined} onUpload={() => undefined} />
    </QueryClientProvider>
  )
  return screen.getAllByRole('switch', { name: 'Enable plugin example' })[0]
}

test.each([
  ['card', true],
  ['card', false],
  ['table', true],
  ['table', false],
] as const)(
  '%s view: changing enabled=%s requires confirmation and cancellation preserves status',
  async (view, enabled) => {
    const user = userEvent.setup()
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: null } })
    const toggle = renderPlugins(enabled, view)
    await user.click(toggle)
    expect(post).not.toHaveBeenCalled()
    expect(toggle).toHaveAttribute('aria-checked', String(enabled))
    const title = enabled ? 'Disable plugin?' : 'Enable plugin?'
    const dialog = await screen.findByRole('alertdialog', { name: title })
    expect(dialog).toHaveTextContent('Example')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(post).not.toHaveBeenCalled()
    expect(toggle).toHaveAttribute('aria-checked', String(enabled))
    await user.click(
      screen.getAllByRole('switch', { name: 'Enable plugin example' })[0]
    )
    await user.click(
      within(await screen.findByRole('alertdialog', { name: title })).getByRole(
        'button',
        { name: enabled ? 'Disable' : 'Enable' }
      )
    )
    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/api/plugin/task/example/status',
        { enabled: !enabled },
        expect.any(Object)
      )
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(
      screen.getAllByRole('switch', { name: 'Enable plugin example' })[0]
    ).toHaveAttribute('aria-checked', String(!enabled))
  }
)

test('keyboard toggle opens confirmation and Escape cancels without a request', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  const toggle = renderPlugins(true)
  act(() => toggle.focus())
  await user.keyboard(' ')
  await screen.findByRole('alertdialog', { name: 'Disable plugin?' })
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(post).not.toHaveBeenCalled()
  expect(toggle).toHaveAttribute('aria-checked', 'true')
})

test('failed status request leaves confirmation available to retry and preserves the switch', async () => {
  const user = userEvent.setup()
  let finish!: (value: { data: { success: boolean; message: string } }) => void
  const post = vi.spyOn(api, 'post').mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  const toggle = renderPlugins(true)
  await user.click(toggle)
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Disable plugin?',
  })
  const confirm = within(dialog).getByRole('button', { name: 'Disable' })
  await user.click(confirm)
  expect(confirm).toBeDisabled()
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled()
  expect(screen.getAllByRole('switch', { hidden: true })[0]).toHaveAttribute(
    'aria-checked',
    'true'
  )
  await user.click(confirm)
  expect(post).toHaveBeenCalledTimes(1)
  await act(async () =>
    finish({ data: { success: false, message: 'Status update failed' } })
  )
  await waitFor(() => expect(confirm).toBeEnabled())
  expect(toggle).toHaveAttribute('aria-checked', 'true')
  expect(dialog).toBeInTheDocument()
})

test('confirmed disable blocked by usage opens the existing cascade confirmation', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: {
        success: false,
        message: 'Plugin in use',
        data: {
          channels: [{ id: 7, name: 'Video channel' }],
          in_flight_count: 1,
        },
      },
    })
    .mockResolvedValueOnce({ data: { success: true, data: null } })
  await user.click(renderPlugins(true))
  await user.click(
    within(
      await screen.findByRole('alertdialog', { name: 'Disable plugin?' })
    ).getByRole('button', { name: 'Disable' })
  )
  const usage = await screen.findByRole('alertdialog', {
    name: 'Plugin is still in use',
  })
  expect(
    screen.queryByRole('alertdialog', { name: 'Disable plugin?' })
  ).not.toBeInTheDocument()
  expect(usage).toHaveTextContent('Video channel')
  await user.click(
    within(usage).getByRole('button', { name: 'Cascade disable channels' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenLastCalledWith(
      '/api/plugin/task/example/status',
      { enabled: false },
      expect.objectContaining({ params: { cascade: true } })
    )
  )
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
})

test('usage dialog offers one cancel button and force operation posts force params', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: {
        success: false,
        message: 'Plugin in use',
        data: {
          channels: [{ id: 7, name: 'Video channel' }],
          in_flight_count: 0,
        },
      },
    })
    .mockResolvedValueOnce({ data: { success: true, data: null } })
  await user.click(renderPlugins(true))
  await user.click(
    within(
      await screen.findByRole('alertdialog', { name: 'Disable plugin?' })
    ).getByRole('button', { name: 'Disable' })
  )
  const usage = await screen.findByRole('alertdialog', {
    name: 'Plugin is still in use',
  })
  expect(within(usage).getAllByRole('button', { name: 'Cancel' })).toHaveLength(
    1
  )
  await user.click(
    within(usage).getByRole('button', { name: 'Force operation' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenLastCalledWith(
      '/api/plugin/task/example/status',
      { enabled: false },
      expect.objectContaining({ params: { cascade: true, force: true } })
    )
  )
})

test.each(['factory', 'override_over_factory'] as const)(
  '%s deletion protects the factory plugin while allowing custom version removal',
  async (source) => {
    const user = userEvent.setup()
    renderPlugins(true, 'table', source)
    await user.click(screen.getAllByRole('button', { name: 'Open menu' })[0])
    const remove = screen.getByRole('menuitem', {
      name: 'Delete active custom version',
    })
    if (source === 'factory') {
      expect(remove).toHaveAttribute('aria-disabled', 'true')
    } else {
      expect(remove).not.toHaveAttribute('aria-disabled', 'true')
      await user.click(remove)
      expect(
        await screen.findByRole('alertdialog', {
          name: 'Delete plugin version?',
        })
      ).toBeVisible()
    }
  }
)
