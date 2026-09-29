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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, expect, test, vi } from 'vitest'

import { Markdown } from '@/components/ui/markdown'

import { PluginChangelogPanel } from '../components/plugin-changelog-panel'
import type { MarketplacePlugin } from '../types'

const plugin: MarketplacePlugin = {
  key: 'demo',
  name: 'Demo',
  latest: '1.2.0',
  versions: ['1.2.0', '1.1.0'].map((version) => ({
    version,
    path: `plugins/tasks/demo/${version}/plugin.js`,
  })),
}
const english = `---
changelogVersion: 1
plugin: demo
version: "1.2.0"
locale: en
translations:
  zh-CN: CHANGELOG.zh-CN.md
---
# Changelog

## [1.2.0]

### Fixed

- Preserve **zero** in \`seed: 0\` &amp; explicit false. See [source](plugin.js).

### Migration

- Configure resolution prices.
`
const chinese = english
  .replace(
    'locale: en\ntranslations:\n  zh-CN: CHANGELOG.zh-CN.md',
    'locale: zh-CN'
  )
  .replace('Configure resolution prices.', '配置分辨率价格。')
const clients: QueryClient[] = []

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const view = render(
    <QueryClientProvider client={client}>
      <PluginChangelogPanel
        indexUrl='https://example.com/index.json'
        plugin={plugin}
        version='1.2.0'
      />
    </QueryClientProvider>
  )
  return {
    setVersion: (version: string) =>
      view.rerender(
        <QueryClientProvider client={client}>
          <PluginChangelogPanel
            indexUrl='https://example.com/index.json'
            plugin={plugin}
            version={version}
          />
        </QueryClientProvider>
      ),
  }
}

afterEach(async () => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.unstubAllGlobals()
  await act(() => i18next.changeLanguage('en'))
})

test('renders inline formatting and resolves release links against the source instead of the dashboard', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(english))
  )
  renderPanel()
  expect(
    await screen.findByRole('heading', { name: 'Migration notes' })
  ).toBeVisible()
  expect(screen.getByText('zero').tagName).toBe('STRONG')
  expect(screen.getByText('seed: 0').tagName).toBe('CODE')
  expect(screen.getByText(/explicit false/).textContent).toContain(
    '& explicit false'
  )
  const source = screen.getByRole('link', { name: 'source' })
  expect(source).toHaveAttribute(
    'href',
    'https://example.com/plugins/tasks/demo/1.2.0/plugin.js'
  )
  expect(source).toHaveAttribute('rel', 'noopener noreferrer')
})

test('changing language chooses Chinese when declared and otherwise displays English with a fallback notice', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async (url: string) =>
        new Response(url.endsWith('CHANGELOG.zh-CN.md') ? chinese : english)
    )
  )
  renderPanel()
  expect(await screen.findByText('Configure resolution prices.')).toBeVisible()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(await screen.findByText('配置分辨率价格。')).toBeVisible()
  await act(() => i18next.changeLanguage('fr'))
  expect(
    await screen.findByText(
      'This changelog is not available in your language. Showing English.'
    )
  ).toBeVisible()
  expect(screen.getByText('Configure resolution prices.')).toBeVisible()
  expect(screen.queryByText('配置分辨率价格。')).not.toBeInTheDocument()
})

test('a release without a changelog shows the shared empty state', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(null, { status: 404 }))
  )
  renderPanel()
  expect(
    await screen.findByText('No changelog for this version.')
  ).toBeVisible()
  expect(
    screen.queryByRole('link', { name: 'View changelog source' })
  ).not.toBeInTheDocument()
})

test('a failed download can be retried from the shared error state', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce(new Response(english))
  )
  renderPanel()
  await waitFor(() =>
    expect(screen.getByText('Could not load the changelog')).toBeVisible()
  )
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('Configure resolution prices.')).toBeVisible()
})

test('switching versions hides the previous release while loading and ignores its late response', async () => {
  let finishLatest!: (response: Response) => void
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/1.1.0/')) {
        return Promise.resolve(
          new Response(
            english
              .replaceAll('1.2.0', '1.1.0')
              .replace(
                'Configure resolution prices.',
                'Historical pricing guidance.'
              )
          )
        )
      }
      return new Promise<Response>((resolve) => {
        finishLatest = resolve
      })
    })
  )
  const view = renderPanel()
  expect(screen.getByText('Loading...')).toBeVisible()
  view.setVersion('1.1.0')
  expect(await screen.findByText('Historical pricing guidance.')).toBeVisible()
  await act(async () => finishLatest(new Response(english)))
  expect(
    screen.queryByText('Configure resolution prices.')
  ).not.toBeInTheDocument()
  expect(screen.getByText('v1.1.0')).toBeVisible()
  await waitFor(() =>
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  )
})

test('relative-link support keeps the shared Markdown sanitizer and default link behavior', () => {
  const view = render(
    <Markdown baseUrl='https://example.com/releases/CHANGELOG.md'>
      {
        '[safe](../guide) [unsafe](javascript:alert%281%29) [entity](javascript&#58;alert%281%29)'
      }
    </Markdown>
  )
  expect(screen.getByRole('link', { name: 'safe' })).toHaveAttribute(
    'href',
    'https://example.com/guide'
  )
  expect(screen.queryByRole('link', { name: 'unsafe' })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: 'entity' })).not.toBeInTheDocument()
  view.rerender(<Markdown>[relative](../guide)</Markdown>)
  expect(screen.getByRole('link', { name: 'relative' })).toHaveAttribute(
    'href',
    '../guide'
  )
})
