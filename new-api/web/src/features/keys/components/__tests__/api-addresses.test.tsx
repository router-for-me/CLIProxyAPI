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
  act,
  cleanup,
  render,
  screen,
  within,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { STATUS_QUERY_KEY, type StatusData } from '@/lib/status-query'

import { ApiKeysPrimaryButtons } from '../api-keys-primary-buttons'
import { ApiKeysProvider } from '../api-keys-provider'

let client: QueryClient

beforeEach(() => {
  localStorage.clear()
  client = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  localStorage.clear()
})

function renderAddresses(status: StatusData) {
  client.setQueryData(STATUS_QUERY_KEY, status)
  return render(
    <QueryClientProvider client={client}>
      <ApiKeysProvider>
        <ApiKeysPrimaryButtons />
      </ApiKeysProvider>
    </QueryClientProvider>
  )
}

it('shows every configured address and copies only the chosen visible URL', async () => {
  const user = userEvent.setup()
  const writeText = vi.spyOn(navigator.clipboard, 'writeText')
  renderAddresses({
    server_address: 'https://console.example.com',
    api_info_enabled: true,
    api_info: [
      {
        route: 'Global',
        description: 'Worldwide access',
        url: 'https://api.example.com/v1',
        color: 'blue',
      },
      {
        route: 'Asia',
        description: 'Regional access',
        url: 'https://asia.example.com/gateway/v1/',
        color: 'green',
      },
    ],
  })

  await user.click(screen.getByRole('button', { name: 'API Addresses' }))
  const dialog = await screen.findByRole('dialog', { name: 'API Addresses' })
  const rows = within(dialog).getAllByRole('listitem')
  expect(rows).toHaveLength(2)
  expect(rows[0]).toHaveTextContent('Global')
  expect(rows[0]).toHaveTextContent('Worldwide access')
  expect(within(rows[0]).getByText('https://api.example.com/v1')).toBeVisible()
  expect(rows[1]).toHaveTextContent('Asia')
  expect(rows[1]).toHaveTextContent('Regional access')
  expect(
    within(rows[1]).getByText('https://asia.example.com/gateway/v1/')
  ).toBeVisible()
  expect(writeText).not.toHaveBeenCalled()

  await user.click(
    within(rows[1]).getByRole('button', {
      name: 'Copy API URL: https://asia.example.com/gateway/v1/',
    })
  )
  expect(writeText).toHaveBeenCalledWith('https://asia.example.com/gateway/v1/')
  expect(
    await within(rows[1]).findByRole('button', { name: 'Copied' })
  ).toBeVisible()
  expect(
    within(rows[0]).getByRole('button', {
      name: 'Copy API URL: https://api.example.com/v1',
    })
  ).toBeVisible()
})

it.each([
  {
    status: {
      api_info: [],
      server_address: 'https://gateway.example.com/proxy/',
    },
    label: 'Default API address',
    url: 'https://gateway.example.com/proxy/',
  },
  {
    status: { api_info: [] },
    label: 'Current domain',
    url: window.location.origin,
  },
  {
    status: {
      api_info_enabled: false,
      api_info: [
        {
          route: 'Hidden',
          description: 'Disabled address',
          url: 'https://hidden.example.com',
          color: 'blue',
        },
      ],
      server_address: 'https://gateway.example.com',
    },
    label: 'Default API address',
    url: 'https://gateway.example.com',
  },
])(
  'shows and copies the fallback $label when no configured addresses are available',
  async ({ status, label, url }) => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    renderAddresses(status)

    await user.tab()
    const trigger = screen.getByRole('button', { name: 'API Addresses' })
    expect(trigger).toHaveFocus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog', { name: 'API Addresses' })
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(within(dialog).getAllByRole('listitem')).toHaveLength(1)
    expect(within(dialog).getByText(label)).toBeVisible()
    expect(within(dialog).getByText(url)).toBeVisible()
    const copy = within(dialog).getByRole('button', {
      name: `Copy API URL: ${url}`,
    })
    await waitFor(() => expect(copy).toHaveFocus())
    await user.keyboard('{Enter}')
    expect(writeText).toHaveBeenCalledWith(url)
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
    )
    expect(trigger).toHaveFocus()
  }
)

it('keeps long addresses readable within the scrollable panel and updates when configuration changes', async () => {
  const user = userEvent.setup()
  const url =
    'https://regional-api-gateway.example.com/organization/production/openai-compatible/v1/'
  renderAddresses({
    api_info: [
      {
        route: 'Regional gateway',
        description: 'Regional production gateway',
        url,
        color: 'blue',
      },
    ],
  })
  await user.click(screen.getByRole('button', { name: 'API Addresses' }))
  const dialog = await screen.findByRole('dialog', { name: 'API Addresses' })
  expect(dialog).toHaveClass('max-w-[calc(100vw-2rem)]', 'overflow-y-auto')
  expect(within(dialog).getByText(url)).toHaveClass('break-all')
  expect(within(dialog).getByText(url)).not.toHaveClass('truncate')

  act(() => {
    client.setQueryData(STATUS_QUERY_KEY, {
      api_info: [
        {
          route: 'Replacement',
          description: 'Updated gateway',
          url: 'https://new.example.com',
          color: 'blue',
        },
      ],
    })
  })
  expect(
    await within(dialog).findByText('https://new.example.com')
  ).toBeVisible()
  expect(within(dialog).queryByText(url)).not.toBeInTheDocument()
  await user.click(
    within(dialog).getByRole('button', {
      name: 'Copy API URL: https://new.example.com',
    })
  )
  expect(await navigator.clipboard.readText()).toBe('https://new.example.com')
})
