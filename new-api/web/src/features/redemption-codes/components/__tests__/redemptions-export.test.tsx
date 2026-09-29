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
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { RedemptionsExportDialog } from '../redemptions-export-dialog'
import { RedemptionsMutateDrawer } from '../redemptions-mutate-drawer'
import { RedemptionsProvider } from '../redemptions-provider'

type Download = { filename: string; blob: Blob }

function captureDownloads() {
  const downloads: Download[] = []
  let currentBlob: Blob
  vi.stubGlobal(
    'URL',
    Object.assign(class extends URL {}, {
      createObjectURL: vi.fn((blob: Blob) => {
        currentBlob = blob
        return 'blob:redemption-export'
      }),
      revokeObjectURL: vi.fn(),
    })
  )
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(
    function (this: HTMLAnchorElement) {
      downloads.push({ filename: this.download, blob: currentBlob })
    }
  )
  return downloads
}

function readDownload(download: Download): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result)), {
      once: true,
    })
    reader.addEventListener('error', () => reject(reader.error), { once: true })
    reader.readAsText(download.blob)
  })
}

function CreateDrawer() {
  const [open, setOpen] = useState(true)
  return (
    <RedemptionsProvider>
      <RedemptionsMutateDrawer open={open} onOpenChange={setOpen} />
    </RedemptionsProvider>
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  localStorage.clear()
})

test.each([
  {
    name: true,
    quota: true,
    txt: 'launch\tcodeA\t$10.00\nlaunch\tcodeB\t$10.00\n',
    md: '| Name | Code | Quota |\n| --- | --- | --- |\n| launch | codeA | $10.00 |\n| launch | codeB | $10.00 |\n',
  },
  {
    name: true,
    quota: false,
    txt: 'launch\tcodeA\nlaunch\tcodeB\n',
    md: '| Name | Code |\n| --- | --- |\n| launch | codeA |\n| launch | codeB |\n',
  },
  {
    name: false,
    quota: true,
    txt: 'codeA\t$10.00\ncodeB\t$10.00\n',
    md: '| Code | Quota |\n| --- | --- |\n| codeA | $10.00 |\n| codeB | $10.00 |\n',
  },
  {
    name: false,
    quota: false,
    txt: 'codeA\ncodeB\n',
    md: '| Code |\n| --- |\n| codeA |\n| codeB |\n',
  },
])(
  'exports TXT and Markdown with name=$name and quota=$quota',
  async (options) => {
    const downloads = captureDownloads()
    const user = userEvent.setup()
    for (const format of ['txt', 'md'] as const) {
      const onClose = vi.fn()
      const view = render(
        <RedemptionsExportDialog
          data={{ keys: ['codeA', 'codeB'], name: 'launch', quota: '$10.00' }}
          onClose={onClose}
        />
      )
      await user.click(screen.getByRole('checkbox', { name: 'Save as a file' }))
      await user.click(
        screen.getByRole('radio', {
          name: format === 'txt' ? 'Save as TXT' : 'Save as Markdown',
        })
      )
      const name = screen.getByRole('checkbox', { name: 'Include name' })
      const quota = screen.getByRole('checkbox', { name: 'Include quota' })
      expect(name).not.toHaveAttribute('aria-disabled', 'true')
      expect(quota).not.toHaveAttribute('aria-disabled', 'true')
      expect(name).toBeChecked()
      expect(quota).toBeChecked()
      if (!options.name) await user.click(name)
      if (!options.quota) {
        quota.focus()
        await user.keyboard(' ')
      }
      await user.click(screen.getByRole('button', { name: 'Done' }))
      const download = downloads[format === 'txt' ? 0 : 1]
      expect(download.filename).toMatch(
        new RegExp(`^redemption-codes-\\d+\\.${format}$`)
      )
      expect(download.blob.type).toBe(
        format === 'txt'
          ? 'text/plain;charset=utf-8'
          : 'text/markdown;charset=utf-8'
      )
      expect(await readDownload(download)).toBe(options[format])
      expect(onClose).toHaveBeenCalledOnce()
      view.unmount()
    }
    expect(downloads).toHaveLength(2)
  }
)

