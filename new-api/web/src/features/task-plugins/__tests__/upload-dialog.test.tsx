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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { UploadDialog } from '../components/upload-dialog'
import { MAX_PLUGIN_SOURCE_BYTES } from '../lib/plugin-url'
import type { TaskPluginDetail } from '../types'

const { uploadTaskPlugin, activateTaskPlugin } = vi.hoisted(() => ({
  uploadTaskPlugin: vi.fn(),
  activateTaskPlugin: vi.fn(),
}))

vi.mock('../api', () => ({ uploadTaskPlugin, activateTaskPlugin }))

const queryClients: QueryClient[] = []

function renderDialog(open = true, initialKey?: string) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
      mutations: { retry: false },
    },
  })
  queryClients.push(queryClient)
  const onOpenChange = vi.fn()
  const view = render(
    <QueryClientProvider client={queryClient}>
      <UploadDialog
        open={open}
        onOpenChange={onOpenChange}
        initialKey={initialKey}
      />
    </QueryClientProvider>
  )
  return { onOpenChange, queryClient, view }
}

function sourceEditor() {
  return screen.getByRole('textbox', { name: 'Plugin source' })
}

function fileInput() {
  return screen.getByLabelText('JavaScript file') as HTMLInputElement
}

/** The dialog chrome also renders an sr-only "Close", so scope to the footer. */
function footerButton(name: string | RegExp) {
  const footer = document.querySelector('[data-slot=dialog-footer]')
  return within(footer as HTMLElement).getByRole('button', { name })
}

function savedPlugin(active: boolean): TaskPluginDetail {
  return {
    meta: {
      key: 'demo',
      name: 'Demo',
      version: '2.0.0',
      apiVersion: 1,
      author: { name: 'Demo' },
      models: [],
      fetchMode: 'per_task',
    },
    source: 'const a = 1',
    layer: 'override',
    plugin: {
      id: 2,
      key: 'demo',
      api_version: 1,
      version: '2.0.0',
      source: 'const a = 1',
      source_hash: 'hash',
      enabled: true,
      active,
      created_at: 1,
      remark: '',
    },
  }
}

async function uploadSource(user: ReturnType<typeof userEvent.setup>) {
  await user.upload(
    fileInput(),
    new File(['const a = 1'], 'plugin.js', { type: 'text/javascript' })
  )
  await waitFor(() => expect(footerButton('Upload')).toBeEnabled())
  await user.click(footerButton('Upload'))
}

afterEach(() => {
  for (const queryClient of queryClients) queryClient.clear()
  queryClients.length = 0
  vi.unstubAllGlobals()
})

describe('UploadDialog layout', () => {
  test('renders the source editor and hides the native file input from view', () => {
    renderDialog()

    expect(sourceEditor()).toBeInTheDocument()
    // The unstyled native picker is the element that made this dialog look
    // foreign; it must stay in the DOM (labelled) but never be the visible
    // control.
    expect(fileInput()).toHaveClass('sr-only')
    expect(
      screen.getByRole('button', { name: 'Choose file' })
    ).toBeInTheDocument()
    expect(screen.getByText('0 bytes')).toBeInTheDocument()
  })

  test('scrolls its body instead of growing past the viewport', () => {
    renderDialog()

    const content = document.querySelector('[data-slot=dialog-content]')
    const body = content?.querySelector(':scope > div:nth-child(2)')
    expect(content).toHaveClass(
      'max-h-[var(--dialog-available-height,calc(100dvh-2rem))]'
    )
    expect(body).toHaveClass('overflow-y-auto')
  })

  test('does not steal focus into the mid-dialog source editor on open', () => {
    renderDialog()

    expect(document.querySelector('.cm-content')).not.toHaveFocus()
  })
})

