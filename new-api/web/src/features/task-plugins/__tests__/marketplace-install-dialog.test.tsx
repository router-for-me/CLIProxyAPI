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
import { createHash, webcrypto } from 'node:crypto'

import {
  QueryClient,
  QueryClientProvider,
  QueryObserver,
} from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import {
  MarketplaceInstallDialog,
  type MarketplaceInstallTarget,
} from '../components/marketplace-install-dialog'

const api = vi.hoisted(() => ({
  getTaskPlugin: vi.fn(),
  installMarketplacePlugin: vi.fn(),
  activateTaskPlugin: vi.fn(),
}))
vi.mock('../api', () => api)
const clients: QueryClient[] = []
const source =
  '// comment\nconst plugin = "demo";\nfunction run() { return 42 }'
function renderDialog(
  upgrade = false,
  sha256?: string,
  multiple = false,
  installedVersion = '1.0'
) {
  const target: MarketplaceInstallTarget = {
    source: { name: 'Official', index_url: 'https://example.com/index.json' },
    plugin: {
      key: 'demo',
      name: 'Demo',
      latest: '1.1',
      versions: [
        {
          version: '1.1',
          path: 'plugin.js',
          sha256,
          baseUrl: `https://example.com/${'long-path/'.repeat(15)}`,
        },
      ],
    },
    version: '1.1',
    installState: upgrade
      ? { status: 'upgradable', installedVersion, latestVersion: '1.1' }
      : { status: 'not_installed' },
  }
  if (multiple) {
    target.plugin.versions.push({
      version: '1.0',
      path: 'old.js',
      baseUrl: 'https://old.example.com',
      sha256: createHash('sha256').update('const old = 0').digest('hex'),
    })
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const onOpenChange = vi.fn()
  const view = render(
    <QueryClientProvider client={client}>
      <MarketplaceInstallDialog target={target} onOpenChange={onOpenChange} />
    </QueryClientProvider>
  )
  return {
    client,
    onOpenChange,
    reopen: () => {
      view.rerender(
        <QueryClientProvider client={client}>
          <MarketplaceInstallDialog target={null} onOpenChange={onOpenChange} />
        </QueryClientProvider>
      )
      view.rerender(
        <QueryClientProvider client={client}>
          <MarketplaceInstallDialog
            target={target}
            onOpenChange={onOpenChange}
          />
        </QueryClientProvider>
      )
    },
  }
}
afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.unstubAllGlobals()
  vi.resetAllMocks()
})
test('loading keeps installation disabled and footer outside the bounded scroll body', () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => new Promise(() => {}))
  )
  renderDialog()
  const dialog = screen.getByRole('dialog')
  expect(dialog).toHaveClass(
    'sm:max-w-3xl',
    'max-h-[var(--dialog-available-height,calc(100dvh-2rem))]',
    'overflow-y-auto'
  )
  const footer = dialog.querySelector('[data-slot=dialog-footer]')
  const body = [...dialog.children].find((element) =>
    element.classList.contains('overflow-y-auto')
  )
  expect(body).toBeDefined()
  if (!body || !footer) throw new Error('Missing dialog scroll body or footer')
  expect(body.contains(footer)).toBe(false)
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeDisabled()
  expect(screen.getByText('Fetching plugin source...')).toBeInTheDocument()
})
test('download failure shows the error and prevents installation', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
  renderDialog()
  expect(
    await screen.findByText(/Could not fetch the plugin source/)
  ).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeDisabled()
})
test('hash mismatch blocks installation and the complete hash can be copied', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(source))
  )
  const hash = 'a'.repeat(64)
  renderDialog(false, hash)
  expect(await screen.findByText('Integrity check failed')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeDisabled()
  expect(screen.queryByText(hash)).not.toBeInTheDocument()
  const details = screen.getByRole('button', { name: 'Integrity hash' })
  expect(details).toHaveAttribute('aria-expanded', 'false')
  details.focus()
  await user.keyboard('{Enter}')
  expect(details).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByText(hash)).toHaveClass('break-all')
  await user.click(screen.getByRole('button', { name: 'Copy to clipboard' }))
  expect(await navigator.clipboard.readText()).toBe(hash)
})
test('first install shows source on demand and disables repeat submission until success', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(source))
  )
  let resolveInstall!: (value: unknown) => void
  api.installMarketplacePlugin.mockImplementation(
    () =>
      new Promise((resolve) => {
        resolveInstall = resolve
      })
  )
  const { onOpenChange } = renderDialog()
  await user.click(screen.getByRole('tab', { name: 'Full source' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Install and enable' })
    ).toBeEnabled()
  )
  expect(screen.getByText('const')).toHaveClass('tok-keyword')
  expect(screen.getByText('"demo"')).toHaveClass('tok-string')
  expect(screen.getByText('// comment')).toHaveClass('tok-comment')
  expect(screen.getByText('42')).toHaveClass('tok-number')
  expect(screen.getByText('run')).toHaveClass('tok-definition')
  await user.click(screen.getByRole('button', { name: 'Install and enable' }))
  expect(
    await screen.findByRole('button', { name: 'Installing...' })
  ).toBeDisabled()
  expect(api.installMarketplacePlugin).toHaveBeenCalledWith({
    source,
    sourceSha256: undefined,
    remark: 'Official v1.1',
    icon: undefined,
  })
  resolveInstall({ meta: { name: 'Demo', version: '1.1' } })
  await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
})
test('installed plugins allow opening differences and switching to full source with the keyboard', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(source))
  )
  api.getTaskPlugin.mockResolvedValue({
    meta: { version: '1.0' },
    source: 'const old = 0',
  })
  renderDialog(true)
  const diffTab = await screen.findByRole('tab', {
    name: 'Version differences',
  })
  await user.click(diffTab)
  expect(diffTab).toHaveAttribute('aria-selected', 'true')
  expect(await screen.findByText('- const old = 0')).toBeInTheDocument()
  diffTab.focus()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Full source' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeEnabled()
})