test('keeps names containing table delimiters and markup inside one Markdown cell', async () => {
  const downloads = captureDownloads()
  const user = userEvent.setup()
  render(
    <RedemptionsExportDialog
      data={{ keys: ['codeA'], name: 'A | [B]\n<x>', quota: '$10' }}
      onClose={() => undefined}
    />
  )
  await user.click(screen.getByRole('checkbox', { name: 'Save as a file' }))
  await user.click(screen.getByRole('radio', { name: 'Save as Markdown' }))
  await user.click(screen.getByRole('button', { name: 'Done' }))
  expect(await readDownload(downloads[0])).toBe(
    '| Name | Code | Quota |\n| --- | --- | --- |\n| A \\| \\[B\\] \\<x\\> | codeA | $10 |\n'
  )
})

test('successful batch creation opens export with returned codes and the configured currency', async () => {
  const downloads = captureDownloads()
  const user = userEvent.setup()
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7.2,
    },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: ['createdA', 'createdB'] },
  })
  render(<CreateDrawer />)
  const createDialog = screen.getByRole('dialog', {
    name: 'Create Redemption Code',
  })
  fireEvent.change(within(createDialog).getByLabelText('Name'), {
    target: { value: 'batch' },
  })
  fireEvent.change(within(createDialog).getByLabelText('Quantity'), {
    target: { value: '2' },
  })
  fireEvent.change(within(createDialog).getByLabelText('Quota (CNY)'), {
    target: { value: '2000' },
  })
  await user.click(
    within(createDialog).getByRole('button', { name: 'Save changes' })
  )
  const exportDialog = await screen.findByRole('dialog', {
    name: 'Redemption codes created',
  })
  expect(exportDialog).toHaveTextContent(
    'Successfully created 2 redemption codes'
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Create Redemption Code' })
    ).not.toBeInTheDocument()
  )
  await user.click(
    within(exportDialog).getByRole('checkbox', { name: 'Save as a file' })
  )
  await user.click(
    within(exportDialog).getByRole('radio', { name: 'Save as TXT' })
  )
  expect(downloads).toHaveLength(0)
  await user.click(within(exportDialog).getByRole('button', { name: 'Done' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Redemption codes created' })
    ).not.toBeInTheDocument()
  )
  expect(await readDownload(downloads[0])).toBe(
    'batch\tcreatedA\t¥2,000\nbatch\tcreatedB\t¥2,000\n'
  )
})

test('failed creation does not open a success export dialog', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Creation failed' },
  })
  render(<CreateDrawer />)
  fireEvent.change(screen.getByLabelText('Name'), {
    target: { value: 'batch' },
  })
  await user.click(screen.getByRole('button', { name: 'Save changes' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  )
  expect(
    screen.queryByRole('dialog', { name: 'Redemption codes created' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('dialog', { name: 'Create Redemption Code' })
  ).toBeVisible()
})

test('completion defaults to no file and closes without downloading', async () => {
  const downloads = captureDownloads()
  const user = userEvent.setup()
  const onClose = vi.fn()
  render(
    <RedemptionsExportDialog
      data={{ keys: ['codeA'], name: 'launch', quota: '$10' }}
      onClose={onClose}
    />
  )
  expect(
    screen.getByRole('checkbox', { name: 'Save as a file' })
  ).not.toBeChecked()
  expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('checkbox', { name: 'Include name' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('checkbox', { name: 'Include quota' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Done' }))
  expect(downloads).toEqual([])
  expect(onClose).toHaveBeenCalledOnce()
})

test('unchecking save as a file hides export settings and completes without downloading', async () => {
  const downloads = captureDownloads()
  const user = userEvent.setup()
  const onClose = vi.fn()
  render(
    <RedemptionsExportDialog
      data={{ keys: ['codeA'], name: 'launch', quota: '$10' }}
      onClose={onClose}
    />
  )
  const saveFile = screen.getByRole('checkbox', { name: 'Save as a file' })
  saveFile.focus()
  await user.keyboard(' ')
  expect(screen.getByRole('radio', { name: 'Save as TXT' })).toBeChecked()
  await user.click(screen.getByRole('radio', { name: 'Save as Markdown' }))
  await user.click(saveFile)
  expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('checkbox', { name: 'Include name' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('checkbox', { name: 'Include quota' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Done' }))
  expect(downloads).toEqual([])
  expect(onClose).toHaveBeenCalledOnce()
})
