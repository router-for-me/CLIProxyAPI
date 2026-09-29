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
import { render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { MarketplacePluginCard } from '../components/marketplace-plugin-card'
import type { MarketplacePlugin } from '../types'

vi.mock('@/lib/lobe-icon', () => ({
  getLobeIcon: () => null,
}))

const INDEX_URL =
  'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/index.json'

function plugin(overrides?: Partial<MarketplacePlugin>): MarketplacePlugin {
  return {
    key: 'incho',
    name: 'Incho',
    icon: 'text',
    latest: '1.0.1',
    versions: [
      { version: '1.0.1', path: 'plugins/tasks/incho/1.0.1/plugin.js' },
    ],
    iconFile: { path: 'plugins/tasks/incho/icon.svg' },
    ...overrides,
  }
}

function renderCard(target: MarketplacePlugin) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <MarketplacePluginCard
        plugin={target}
        indexUrl={INDEX_URL}
        installState={{ status: 'not_installed' }}
        onInstall={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('marketplace card logo', () => {
  test('fetches the sidecar icon from the index origin and renders it as a data URI', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response('<svg/>', {
          status: 200,
          headers: { 'content-type': 'text/plain; charset=utf-8' },
        })
    )
    vi.stubGlobal('fetch', fetchMock)

    const { container } = renderCard(plugin())

    await waitFor(() => {
      const image = container.querySelector('img')
      expect(image?.getAttribute('src')).toBe(
        'data:image/svg+xml;base64,PHN2Zy8+'
      )
    })
    expect(fetchMock).toHaveBeenCalledWith(
      'https://raw.githubusercontent.com/QuantumNous/new-api-plugins/main/plugins/tasks/incho/icon.svg'
    )
  })

  test('shows the text avatar when the icon file cannot be fetched', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('', { status: 404 }))
    )

    const { container } = renderCard(plugin())

    await waitFor(() => expect(container.textContent).toContain('IN'))
    expect(container.querySelector('img')).toBeNull()
  })

  test('does not fetch anything for a plugin without a sidecar icon', () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const { container } = renderCard(plugin({ iconFile: undefined }))

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.querySelector('img')).toBeNull()
  })
})

test.each([{ channelTypes: undefined }, { channelTypes: [] }])(
  'shows the generic task channel type when channelTypes is $channelTypes',
  ({ channelTypes }) => {
    const { getByText } = renderCard(
      plugin({ iconFile: undefined, channelTypes })
    )
    expect(getByText('Channel type').nextElementSibling).toHaveTextContent(
      'Task Plugin'
    )
  }
)