const metaSource = `export const meta = {
  models: ['incho_music', 'voice', 'instrumental', 'remix', 'ambient', 'lyrics', 'extended'],
  baseUrl: 'https://open.yinchaoyongxian.com',
  protocols: [{name: 'openai_responses', supports: ['stream', 'sync', 'background'], models: ['incho_music']}],
  routes: [
    {method: 'POST', path: '/incho/submit/:action', type: 'submit'},
    {method: 'GET', path: '/incho/fetch/:task_id', type: 'query'},
  ],
};`

test('shows all four Incho interfaces and explains absent authentication and extra domains', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(metaSource))
  )
  renderDialog()
  const endpoints = screen.getByRole('region', { name: 'Supported interfaces' })
  expect(
    await within(endpoints).findByText('/incho/submit/:action')
  ).toBeVisible()
  expect(within(endpoints).getByText('/incho/fetch/:task_id')).toBeVisible()
  const create = within(endpoints).getByText('/v1/responses').closest('li')
  const query = within(endpoints)
    .getByText('/v1/responses/{response_id}')
    .closest('li')
  if (!create || !query) throw new Error('Missing protocol endpoints')
  expect(within(create).getByText('POST')).toBeVisible()
  expect(within(create).getByText('stream')).toBeVisible()
  expect(within(create).getByText('sync')).toBeVisible()
  expect(within(create).getByText('background')).toBeVisible()
  expect(within(query).queryByText('stream')).not.toBeInTheDocument()
  const connections = screen.getByRole('region', {
    name: 'Connection settings',
  })
  expect(
    within(connections).getByText('No extra domains declared')
  ).toBeVisible()
  expect(
    within(connections).getByText(
      'Not declared; an API key may still be required.'
    )
  ).toBeVisible()
  expect(
    within(connections).queryByText('From marketplace index')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText('Some plugin information could not be read')
  ).not.toBeInTheDocument()
})

test('keyboard users can expand the full model list and inspect a protocol model scope', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(metaSource))
  )
  renderDialog()
  const more = await screen.findByRole('button', { name: 'More models (1)' })
  const models = screen.getByRole('region', { name: 'Supported models' })
  expect(within(models).queryByText('extended')).not.toBeInTheDocument()
  more.focus()
  await user.keyboard('{Enter}')
  expect(more).toHaveAttribute('aria-expanded', 'true')
  expect(within(models).getByText('extended')).toBeVisible()
  const scope = screen.getByRole('button', { name: 'Model scope' })
  scope.focus()
  await user.keyboard('{Enter}')
  expect(scope).toHaveAttribute('aria-expanded', 'true')
  const scopePanel = screen.getByRole('dialog', { name: 'Model scope' })
  expect(within(scopePanel).getByText('incho_music')).toBeVisible()
})

