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
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SystemTasksPanel } from '../components/system-tasks-panel'

const task = {
  id: 1,
  task_id: 'history-task',
  type: 'model_update',
  status: 'succeeded',
  created_at: 100,
  updated_at: 200,
  locked_by: 'history-runner',
}

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SystemTasksPanel />
      <Toaster />
    </QueryClientProvider>
  )
  return client
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

it('filters history on the server and resets pagination without hiding active tasks', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    if (config?.params?.scope === 'active') {
      return {
        data: {
          success: true,
          data: [
            {
              ...task,
              task_id: 'active',
              status: 'running',
              locked_by: 'active-runner',
            },
          ],
          total: 1,
        },
      }
    }
    return { data: { success: true, data: [task], total: 21 } }
  })
  const client = renderPanel()
  await screen.findByRole('combobox', { name: 'Type' })
  await userEvent.click(
    await screen.findByRole('button', { name: 'Go to next page' })
  )
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      '/api/system-task/list',
      expect.objectContaining({
        params: expect.objectContaining({ scope: 'history', offset: 20 }),
      })
    )
  )
  await userEvent.selectOptions(
    screen.getByRole('combobox', { name: 'Type' }),
    'model_update'
  )
  await userEvent.selectOptions(
    screen.getByRole('combobox', { name: 'Status' }),
    'failed'
  )
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/system-task/list',
      expect.objectContaining({
        params: expect.objectContaining({
          scope: 'history',
          type: 'model_update',
          status: 'failed',
          offset: 0,
        }),
      })
    )
  )
  expect(screen.getByText('active-runner')).toBeVisible()
  client.clear()
})

it('keeps filters available and disables cleanup when history is empty', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [], total: 0 },
  })
  const client = renderPanel()
  expect(await screen.findByText('No historical system tasks.')).toBeVisible()
  expect(screen.getByRole('combobox', { name: 'Type' })).toBeEnabled()
  expect(
    screen.getByRole('button', { name: 'Clean task history' })
  ).toBeDisabled()
  client.clear()
})

it('requires confirmation and cleans all matching history pages using the selected filters', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (_url, config) => ({
    data: {
      success: true,
      data: config?.params?.scope === 'active' ? [] : [task],
      total: 21,
    },
  }))
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true, data: { deleted_count: 20 } } })
  const client = renderPanel()
  await userEvent.selectOptions(
    await screen.findByRole('combobox', { name: 'Type' }),
    'model_update'
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Clean task history' })
    ).toBeEnabled()
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Clean task history' })
  )
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByText(/latest run of each task type/)).toBeVisible()
  expect(remove).not.toHaveBeenCalled()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(remove).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Clean task history' })
  )
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('/api/system-task/history', {
      params: { type: 'model_update', status: '' },
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  client.clear()
})

it('retains the confirmation and shows the server error when cleanup fails', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [task], total: 1 },
  })
  let finishCleanup!: (response: {
    data: { success: boolean; message: string }
  }) => void
  const response = new Promise<{ data: { success: boolean; message: string } }>(
    (resolve) => {
      finishCleanup = resolve
    }
  )
  vi.spyOn(api, 'delete').mockReturnValue(response)
  const client = renderPanel()
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Clean task history' })
    ).toBeEnabled()
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Clean task history' })
  )
  const dialog = screen.getByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(
    within(dialog).getByRole('button', { name: 'Deleting...' })
  ).toBeDisabled()
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled()
  finishCleanup({
    data: { success: false, message: 'History cleanup unavailable' },
  })
  expect(await screen.findByText('History cleanup unavailable')).toBeVisible()
  expect(within(dialog).getByRole('button', { name: 'Delete' })).toBeEnabled()
  client.clear()
})

it('shows history query failures without hiding active tasks and allows retry', async () => {
  let historyFailed = true
  vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    if (config?.params?.scope === 'history' && historyFailed) {
      return { data: { success: false, message: 'History unavailable' } }
    }
    return {
      data: {
        success: true,
        data: [
          {
            ...task,
            locked_by:
              config?.params?.scope === 'active'
                ? 'active-runner'
                : 'history-runner',
          },
        ],
        total: 1,
      },
    }
  })
  const client = renderPanel()
  expect(await screen.findByText('History unavailable')).toBeVisible()
  expect(screen.getByText('active-runner')).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Clean task history' })
  ).toBeDisabled()
  historyFailed = false
  await userEvent.click(
    screen.getByRole('button', { name: /retry|try again/i })
  )
  expect(await screen.findByText('history-runner')).toBeVisible()
  client.clear()
})

it('refreshes history when the last running task finishes', async () => {
  let finished = false
  vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    const active = config?.params?.scope === 'active'
    const data =
      active === finished
        ? []
        : [{ ...task, status: finished ? 'succeeded' : 'running' }]
    return { data: { success: true, data, total: data.length } }
  })
  const client = renderPanel()
  await screen.findByText('No historical system tasks.')
  finished = true
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('No active system tasks.')).toBeVisible()
  await waitFor(() =>
    expect(
      screen.queryByText('No historical system tasks.')
    ).not.toBeInTheDocument()
  )
  expect(screen.getByText('history-runner')).toBeVisible()
  client.clear()
})
