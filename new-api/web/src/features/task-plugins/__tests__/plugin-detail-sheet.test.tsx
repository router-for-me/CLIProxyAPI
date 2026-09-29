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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { PluginDetailSheet } from '../components/plugin-detail-sheet'
import type {
  TaskPluginDetail,
  TaskPluginListItem,
  TaskPluginMeta,
} from '../types'

const queryClients: QueryClient[] = []

function makeItem(): TaskPluginListItem {
  return {
    meta: {
      apiVersion: 1,
      key: 'kling',
      name: 'Kling',
      version: '1.2.3',
      author: { name: 'acme' },
      models: ['kling-v1'],
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
}

function renderSheet(metaOverrides: Partial<TaskPluginMeta>) {
  const item = makeItem()
  const detail: TaskPluginDetail = {
    meta: { ...item.meta, ...metaOverrides },
    source: '',
    layer: 'factory',
  }
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
    },
  })
  queryClient.setQueryData(['task-plugin', item.meta.key], detail)
  queryClient.setQueryData(['task-plugin-versions', item.meta.key], [])
  queryClients.push(queryClient)
  const view = render(
    <QueryClientProvider client={queryClient}>
      <PluginDetailSheet plugin={item} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  return { ...view, queryClient, item, detail }
}

/**
 * The endpoint row that renders `path`, i.e. the list item holding both the
 * method badge and the path. Annotations such as the supported request forms
 * belong to a specific endpoint, so ownership is asserted through this row
 * rather than through document-wide presence.
 */
function endpointRow(path: string): HTMLElement {
  const row = screen.getByText(path).closest('li')
  if (!row) throw new Error(`no endpoint row for ${path}`)
  return row
}

afterEach(() => {
  for (const queryClient of queryClients) queryClient.clear()
  queryClients.length = 0
})

describe('PluginDetailSheet metadata fields', () => {
  test('given a plugin whose manifest cannot declare actions, no actions row is rendered in the metadata card', async () => {
    renderSheet({ protocols: ['openai_video'] })

    // 'Actions' remains a column header in the version-history table, so the
    // removed metadata row is asserted through the metadata card's own list.
    const region = await screen.findByRole('region', {
      name: 'Plugin metadata',
    })
    const metadataList = region.querySelector('dl')
    expect(metadataList).not.toBeNull()
    expect(metadataList?.textContent).not.toContain('Actions')
  })

  test('given a plugin with several models, the models value renders as one wrapping list', async () => {
    renderSheet({ models: ['kling-v1', 'kling-v1-6', 'kling-v2-master'] })

    const models = await screen.findByRole('region', { name: 'Models' })
    for (const name of ['kling-v1', 'kling-v1-6', 'kling-v2-master']) {
      expect(within(models).getByText(name)).toBeVisible()
    }
  })
})