test('model constants show source models without an unreadable metadata warning', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(`
      const MODELS = ['jev-1.13.0', 'jev-latest', 'jev-preview'];
      export const meta = {models: MODELS};
      function validate(model) { return MODELS.includes(model); }
    `)
    )
  )
  renderDialog()
  const models = screen.getByRole('region', { name: 'Supported models' })
  expect(await within(models).findByText('jev-1.13.0')).toBeVisible()
  expect(within(models).getByText('jev-latest')).toBeVisible()
  expect(within(models).getByText('jev-preview')).toBeVisible()
  expect(
    within(models).queryByText('From marketplace index')
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText('Some plugin information could not be read')
  ).not.toBeInTheDocument()
})

test('unreadable metadata can be retried without preventing installation or inventing domain restrictions', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValueOnce(new Response('export const meta = makeMeta();'))
      .mockResolvedValueOnce(new Response(metaSource))
  )
  renderDialog()
  expect(
    await screen.findByText('Some plugin information could not be read')
  ).toBeVisible()
  expect(
    screen.queryByText('No extra domains declared')
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeEnabled()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() =>
    expect(
      screen.queryByText('Some plugin information could not be read')
    ).not.toBeInTheDocument()
  )
  expect(
    within(
      screen.getByRole('region', { name: 'Supported interfaces' })
    ).getByText('/incho/submit/:action')
  ).toBeVisible()
})

test('a matching hash shows verification separately from code safety and keeps hash details collapsed', async () => {
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(metaSource))
  )
  const hash = createHash('sha256').update(metaSource).digest('hex')
  renderDialog(false, hash)
  expect(await screen.findByText('File integrity verified')).toBeVisible()
  expect(
    screen.getByText(
      'A matching hash confirms the published file, not the safety of its code.'
    )
  ).toBeVisible()
  expect(screen.queryByText(hash)).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeEnabled()
})

test('a browser without WebCrypto reports that verification is unavailable instead of claiming success', async () => {
  vi.stubGlobal('crypto', {})
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(metaSource))
  )
  renderDialog(false, 'a'.repeat(64))
  expect(
    await screen.findByText(
      'Integrity verification unavailable in this environment'
    )
  ).toBeVisible()
  expect(screen.queryByText('File integrity verified')).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Install and enable' })
  ).toBeEnabled()
})

test('selecting a historical version installs its source and hash', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async (url: string) =>
        new Response(url.endsWith('old.js') ? 'const old = 0' : source)
    )
  )
  api.installMarketplacePlugin.mockResolvedValue({
    meta: { key: 'demo', name: 'Demo', version: '1.0' },
    plugin: { active: true },
  })
  renderDialog(false, undefined, true)
  const selector = screen.getByRole('combobox', { name: 'Select version' })
  expect(selector).toHaveValue('1.1 · Latest version')
  await user.click(selector)
  await user.click(screen.getByRole('option', { name: '1.0' }))
  const install = screen.getByRole('button', { name: 'Install and enable' })
  await waitFor(() => expect(install).toBeEnabled())
  expect(
    within(
      screen.getByRole('region', { name: 'Connection settings' })
    ).getByText('https://old.example.com')
  ).toBeVisible()
  await user.click(install)
  expect(api.activateTaskPlugin).not.toHaveBeenCalled()
  expect(api.installMarketplacePlugin).toHaveBeenCalledWith({
    source: 'const old = 0',
    sourceSha256: createHash('sha256').update('const old = 0').digest('hex'),
    remark: 'Official v1.0',
    icon: undefined,
  })
})

test('a single published version remains visible and cannot be changed', () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => new Promise(() => {}))
  )
  renderDialog()
  expect(
    screen.getByRole('combobox', { name: 'Select version' })
  ).toBeDisabled()
  expect(screen.getByRole('combobox', { name: 'Select version' })).toHaveValue(
    '1.1 · Latest version'
  )
})

