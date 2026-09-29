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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const pointerCaptureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

function FilterFixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <UsageLogsProvider>
      <CommonLogsFilterBar table={table} />
    </UsageLogsProvider>
  )
}

async function renderFilter(
  initialEntry = '/usage-logs/common',
  groups: Record<string, { desc: string; ratio: number }> | null = {
    default: { desc: '', ratio: 1 },
    premium: { desc: '', ratio: 2 },
  }
) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/self/groups' || url === '/api/group/') {
      if (groups === null) throw new Error('Group loading failed')
      return {
        data: {
          success: true,
          data: url === '/api/group/' ? Object.keys(groups) : groups,
        },
      }
    }
    return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  if (window.matchMedia('(max-width: 640px)').matches) {
    await userEvent.click(
      await screen.findByRole('button', { name: /^Filter/ })
    )
  }
  await screen.findByRole('combobox', { name: 'Group' })
  return router
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.setUser(null)
  if (pointerCaptureDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      pointerCaptureDescriptor
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
  }
})

it('loads personal groups and filters choices without submitting until Search', async () => {
  const router = await renderFilter()
  const input = screen.getByRole('combobox', { name: 'Group' })
  await userEvent.click(input)
  expect(await screen.findByRole('option', { name: 'default' })).toBeVisible()
  await userEvent.type(input, 'prem')
  expect(
    screen.queryByRole('option', { name: 'default' })
  ).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('option', { name: 'premium' }))
  expect(input).toHaveValue('premium')
  expect(router.state.location.search).not.toHaveProperty('group')
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      group: 'premium',
      page: 1,
    })
  )
  expect(api.get).toHaveBeenCalledWith('/api/user/self/groups')
  expect(api.get).not.toHaveBeenCalledWith('/api/group/')
})

it('loads all groups in the administrator view', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  await renderFilter()
  await userEvent.click(screen.getByRole('combobox', { name: 'Group' }))
  expect(await screen.findByRole('option', { name: 'premium' })).toBeVisible()
  expect(api.get).toHaveBeenCalledWith('/api/group/')
  expect(api.get).not.toHaveBeenCalledWith('/api/user/self/groups')
})

it('confirms a keyboard choice before Enter submits the selected group', async () => {
  const router = await renderFilter()
  const input = screen.getByRole('combobox', { name: 'Group' })
  await userEvent.click(input)
  await screen.findByRole('option', { name: 'default' })
  await userEvent.keyboard('{ArrowDown}{Enter}')
  expect(input).toHaveValue('default')
  expect(input).toHaveAttribute('aria-expanded', 'false')
  expect(router.state.location.search).not.toHaveProperty('group')
  await userEvent.keyboard('{Enter}')
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ group: 'default' })
  )
})

it.each([{}, null])(
  'preserves historical input and supports clearing when groups are unavailable (%s)',
  async (groups) => {
    const router = await renderFilter(
      '/usage-logs/common?group=retired',
      groups
    )
    const input = screen.getByRole('combobox', { name: 'Group' })
    expect(input).toHaveValue('retired')
    await userEvent.clear(input)
    await userEvent.type(input, 'historical')
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({
        group: 'historical',
      })
    )
    await userEvent.clear(input)
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(router.state.location.search).not.toHaveProperty('group')
    )
  }
)

it('resets the selected group and restores a group from URL navigation', async () => {
  const router = await renderFilter('/usage-logs/common?group=premium')
  const input = screen.getByRole('combobox', { name: 'Group' })
  expect(input).toHaveValue('premium')
  await userEvent.click(screen.getByRole('button', { name: 'Reset' }))
  await waitFor(() => expect(input).toHaveValue(''))
  expect(router.state.location.search).not.toHaveProperty('group')
  await router.history.push('/usage-logs/common?group=retired')
  await waitFor(() => expect(input).toHaveValue('retired'))
})

it('keeps a selected group visible on focus and can clear it without choosing another option', async () => {
  const router = await renderFilter('/usage-logs/common?group=premium')
  const input = screen.getByRole('combobox', { name: 'Group' })
  await userEvent.click(input)
  expect(input).toHaveValue('premium')
  expect(await screen.findByRole('option', { name: 'default' })).toBeVisible()
  await userEvent.clear(input)
  await userEvent.keyboard('{Enter}')
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('group')
  )
})

it('keeps the compact input and masks the dropdown together with other sensitive filters', async () => {
  await renderFilter()
  const input = screen.getByRole('combobox', { name: 'Group' })
  expect(input).toHaveClass('h-8', 'text-sm', 'leading-5')
  await userEvent.click(screen.getByRole('button', { name: /^Hide$/ }))
  await userEvent.click(input)
  const option = await screen.findByRole('option', { name: 'premium' })
  const maskedField = input.closest('.\\[-webkit-text-security\\:disc\\]')
  expect(maskedField).not.toBeNull()
  expect(option.closest('.\\[-webkit-text-security\\:disc\\]')).not.toBeNull()
  await userEvent.keyboard('{Escape}')
  expect(input).toHaveAttribute('aria-expanded', 'false')
  await userEvent.tab()
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
})

it('lets mobile users select a long group name inside the filter drawer and submit it', async () => {
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: vi.fn(),
  })
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query === '(max-width: 640px)',
  }))
  const longGroup = 'enterprise-team-with-a-long-group-name'
  const router = await renderFilter('/usage-logs/common', {
    [longGroup]: { desc: '', ratio: 1 },
  })
  const dialog = screen.getByRole('dialog')
  const input = within(dialog).getByRole('combobox', { name: 'Group' })
  await userEvent.click(input)
  const option = await within(dialog).findByRole('option', { name: longGroup })
  expect(option).toBeVisible()
  await userEvent.click(option)
  expect(input).toHaveValue(longGroup)
  expect(dialog).toBeVisible()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ group: longGroup })
  )
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
})

it.each([1, 10])(
  'excludes only auto from group choices for role %s',
  async (role) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'viewer', role })
    const router = await renderFilter('/usage-logs/common', {
      auto: { desc: '', ratio: 1 },
      'auto-team': { desc: '', ratio: 1 },
    })
    const input = screen.getByRole('combobox', { name: 'Group' })
    await userEvent.click(input)
    const option = await screen.findByRole('option', { name: 'auto-team' })
    expect(
      screen.queryByRole('option', { name: 'auto' })
    ).not.toBeInTheDocument()
    await userEvent.click(option)
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({ group: 'auto-team' })
    )
  }
)

it('keeps historical auto values editable when auto is the only available group', async () => {
  const router = await renderFilter('/usage-logs/common?group=auto', {
    auto: { desc: '', ratio: 1 },
  })
  const input = screen.getByRole('combobox', { name: 'Group' })
  expect(input).toHaveValue('auto')
  await userEvent.click(input)
  expect(screen.queryByRole('option', { name: 'auto' })).not.toBeInTheDocument()
  await userEvent.clear(input)
  await userEvent.type(input, 'retired')
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ group: 'retired' })
  )
})