describe('UploadDialog file selection', () => {
  test('fills the source editor and names the chosen file', async () => {
    const user = userEvent.setup()
    renderDialog()

    await user.upload(
      fileInput(),
      new File(['export const meta = {}'], 'plugin.js', {
        type: 'text/javascript',
      })
    )

    expect(await screen.findByText('plugin.js')).toBeInTheDocument()
    await waitFor(() =>
      expect(document.querySelector('.cm-content')?.textContent).toContain(
        'export const meta = {}'
      )
    )
    expect(
      screen.getByRole('button', { name: 'Choose another file' })
    ).toBeInTheDocument()
  })

  test('rejects a file over the 8 MiB limit without touching the source', async () => {
    const user = userEvent.setup()
    renderDialog()

    const oversized = new File(['x'], 'huge.js', { type: 'text/javascript' })
    Object.defineProperty(oversized, 'size', {
      value: MAX_PLUGIN_SOURCE_BYTES + 1,
    })
    await user.upload(fileInput(), oversized)

    expect(
      await screen.findByText('Plugin source exceeds the 8 MiB limit.')
    ).toBeInTheDocument()
    expect(screen.queryByText('huge.js')).toBeNull()
    expect(footerButton('Upload')).toBeDisabled()
  })
})

describe('UploadDialog URL import', () => {
  test('keeps Fetch disabled until a URL is typed, then fills the source', async () => {
    const user = userEvent.setup()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('const fetched = 1', { status: 200 }))
    )
    renderDialog()

    expect(screen.getByRole('button', { name: 'Fetch' })).toBeDisabled()

    await user.type(
      screen.getByLabelText('Import from URL'),
      'https://example.com/plugin.js'
    )
    expect(screen.getByRole('button', { name: 'Fetch' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: 'Fetch' }))

    await waitFor(() =>
      expect(document.querySelector('.cm-content')?.textContent).toContain(
        'const fetched = 1'
      )
    )
    // Fetching must never upload on its own.
    expect(uploadTaskPlugin).not.toHaveBeenCalled()
  })

  test('fetches on Enter and marks the field invalid when the URL is not absolute', async () => {
    const user = userEvent.setup()
    renderDialog()

    const urlField = screen.getByLabelText('Import from URL')
    await user.type(urlField, 'plugin.js{Enter}')

    expect(
      await screen.findByText('Enter an absolute http(s) URL.')
    ).toBeInTheDocument()
    expect(urlField).toHaveAttribute('aria-invalid', 'true')
    expect(document.querySelector('[data-slot=field-error]')).toHaveAttribute(
      'role',
      'alert'
    )
  })
})