test.each(['0.9', '1.1', '1.0'])(
  'installing 1.0 from active %s shows the target diff and activates the saved version',
  async (installedVersion) => {
    const user = userEvent.setup()
    vi.stubGlobal('crypto', webcrypto)
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async (url: string) =>
          new Response(url.endsWith('old.js') ? 'const old = 0' : source)
      )
    )
    api.getTaskPlugin.mockResolvedValue({
      meta: { version: installedVersion },
      source: 'const current = 1',
    })
    api.installMarketplacePlugin.mockResolvedValue({
      meta: { key: 'demo', name: 'Demo', version: '1.0' },
      plugin: { active: false },
    })
    let finishActivation!: () => void
    api.activateTaskPlugin.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishActivation = resolve
        })
    )
    const { onOpenChange } = renderDialog(
      true,
      undefined,
      true,
      installedVersion
    )
    const selector = screen.getByRole('combobox', { name: 'Select version' })
    await user.click(selector)
    await user.click(
      screen.getByRole('option', {
        name: installedVersion === '1.0' ? '1.0 · Active version' : '1.0',
      })
    )
    if (installedVersion === '1.0') {
      await user.click(screen.getByRole('tab', { name: 'Version differences' }))
    }
    expect(await screen.findByText('+ const old = 0')).toBeVisible()
    expect(
      screen.getByText(`Current v${installedVersion} → install v1.0`)
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Install and enable' }))
    await waitFor(() =>
      expect(api.activateTaskPlugin).toHaveBeenCalledWith('demo', '1.0')
    )
    expect(selector).toBeDisabled()
    expect(onOpenChange).not.toHaveBeenCalled()
    await act(async () => finishActivation())
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  }
)

test('a late response for the previous version cannot replace the selected source', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('crypto', webcrypto)
  let finishLatest!: (value: Response) => void
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) =>
      url.endsWith('old.js')
        ? Promise.resolve(new Response('const old = 0'))
        : new Promise<Response>((resolve) => {
            finishLatest = resolve
          })
    )
  )
  api.installMarketplacePlugin.mockResolvedValue({
    meta: { name: 'Demo', version: '1.0' },
    plugin: { active: true },
  })
  const { reopen } = renderDialog(false, undefined, true)
  const selector = screen.getByRole('combobox', { name: 'Select version' })
  await user.click(selector)
  await user.keyboard('1.0{ArrowDown}{Enter}')
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Install and enable' })
    ).toBeEnabled()
  )
  await act(async () => finishLatest(new Response(source)))
  expect(selector).toHaveValue('1.0')
  await user.click(screen.getByRole('button', { name: 'Install and enable' }))
  expect(api.installMarketplacePlugin).toHaveBeenCalledWith(
    expect.objectContaining({
      source: 'const old = 0',
      remark: 'Official v1.0',
    })
  )
  reopen()
  expect(screen.getByRole('combobox', { name: 'Select version' })).toHaveValue(
    '1.1 · Latest version'
  )
})

test.each(['download', 'hash'])(
  'switching versions blocks installation on %s failure',
  async (failure) => {
    const user = userEvent.setup()
    vi.stubGlobal('crypto', webcrypto)
    let finishOld!: (value: Response) => void
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        url.endsWith('old.js')
          ? new Promise<Response>((resolve) => {
              finishOld = resolve
            })
          : Promise.resolve(new Response(source))
      )
    )
    renderDialog(false, undefined, true)
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Install and enable' })
      ).toBeEnabled()
    )
    await user.click(screen.getByRole('combobox', { name: 'Select version' }))
    await user.click(screen.getByRole('option', { name: '1.0' }))
    expect(
      screen.getByRole('button', { name: 'Install and enable' })
    ).toBeDisabled()
    await act(async () =>
      finishOld(
        new Response('wrong source', {
          status: failure === 'download' ? 500 : 200,
        })
      )
    )
    if (failure === 'hash') {
      expect(await screen.findByText('Integrity check failed')).toBeVisible()
    } else {
      expect(
        await screen.findByText(/Could not fetch the plugin source/)
      ).toBeVisible()
    }
    expect(
      screen.getByRole('button', { name: 'Install and enable' })
    ).toBeDisabled()
  }
)

test('activation failure stays open and changing version clears the installation error', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(source))
  )
  api.installMarketplacePlugin.mockResolvedValue({
    meta: { key: 'demo', name: 'Demo', version: '1.1' },
    plugin: { active: false },
  })
  api.activateTaskPlugin.mockRejectedValue(new Error('activation failed'))
  const { onOpenChange } = renderDialog(false, undefined, true)
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Install and enable' })
    ).toBeEnabled()
  )
  await user.click(screen.getByRole('button', { name: 'Install and enable' }))
  expect(await screen.findByText('activation failed')).toBeVisible()
  expect(onOpenChange).not.toHaveBeenCalled()
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.0' }))
  expect(screen.queryByText('activation failed')).not.toBeInTheDocument()
})

