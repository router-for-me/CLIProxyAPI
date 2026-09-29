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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { MarketplacePanel } from '../components/marketplace-panel'
import {
  DEFAULT_MARKETPLACE_INDEX_URL,
  GITHUB_MARKETPLACE_INDEX_URL,
} from '../lib/marketplace'
import type { MarketplaceIndex, MarketplaceSource } from '../types'

vi.mock('../components/marketplace-install-dialog', () => ({
  MarketplaceInstallDialog: () => null,
}))
vi.mock('../components/marketplace-plugin-card', () => ({
  MarketplacePluginCard: () => null,
}))
vi.mock('../components/marketplace-sources-dialog', () => ({
  MarketplaceSourcesDialog: () => null,
}))

const officialSource: MarketplaceSource = {
  name: 'Official',
  index_url: DEFAULT_MARKETPLACE_INDEX_URL,
}
const githubSource: MarketplaceSource = {
  name: 'GitHub',
  index_url: GITHUB_MARKETPLACE_INDEX_URL,
}

const officialIndex: MarketplaceIndex = {
  indexVersion: 1,
  name: 'Official catalog',
  plugins: [],
}
const githubIndex: MarketplaceIndex = {
  indexVersion: 1,
  name: 'GitHub catalog',
  plugins: [],
}

const queryClients: QueryClient[] = []

function renderPanel(): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
    },
  })
  queryClient.setQueryData(
    ['task-plugin-marketplace-sources'],
    [officialSource, githubSource]
  )
  queryClient.setQueryData(['task-plugins'], [])
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <MarketplacePanel />
    </QueryClientProvider>
  )
  return queryClient
}

function installIndexFetchMock() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const index =
      url === GITHUB_MARKETPLACE_INDEX_URL ? githubIndex : officialIndex
    return new Response(JSON.stringify(index), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  for (const queryClient of queryClients) queryClient.clear()
  queryClients.length = 0
  vi.unstubAllGlobals()
})

describe('MarketplacePanel source switch', () => {
  test('selects only the official source by default and hides both URLs', async () => {
    const fetchMock = installIndexFetchMock()
    renderPanel()

    expect(
      await screen.findByRole('heading', {
        name: officialIndex.name,
      })
    ).toBeInTheDocument()
    expect(screen.queryByText(DEFAULT_MARKETPLACE_INDEX_URL)).toBeNull()
    expect(screen.queryByText(GITHUB_MARKETPLACE_INDEX_URL)).toBeNull()
    expect(screen.getByRole('button', { name: 'Official' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
    expect(fetchMock).toHaveBeenCalledWith(DEFAULT_MARKETPLACE_INDEX_URL)
    expect(fetchMock).not.toHaveBeenCalledWith(GITHUB_MARKETPLACE_INDEX_URL)
  })

  test('loads GitHub only after the administrator switches to it', async () => {
    const fetchMock = installIndexFetchMock()
    const user = userEvent.setup()
    renderPanel()
    await screen.findByRole('heading', { name: officialIndex.name })

    expect(fetchMock).not.toHaveBeenCalledWith(GITHUB_MARKETPLACE_INDEX_URL)

    await user.click(screen.getByRole('button', { name: 'GitHub' }))

    expect(
      await screen.findByRole('heading', { name: githubIndex.name })
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: officialIndex.name })
    ).toBeNull()
    expect(screen.getByRole('button', { name: 'GitHub' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
    expect(fetchMock).toHaveBeenCalledWith(GITHUB_MARKETPLACE_INDEX_URL)
  })

  test('does not request GitHub automatically when the official source fails', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === DEFAULT_MARKETPLACE_INDEX_URL) {
        return new Response('', { status: 503 })
      }
      return new Response(JSON.stringify(githubIndex), { status: 200 })
    })
    vi.stubGlobal('fetch', fetchMock)
    renderPanel()

    expect(
      await screen.findByText('Could not load this source')
    ).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith(DEFAULT_MARKETPLACE_INDEX_URL)
    expect(fetchMock).not.toHaveBeenCalledWith(GITHUB_MARKETPLACE_INDEX_URL)
  })
})
