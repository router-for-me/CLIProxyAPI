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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { MarketplacePluginCard } from '../components/marketplace-plugin-card'
import { PluginWebsiteLink } from '../components/plugin-website-link'
import { PluginsTable } from '../components/plugins-table'
import type { TaskPluginListItem } from '../types'

vi.mock('@/lib/lobe-icon', () => ({ getLobeIcon: () => null }))

const clients: QueryClient[] = []
afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  localStorage.removeItem('task-plugins-view-mode')
})

const website = 'https://example.com/plugin'

test('marketplace card shows the plugin website separately from installation', () => {
  render(
    <MarketplacePluginCard
      indexUrl='https://example.com/index.json'
      plugin={{
        key: 'example',
        name: 'Example',
        website,
        latest: '1.0.0',
        versions: [{ version: '1.0.0', path: 'plugin.js' }],
      }}
      installState={{ status: 'not_installed' }}
      onInstall={() => undefined}
    />
  )
  expect(screen.getByRole('link', { name: 'Plugin website' })).toHaveAttribute(
    'href',
    website
  )
  expect(screen.getByRole('button', { name: 'Install' })).toBeEnabled()
})

test('installed table shows the same safe website as the plugin card', () => {
  localStorage.setItem('task-plugins-view-mode', 'table')
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  clients.push(client)
  const item: TaskPluginListItem = {
    meta: {
      key: 'example',
      name: 'Example',
      website,
      sortPriority: 10,
      version: '1.0.0',
      apiVersion: 1,
      author: { name: 'Example' },
      models: [],
      fetchMode: 'per_task',
    },
    source: 'factory',
    enabled: true,
    active: true,
    source_hash: '',
    remark: '',
    runtime_status: 'registered',
    channel_count: 0,
    in_flight_count: 0,
  }
  client.setQueryData(['task-plugins'], [item])
  render(
    <QueryClientProvider client={client}>
      <PluginsTable onDetails={() => undefined} onUpload={() => undefined} />
    </QueryClientProvider>
  )
  const links = screen.getAllByRole('link', { name: 'Plugin website' })
  for (const link of links) {
    expect(link).toHaveAttribute('href', website)
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  }
})

test('website link is reachable and activated using the keyboard', async () => {
  const user = userEvent.setup()
  render(<PluginWebsiteLink website={website} />)
  const link = screen.getByRole('link', { name: 'Plugin website' })
  const click = vi.fn((event: Event) => event.preventDefault())
  link.addEventListener('click', click)
  await user.tab()
  expect(link).toHaveFocus()
  await user.keyboard('{Enter}')
  expect(click).toHaveBeenCalledOnce()
  link.removeEventListener('click', click)
})