describe('PluginDetailSheet host protocol endpoints', () => {
  test('given an openai_responses claim, both the create and the retrieve endpoint are listed', async () => {
    renderSheet({
      protocols: [{ name: 'openai_responses', supports: ['stream'] }],
    })

    expect(await screen.findByText('/v1/responses')).toBeInTheDocument()
    expect(screen.getByText('/v1/responses/{response_id}')).toBeInTheDocument()
    expect(endpointRow('/v1/responses').textContent).toContain('POST')
    expect(endpointRow('/v1/responses/{response_id}').textContent).toContain(
      'GET'
    )
  })

  test('given an object claim with all supports, the three mode chips sit on the create row', async () => {
    renderSheet({
      protocols: [
        {
          name: 'openai_responses',
          supports: ['stream', 'sync', 'background'],
        },
      ],
    })

    await screen.findByText('/v1/responses')

    const createRow = endpointRow('/v1/responses')
    expect(createRow).toContainElement(screen.getByText('stream'))
    expect(createRow).toContainElement(screen.getByText('sync'))
    expect(createRow).toContainElement(screen.getByText('background'))
  })

  test('given an object claim with all supports, the retrieve row carries no mode chips', async () => {
    renderSheet({
      protocols: [
        {
          name: 'openai_responses',
          supports: ['stream', 'sync', 'background'],
        },
      ],
    })

    await screen.findByText('/v1/responses/{response_id}')

    const retrieveRow = endpointRow('/v1/responses/{response_id}')
    expect(retrieveRow.textContent).not.toContain('stream')
    expect(retrieveRow.textContent).not.toContain('sync')
    expect(retrieveRow.textContent).not.toContain('background')
  })

  test('given an object claim supporting only stream, the other mode chips are absent', async () => {
    renderSheet({
      protocols: [{ name: 'openai_responses', supports: ['stream'] }],
    })

    expect(await screen.findByText('stream')).toBeInTheDocument()
    expect(screen.queryByText('sync')).toBeNull()
    expect(screen.queryByText('background')).toBeNull()
  })

  test('given a string claim, its three endpoints render without any mode chip', async () => {
    renderSheet({ protocols: ['openai_video'] })

    expect(await screen.findByText('/v1/videos')).toBeInTheDocument()
    expect(screen.getByText('/v1/videos/{task_id}')).toBeInTheDocument()
    expect(screen.getByText('/v1/videos/{task_id}/content')).toBeInTheDocument()
    expect(screen.queryByText('stream')).toBeNull()
    expect(screen.queryByText('sync')).toBeNull()
    expect(screen.queryByText('background')).toBeNull()
  })

  test('given an openai_image string claim, both synchronous image endpoints render as submit rows without mode chips', async () => {
    renderSheet({ protocols: ['openai_image'] })

    expect(
      await screen.findByText('/v1/images/generations')
    ).toBeInTheDocument()
    expect(screen.getByText('/v1/images/edits')).toBeInTheDocument()
    expect(endpointRow('/v1/images/generations').textContent).toContain('POST')
    expect(endpointRow('/v1/images/edits').textContent).toContain('POST')
    expect(screen.queryByText('Retrieve result')).toBeNull()
    expect(screen.queryByText('stream')).toBeNull()
    expect(screen.queryByText('sync')).toBeNull()
    expect(screen.queryByText('background')).toBeNull()
  })

  test('given a claim narrowing the protocol to a model subset, the subset is marked without printing the model list', async () => {
    renderSheet({
      models: ['kling-v1', 'kling-v2-master'],
      protocols: [{ name: 'openai_video', models: ['kling-v1'] }],
    })

    const user = userEvent.setup()
    const hint = await screen.findByRole('button', { name: 'Model scope' })
    hint.focus()
    await user.keyboard('{Enter}')
    const scope = screen.getByRole('dialog', { name: 'Model scope' })
    expect(within(scope).getByText('kling-v1')).toBeVisible()
    expect(within(scope).queryByText('kling-v2-master')).toBeNull()
  })

  test('given a claim binding every model, no model scope marker is rendered', async () => {
    renderSheet({ protocols: ['openai_video'] })

    await screen.findByText('/v1/videos')
    expect(screen.queryByText('Model scope')).toBeNull()
  })
})

describe('PluginDetailSheet native routes', () => {
  test('given declared native routes, each renders its method, path and type', async () => {
    renderSheet({
      routes: [
        {
          method: 'POST',
          path: '/kling/v1/videos/text2video',
          type: 'submit',
        },
        {
          method: 'GET',
          path: '/kling/v1/videos/text2video/:task_id',
          type: 'query',
        },
      ],
    })

    expect(
      await screen.findByText('Plugin-defined interfaces')
    ).toBeInTheDocument()
    const submitRow = endpointRow('/kling/v1/videos/text2video')
    expect(submitRow.textContent).toContain('POST')
    expect(submitRow.textContent).toContain('submit')
    const queryRow = endpointRow('/kling/v1/videos/text2video/:task_id')
    expect(queryRow.textContent).toContain('GET')
    expect(queryRow.textContent).toContain('query')
  })

  test('given no protocols and no routes, the endpoint section falls back to a single placeholder', async () => {
    renderSheet({ protocols: [], routes: [] })

    expect(await screen.findByText('Endpoints')).toBeInTheDocument()
    expect(screen.queryByText('Plugin-defined interfaces')).toBeNull()
    expect(
      within(screen.getByRole('region', { name: 'Endpoints' })).getByText(
        'Not declared'
      )
    ).toBeVisible()
  })
})