test('details come first and only choosing a different installed version opens differences', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(source))
  )
  api.getTaskPlugin.mockResolvedValue({ meta: { version: '1.0' }, source })
  renderDialog(true, undefined, true)
  const details = screen.getByRole('tab', { name: 'Plugin details' })
  const diff = screen.getByRole('tab', { name: 'Version differences' })
  expect(screen.getAllByRole('tab')[0]).toBe(details)
  expect(details).toHaveAttribute('aria-selected', 'true')
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.0 · Active version' }))
  expect(details).toHaveAttribute('aria-selected', 'true')
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.1 · Latest version' }))
  expect(diff).toHaveAttribute('aria-selected', 'true')
  expect(await screen.findByText('No source changes')).toBeVisible()
  expect(
    screen.queryByRole('region', { name: 'Supported interfaces' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.0 · Active version' }))
  expect(details).toHaveAttribute('aria-selected', 'true')
})

test('first installation opens plugin details and offers full source without a differences tab', () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => new Promise(() => {}))
  )
  renderDialog()
  expect(screen.getByRole('tab', { name: 'Plugin details' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('tab', { name: 'Full source' })).toBeVisible()
  expect(
    screen.queryByRole('tab', { name: 'Version differences' })
  ).not.toBeInTheDocument()
})

test('a factory plugin uses its built-in source as the diff baseline and installs the selected override', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async (url: string) =>
        new Response(url.endsWith('old.js') ? 'const old = 0' : source)
    )
  )
  api.getTaskPlugin.mockResolvedValue({
    layer: 'factory',
    meta: { version: '1.1' },
    source: 'const factory = 1',
  })
  api.installMarketplacePlugin.mockResolvedValue({
    layer: 'override',
    meta: { key: 'demo', name: 'Demo', version: '1.0' },
    plugin: { active: true },
  })
  const { client, onOpenChange } = renderDialog(true, undefined, true, '1.1')
  const installed = [
    { meta: { key: 'demo', version: '1.1' }, source: 'factory' },
  ]
  const updated = [
    { meta: { key: 'demo', version: '1.0' }, source: 'override_over_factory' },
  ]
  client.setQueryData(['task-plugins'], installed)
  const observer = new QueryObserver(client, {
    queryKey: ['task-plugins'],
    queryFn: async () => updated,
    staleTime: Infinity,
  })
  const unsubscribe = observer.subscribe(() => {})
  expect(screen.getByRole('tab', { name: 'Plugin details' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.0' }))
  expect(await screen.findByText('- const factory = 1')).toBeVisible()
  expect(screen.getByText('+ const old = 0')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Install and enable' }))
  expect(api.installMarketplacePlugin).toHaveBeenCalledWith({
    source: 'const old = 0',
    sourceSha256: createHash('sha256').update('const old = 0').digest('hex'),
    remark: 'Official v1.0',
    icon: undefined,
  })
  await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  await waitFor(() => expect(observer.getCurrentResult().data).toEqual(updated))
  unsubscribe()
})

test('changelog is fetched only after opening its tab and a missing log does not block installation', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: string) => {
    if (url.endsWith('CHANGELOG.md')) return new Response(null, { status: 404 })
    return new Response(source)
  })
  vi.stubGlobal('fetch', fetchMock)
  renderDialog()
  const install = screen.getByRole('button', { name: 'Install and enable' })
  await waitFor(() => expect(install).toBeEnabled())
  expect(
    fetchMock.mock.calls.some(([url]) => url.endsWith('CHANGELOG.md'))
  ).toBe(false)
  const changelog = screen.getByRole('tab', { name: 'Changelog' })
  changelog.focus()
  await user.keyboard('{Enter}')
  expect(changelog).toHaveAttribute('aria-selected', 'true')
  expect(
    await screen.findByText('No changelog for this version.')
  ).toBeVisible()
  expect(install).toBeEnabled()
  expect(api.installMarketplacePlugin).not.toHaveBeenCalled()
})

test('changing the selected version while reading changelog keeps that tab open', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url.endsWith('CHANGELOG.md')) {
        return new Response(null, { status: 404 })
      }
      return new Response(source)
    })
  )
  api.getTaskPlugin.mockResolvedValue({ meta: { version: '1.0' }, source })
  renderDialog(true, undefined, true)
  const changelog = screen.getByRole('tab', { name: 'Changelog' })
  await user.click(changelog)
  expect(
    await screen.findByText('No changelog for this version.')
  ).toBeVisible()
  await user.click(screen.getByRole('combobox', { name: 'Select version' }))
  await user.click(screen.getByRole('option', { name: '1.0 · Active version' }))
  expect(changelog).toHaveAttribute('aria-selected', 'true')
  await waitFor(() =>
    expect(screen.getByText('Could not load the changelog')).toBeVisible()
  )
})