describe('UploadDialog upload lifecycle', () => {
  test.each([undefined, 'demo'])(
    'offers activation after saving an inactive version from %s',
    async (initialKey) => {
      const user = userEvent.setup()
      uploadTaskPlugin.mockResolvedValue(savedPlugin(false))
      let resolveActivation!: () => void
      activateTaskPlugin.mockImplementation(
        () =>
          new Promise<void>((resolve) => {
            resolveActivation = resolve
          })
      )
      const { queryClient, onOpenChange } = renderDialog(true, initialKey)
      queryClient.setQueryData(['task-plugin', 'demo'], { cached: true })
      queryClient.setQueryData(['task-plugin-versions', 'demo'], [])
      queryClient.setQueryData(['task-plugins'], [])

      await uploadSource(user)

      expect(
        await screen.findByText('Plugin saved, activation required')
      ).toBeVisible()
      expect(
        screen.getByText(
          'This version is saved but not active. Activate it to replace the current version, or activate it later from the Versions tab.'
        )
      ).toBeVisible()
      expect(
        screen.queryByRole('textbox', { name: 'Plugin source' })
      ).not.toBeInTheDocument()
      expect(activateTaskPlugin).not.toHaveBeenCalled()
      expect(footerButton('Activate now')).toHaveFocus()
      // Reset the cached queries so activation must invalidate them again.
      queryClient.setQueryData(['task-plugin', 'demo'], { cached: true })
      queryClient.setQueryData(['task-plugin-versions', 'demo'], [])
      queryClient.setQueryData(['task-plugins'], [])
      await user.keyboard('{Enter}')

      expect(activateTaskPlugin).toHaveBeenCalledWith('demo', '2.0.0')
      expect(footerButton('Activating...')).toBeDisabled()
      expect(footerButton('Later')).toBeDisabled()
      await user.keyboard('{Escape}')
      expect(onOpenChange).not.toHaveBeenCalled()
      resolveActivation()

      expect(await screen.findByText('Plugin version activated')).toBeVisible()
      expect(
        screen.queryByRole('button', { name: 'Activate now' })
      ).not.toBeInTheDocument()
      expect(footerButton('Close')).toHaveFocus()
      for (const queryKey of [
        ['task-plugins'],
        ['task-plugin', 'demo'],
        ['task-plugin-versions', 'demo'],
      ]) {
        expect(queryClient.getQueryState(queryKey)?.isInvalidated).toBe(true)
      }
    }
  )

  test('keeps the saved version available to retry when activation fails', async () => {
    const user = userEvent.setup()
    uploadTaskPlugin.mockResolvedValue(savedPlugin(false))
    activateTaskPlugin
      .mockRejectedValueOnce(new Error('activation failed'))
      .mockResolvedValueOnce(undefined)
    renderDialog()
    await uploadSource(user)
    await user.click(
      await screen.findByRole('button', { name: 'Activate now' })
    )

    expect(await screen.findByText('activation failed')).toBeVisible()
    expect(screen.getByText('Plugin saved, activation required')).toBeVisible()
    expect(footerButton('Activate now')).toBeEnabled()
    await user.click(footerButton('Activate now'))
    expect(await screen.findByText('Plugin version activated')).toBeVisible()
    expect(uploadTaskPlugin).toHaveBeenCalledTimes(1)
  })

  test('allows postponing activation without changing the active version', async () => {
    const user = userEvent.setup()
    uploadTaskPlugin.mockResolvedValue(savedPlugin(false))
    const { onOpenChange } = renderDialog()
    await uploadSource(user)
    await user.click(await screen.findByRole('button', { name: 'Later' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(activateTaskPlugin).not.toHaveBeenCalled()
  })

  test('shows an already active upload as activated without another activation request', async () => {
    const user = userEvent.setup()
    uploadTaskPlugin.mockResolvedValue(savedPlugin(true))
    renderDialog()
    await uploadSource(user)
    expect(await screen.findByText('Plugin version activated')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Activate now' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /^Upload$/ })
    ).not.toBeInTheDocument()
    expect(activateTaskPlugin).not.toHaveBeenCalled()
  })

  test('disables Upload while the source is empty and shows a pending label', async () => {
    const user = userEvent.setup()
    let resolveUpload: (value: unknown) => void = () => undefined
    uploadTaskPlugin.mockImplementation(
      () => new Promise((resolve) => (resolveUpload = resolve))
    )
    renderDialog()

    expect(footerButton('Upload')).toBeDisabled()

    await user.upload(
      fileInput(),
      new File(['const a = 1'], 'plugin.js', { type: 'text/javascript' })
    )
    const uploadButton = await waitFor(() => {
      const button = footerButton('Upload')
      expect(button).toBeEnabled()
      return button
    })

    await user.click(uploadButton)
    await waitFor(() => expect(footerButton(/Uploading/)).toBeDisabled())

    resolveUpload(savedPlugin(true))
    expect(
      await screen.findByText('Plugin version activated')
    ).toBeInTheDocument()
  })

  test('surfaces an upload rejection verbatim', async () => {
    const user = userEvent.setup()
    uploadTaskPlugin.mockRejectedValue(new Error('key conflicts with `demo`'))
    renderDialog()

    await user.upload(
      fileInput(),
      new File(['const a = 1'], 'plugin.js', { type: 'text/javascript' })
    )
    await waitFor(() => expect(footerButton('Upload')).toBeEnabled())
    await user.click(footerButton('Upload'))

    expect(
      await screen.findByText('key conflicts with `demo`')
    ).toBeInTheDocument()
  })

  test('clears every field when the dialog is closed', async () => {
    const user = userEvent.setup()
    const { onOpenChange, queryClient, view } = renderDialog()

    await user.upload(
      fileInput(),
      new File(['const a = 1'], 'plugin.js', { type: 'text/javascript' })
    )
    await user.type(screen.getByLabelText('Import from URL'), 'plugin.js')
    await screen.findByText('plugin.js')

    await user.click(footerButton('Close'))
    expect(onOpenChange).toHaveBeenCalledWith(false)

    view.rerender(
      <QueryClientProvider client={queryClient}>
        <UploadDialog open onOpenChange={onOpenChange} />
      </QueryClientProvider>
    )

    expect(screen.getByLabelText('Import from URL')).toHaveValue('')
    expect(screen.getByText('0 bytes')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Choose file' })
    ).toBeInTheDocument()
  })
})