test('shows the plugin sort priority and HTTPS website in details', async () => {
  renderSheet({ sortPriority: 42, website: 'https://example.com/plugin' })
  expect(await screen.findByText('Sort priority')).toBeInTheDocument()
  expect(screen.getByText('42')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Plugin website' })).toHaveAttribute(
    'href',
    'https://example.com/plugin'
  )
})

test('defaults to overview and switches tabs with the keyboard', async () => {
  const user = userEvent.setup()
  renderSheet({})
  const overview = screen.getByRole('tab', { name: 'Overview' })
  expect(overview).toHaveAttribute('aria-selected', 'true')
  await user.click(overview)
  await user.keyboard('{ArrowRight}')
  await waitFor(() =>
    expect(
      screen.getByRole('tab', { name: 'Billing parameters' })
    ).toHaveFocus()
  )
  await user.keyboard('{Enter}')
  expect(
    screen.getByRole('tab', { name: 'Billing parameters' })
  ).toHaveAttribute('aria-selected', 'true')
  await waitFor(() =>
    expect(screen.getByText('No billing parameters declared')).toBeVisible()
  )
  expect(screen.queryByRole('region', { name: 'Models' })).toBeNull()
  await user.click(screen.getByRole('tab', { name: 'Plugin source' }))
  await waitFor(() =>
    expect(screen.getByText('No source available')).toBeVisible()
  )
  await user.click(screen.getByRole('tab', { name: 'Version history' }))
  await waitFor(() =>
    expect(screen.getByText('No version history')).toBeVisible()
  )
})

test('copies model names with newlines and copies original URL, key and endpoint path', async () => {
  const user = userEvent.setup()
  const clipboard = vi
    .spyOn(navigator.clipboard, 'writeText')
    .mockResolvedValue()
  renderSheet({
    models: ['a', 'b'],
    baseUrl: 'http://localhost:3000',
    protocols: ['openai_video'],
  })
  await user.click(screen.getByRole('button', { name: 'Copy all models' }))
  expect(clipboard).toHaveBeenLastCalledWith('a\nb')
  await user.click(screen.getByRole('button', { name: 'Copy base URL' }))
  expect(clipboard).toHaveBeenLastCalledWith('http://localhost:3000')
  await user.click(screen.getByRole('button', { name: 'Copy plugin key' }))
  expect(clipboard).toHaveBeenLastCalledWith('kling')
  await user.click(
    within(endpointRow('/v1/videos/{task_id}')).getByRole('button', {
      name: 'Copy endpoint path',
    })
  )
  expect(clipboard).toHaveBeenLastCalledWith('/v1/videos/{task_id}')
})

test('mounts sandbox on first visit and preserves input when switching tabs', async () => {
  const createRange = document.createRange.bind(document)
  vi.spyOn(document, 'createRange').mockImplementation(() => {
    const range = createRange()
    Object.defineProperties(range, {
      getBoundingClientRect: { value: () => new DOMRect() },
      getClientRects: { value: () => Object.assign([], { item: () => null }) },
    })
    return range
  })
  const user = userEvent.setup()
  renderSheet({})
  expect(
    screen.queryByRole('textbox', { name: 'Arguments JSON', hidden: true })
  ).toBeNull()
  await user.click(screen.getByRole('tab', { name: 'Plugin sandbox' }))
  const editor = screen.getByRole('textbox', { name: 'Arguments JSON' })
  const editable = editor.querySelector<HTMLElement>('[contenteditable=true]')
  if (!editable) throw new Error('Missing editable sandbox input')
  act(() => editable.focus())
  await user.paste('42')
  expect(editor).toHaveTextContent('42')
  await user.click(screen.getByRole('tab', { name: 'Overview' }))
  expect(editor).not.toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Plugin sandbox' }))
  expect(
    screen.getByRole('textbox', { name: 'Arguments JSON' })
  ).toHaveTextContent('42')
})

test('closing and reopening resets navigation and restores focus to the opener', async () => {
  const user = userEvent.setup()
  const fixture = renderSheet({})
  fixture.unmount()
  function Harness() {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button type='button' onClick={() => setOpen(true)}>
          Open plugin
        </button>
        <PluginDetailSheet
          plugin={open ? fixture.item : null}
          onOpenChange={setOpen}
        />
      </>
    )
  }
  render(
    <QueryClientProvider client={fixture.queryClient}>
      <Harness />
    </QueryClientProvider>
  )
  const opener = screen.getByRole('button', { name: 'Open plugin' })
  await user.click(opener)
  await user.click(screen.getByRole('tab', { name: 'Plugin source' }))
  await user.keyboard('{Escape}')
  expect(opener).toHaveFocus()
  await user.click(opener)
  expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
})

test('detail loading and failure show feedback and retry recovers the overview', async () => {
  const fixture = renderSheet({})
  fixture.unmount()
  fixture.queryClient.removeQueries({ queryKey: ['task-plugin', 'kling'] })
  let rejectRequest!: (reason: Error) => void
  const request = vi.spyOn(api, 'get').mockImplementationOnce(
    () =>
      new Promise((_resolve, reject) => {
        rejectRequest = reject
      })
  )
  render(
    <QueryClientProvider client={fixture.queryClient}>
      <PluginDetailSheet plugin={fixture.item} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  expect(screen.getByText('Loading...')).toBeVisible()
  await act(async () => rejectRequest(new Error('Detail unavailable')))
  await waitFor(() =>
    expect(screen.getByText('Detail unavailable')).toBeVisible()
  )
  request.mockResolvedValueOnce({
    data: { success: true, data: fixture.detail },
  })
  await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('region', { name: 'Models' })).toBeVisible()
})

test('keeps header and navigation outside the scrolling content with long metadata', async () => {
  renderSheet({
    models: ['very-long-model-name-'.repeat(20)],
    routes: [
      { method: 'GET', path: '/very-long-path/'.repeat(20), type: 'query' },
    ],
  })
  const panel = screen.getByRole('tabpanel', { name: 'Overview' })
  expect(panel).toHaveClass('min-h-0', 'overflow-y-auto')
  expect(panel).not.toContainElement(screen.getByRole('tablist'))
  expect(panel).not.toContainElement(screen.getByRole('heading', { level: 2 }))
  expect(screen.getByText('very-long-model-name-'.repeat(20))).toHaveClass(
    'break-all',
    'h-auto'
  )
  expect(screen.getByText('/very-long-path/'.repeat(20))).toHaveClass(
    'break-all'
  )
})

test('version loading and failure allow retry without hiding the overview', async () => {
  const user = userEvent.setup()
  const fixture = renderSheet({})
  fixture.unmount()
  fixture.queryClient.removeQueries({ queryKey: ['task-plugin-versions'] })
  let rejectRequest!: (reason: Error) => void
  const request = vi.spyOn(api, 'get').mockImplementationOnce(
    () =>
      new Promise((_resolve, reject) => {
        rejectRequest = reject
      })
  )
  render(
    <QueryClientProvider client={fixture.queryClient}>
      <PluginDetailSheet plugin={fixture.item} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  expect(screen.getByRole('region', { name: 'Models' })).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Version history' }))
  expect(screen.getByText('Loading...')).toBeVisible()
  await act(async () => rejectRequest(new Error('Versions unavailable')))
  await waitFor(() =>
    expect(screen.getByText('Versions unavailable')).toBeVisible()
  )
  request.mockResolvedValueOnce({ data: { success: true, data: [] } })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() =>
    expect(screen.getByText('No version history')).toBeVisible()
  )
})

test('switching plugins returns to overview and never carries the previous comparison', async () => {
  const user = userEvent.setup()
  const fixture = renderSheet({})
  const nextItem = {
    ...fixture.item,
    meta: { ...fixture.item.meta, key: 'next', name: 'Next plugin' },
  }
  fixture.queryClient.setQueryData(['task-plugin', 'next'], {
    ...fixture.detail,
    meta: nextItem.meta,
  })
  fixture.queryClient.setQueryData(['task-plugin-versions', 'next'], [])
  await user.click(screen.getByRole('tab', { name: 'Source diff' }))
  fixture.rerender(
    <QueryClientProvider client={fixture.queryClient}>
      <PluginDetailSheet plugin={nextItem} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent(
    'Next plugin'
  )
})

test('comparison selection shows loading, retries failure, and resets after reopening', async () => {
  const user = userEvent.setup()
  const fixture = renderSheet({})
  await act(async () => {
    fixture.queryClient.setQueryData(
      ['task-plugin-versions', 'kling'],
      [
        { id: 1, version: '1.2.3', active: true },
        { id: 2, version: '1.0.0', active: false },
      ]
    )
  })
  let rejectRequest!: (reason: Error) => void
  const request = vi.spyOn(api, 'get').mockImplementationOnce(
    () =>
      new Promise((_resolve, reject) => {
        rejectRequest = reject
      })
  )
  await user.click(screen.getByRole('tab', { name: 'Source diff' }))
  const selector = screen.getByRole('combobox', {
    name: 'Select a version to compare',
  })
  expect(selector).toHaveValue('')
  await user.click(selector)
  await user.click(await screen.findByRole('option', { name: '1.0.0' }))
  expect(screen.getByText('Loading...')).toBeVisible()
  await act(async () => rejectRequest(new Error('Comparison unavailable')))
  await waitFor(() =>
    expect(screen.getByText('Comparison unavailable')).toBeVisible()
  )
  request.mockResolvedValueOnce({
    data: {
      success: true,
      data: { ...fixture.detail, source: 'previous source' },
    },
  })
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText(/previous source/)).toBeVisible()
  fixture.rerender(
    <QueryClientProvider client={fixture.queryClient}>
      <PluginDetailSheet plugin={null} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  fixture.rerender(
    <QueryClientProvider client={fixture.queryClient}>
      <PluginDetailSheet plugin={fixture.item} onOpenChange={() => undefined} />
    </QueryClientProvider>
  )
  expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  await user.click(screen.getByRole('tab', { name: 'Source diff' }))
  expect(
    screen.getByRole('combobox', { name: 'Select a version to compare' })
  ).toHaveValue('')
  expect(screen.queryByText(/previous source/)).toBeNull()
})

test.each([true, false])(
  'shows model usage profiles and only displays defaults for ungrouped models (%s)',
  async (hasDefaultModel) => {
    const user = userEvent.setup()
    renderSheet({
      models: hasDefaultModel
        ? ['image', 'video', 'legacy']
        : ['image', 'video'],
      usageSchema: {
        fallback_units: { type: 'number', unit: 'count' },
      },
      usageProfiles: [
        {
          models: ['image'],
          schema: { image_count: { type: 'number', unit: 'count' } },
        },
        {
          models: ['video'],
          schema: {
            seconds: { type: 'number', unit: 'second' },
            resolution: { enum: ['720P', '1080P'] },
          },
        },
      ],
    })
    await user.click(screen.getByRole('tab', { name: 'Billing parameters' }))
    const image = within(screen.getByRole('region', { name: 'image' }))
    expect(image.getByText('image_count')).toBeVisible()
    expect(image.queryByText('seconds')).not.toBeInTheDocument()
    expect(image.queryByText('resolution')).not.toBeInTheDocument()
    const video = within(screen.getByRole('region', { name: 'video' }))
    expect(video.getByText('seconds')).toBeVisible()
    expect(video.getByText('resolution')).toBeVisible()
    expect(video.queryByText('image_count')).not.toBeInTheDocument()
    if (hasDefaultModel) {
      const defaults = within(screen.getByRole('region', { name: 'Default' }))
      expect(defaults.getByText('legacy')).toBeVisible()
      expect(defaults.getByText('fallback_units')).toBeVisible()
      expect(defaults.queryByText('image')).not.toBeInTheDocument()
    } else {
      expect(
        screen.queryByRole('region', { name: 'Default' })
      ).not.toBeInTheDocument()
      expect(screen.queryByText('fallback_units')).not.toBeInTheDocument()
    }
  }
)

test('keeps the billing parameter table for plugins without usage profiles', async () => {
  const user = userEvent.setup()
  renderSheet({
    usageSchema: { seconds: { type: 'number', unit: 'second' } },
  })
  await user.click(screen.getByRole('tab', { name: 'Billing parameters' }))
  expect(within(screen.getByRole('table')).getByText('seconds')).toBeVisible()
  expect(
    screen.queryByText('No billing parameters declared')
  ).not.toBeInTheDocument()
})
