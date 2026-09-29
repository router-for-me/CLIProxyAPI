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
  act,
  render,
  renderHook,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

import { useChannelUpstreamUpdates } from '../../hooks/use-channel-upstream-updates'
import { UpstreamUpdateDialog } from '../dialogs/upstream-update-dialog'

const detectionResponse = {
  data: {
    success: true,
    data: { add_models: ['gpt-new', 'gpt-extra'], remove_models: ['gpt-old'] },
  },
}
const applyResponse = {
  data: {
    success: true,
    data: {
      added_models: ['gpt-new'],
      removed_models: ['gpt-old'],
      remaining_models: ['gpt-extra'],
      remaining_remove_models: [],
    },
  },
}

function UpstreamUpdateHarness(props: { refresh?: () => Promise<void> }) {
  const upstream = useChannelUpstreamUpdates(
    props.refresh ?? (() => Promise.resolve())
  )
  return (
    <>
      <Button
        onClick={() => upstream.detectChannelUpdates({ id: 42, name: 'batch' })}
      >
        Detect
      </Button>
      <Button
        onClick={() =>
          upstream.openModal(
            { id: 43, name: 'other channel' },
            [],
            ['other-old'],
            'remove'
          )
        }
      >
        Other channel
      </Button>
      <UpstreamUpdateDialog upstream={upstream} />
    </>
  )
}

describe('upstream update preview', () => {
  test('detecting changes opens a preview without applying them', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(detectionResponse)
    const { result } = renderHook(() =>
      useChannelUpstreamUpdates(vi.fn().mockResolvedValue(undefined))
    )
    await act(() =>
      result.current.detectChannelUpdates({ id: 42, name: 'batch' })
    )
    expect(result.current.showModal).toBe(true)
    expect(result.current.addModels).toEqual(['gpt-new', 'gpt-extra'])
    expect(result.current.removeModels).toEqual(['gpt-old'])
    expect(post).toHaveBeenCalledExactlyOnceWith(
      '/api/channel/upstream_updates/detect',
      { id: 42 },
      expect.anything()
    )
  })

  test('loading leads to selectable models and cancelling makes no apply request', async () => {
    let resolve!: (value: typeof detectionResponse) => void
    const promise = new Promise<typeof detectionResponse>((done) => {
      resolve = done
    })
    const post = vi.spyOn(api, 'post').mockReturnValue(promise)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    expect(
      screen.getByText(
        'Checking upstream models. Channel models will only change after confirmation.'
      )
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Review selected changes' })
    ).not.toBeInTheDocument()
    await act(async () => resolve(detectionResponse))
    expect(screen.getByRole('checkbox', { name: 'gpt-new' })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(post).toHaveBeenCalledTimes(1)
  })

  test('removals require explicit selection and a reopened preview resets selections', async () => {
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Other channel' }))
    const model = screen.getByRole('checkbox', { name: 'other-old' })
    expect(model).not.toBeChecked()
    expect(
      screen.getByRole('button', { name: 'Review selected changes' })
    ).toBeDisabled()
    await user.click(model)
    expect(model).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Other channel' }))
    expect(
      screen.getByRole('checkbox', { name: 'other-old' })
    ).not.toBeChecked()
  })

  test('review lists the selected changes and applies only after confirmation without ignoring unchecked models', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce(detectionResponse)
      .mockResolvedValueOnce(applyResponse)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    await user.click(await screen.findByRole('checkbox', { name: 'gpt-extra' }))
    await user.click(screen.getByRole('tab', { name: 'Remove Models (1)' }))
    const removal = screen.getByRole('checkbox', { name: 'gpt-old' })
    expect(removal).not.toBeChecked()
    await user.click(removal)
    await user.click(
      screen.getByRole('button', { name: 'Review selected changes' })
    )
    const confirmation = screen.getByRole('alertdialog')
    expect(within(confirmation).getByText('gpt-new')).toBeVisible()
    expect(within(confirmation).getByText('gpt-old')).toBeVisible()
    expect(
      within(confirmation).queryByText('gpt-extra')
    ).not.toBeInTheDocument()
    expect(post).toHaveBeenCalledTimes(1)
    await user.click(
      within(confirmation).getByRole('button', {
        name: 'Apply selected changes',
      })
    )
    expect(post).toHaveBeenLastCalledWith(
      '/api/channel/upstream_updates/apply',
      {
        id: 42,
        add_models: ['gpt-new'],
        remove_models: ['gpt-old'],
        ignore_models: [],
      },
      expect.anything()
    )
    const results = await screen.findByRole('dialog', {
      name: 'Update results',
    })
    expect(within(results).getByText('#42 batch')).toBeVisible()
    expect(
      within(
        within(results).getByRole('region', { name: 'Added models' })
      ).getByText('gpt-new')
    ).toBeVisible()
    expect(
      within(
        within(results).getByRole('region', { name: 'Removed models' })
      ).getByText('gpt-old')
    ).toBeVisible()
    expect(
      within(
        within(results).getByRole('region', { name: 'Still pending addition' })
      ).getByText('gpt-extra')
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Apply selected changes' })
    ).not.toBeInTheDocument()
    await waitFor(() => {
      expect(
        within(results).getAllByRole('button', { name: 'Close' })
      ).toContain(document.activeElement)
    })
  })

  test('cancelling the final confirmation returns to the same selection without saving', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(detectionResponse)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    await user.click(await screen.findByRole('checkbox', { name: 'gpt-extra' }))
    await user.click(
      screen.getByRole('button', { name: 'Review selected changes' })
    )
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    expect(screen.getByRole('checkbox', { name: 'gpt-new' })).toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'gpt-extra' })
    ).not.toBeChecked()
    expect(post).toHaveBeenCalledTimes(1)
  })

  test('detection failures remain visible and retry can show an empty preview', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Upstream unavailable' },
      })
      .mockResolvedValueOnce({
        data: { success: true, data: { add_models: [], remove_models: [] } },
      })
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    expect(await screen.findByText('Upstream unavailable')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByText(
        'No processable upstream model updates for this channel'
      )
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Review selected changes' })
    ).toBeDisabled()
    expect(post).toHaveBeenCalledTimes(2)
  })

  test('long labels can wrap without a fixed tab-list height', async () => {
    vi.spyOn(api, 'post').mockResolvedValue(detectionResponse)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    expect(await screen.findByRole('tablist')).toHaveClass(
      'group-data-horizontal/tabs:h-auto'
    )
    for (const tab of screen.getAllByRole('tab')) {
      expect(tab).toHaveClass('h-auto', 'whitespace-normal')
    }
    expect(
      screen.getByRole('button', { name: 'Review selected changes' })
    ).toHaveClass('whitespace-normal')
  })

  test('late detection responses cannot replace another channel preview', async () => {
    let resolve!: (value: typeof detectionResponse) => void
    const promise = new Promise<typeof detectionResponse>((done) => {
      resolve = done
    })
    vi.spyOn(api, 'post').mockReturnValue(promise)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Other channel' }))
    await act(async () => resolve(detectionResponse))
    expect(screen.getByText('#43 other channel')).toBeVisible()
    expect(
      screen.getByRole('checkbox', { name: 'other-old' })
    ).not.toBeChecked()
    expect(screen.queryByText('gpt-new')).not.toBeInTheDocument()
  })

  test('pending apply cannot be dismissed or submitted twice and results use the server response even if refresh fails', async () => {
    let resolve!: (value: typeof applyResponse) => void
    const promise = new Promise<typeof applyResponse>((done) => {
      resolve = done
    })
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce(detectionResponse)
      .mockReturnValueOnce(promise)
    const refresh = vi.fn().mockRejectedValue(new Error('List refresh failed'))
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness refresh={refresh} />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    await user.click(
      await screen.findByRole('button', { name: 'Review selected changes' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Apply selected changes' })
    )
    const confirmation = screen.getByRole('alertdialog')
    expect(
      within(confirmation).getByRole('button', { name: 'Applying...' })
    ).toBeDisabled()
    expect(
      within(confirmation).getByRole('button', { name: 'Cancel' })
    ).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('alertdialog')).toBeVisible()
    await act(async () => resolve(applyResponse))
    const results = await screen.findByRole('dialog', {
      name: 'Update results',
    })
    const added = within(results).getByRole('region', { name: 'Added models' })
    expect(within(added).getByText('gpt-new')).toBeVisible()
    expect(within(added).queryByText('gpt-extra')).not.toBeInTheDocument()
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledTimes(2)
  })

  test('failed apply keeps its error visible and requires a fresh preview before resubmission', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValueOnce(detectionResponse)
      .mockRejectedValueOnce(new Error('Connection lost'))
      .mockResolvedValueOnce(detectionResponse)
    const user = userEvent.setup()
    render(<UpstreamUpdateHarness />)
    await user.click(screen.getByRole('button', { name: 'Detect' }))
    await user.click(
      await screen.findByRole('button', { name: 'Review selected changes' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Apply selected changes' })
    )
    expect(await screen.findByText('Connection lost')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Review selected changes' })
    ).toBeDisabled()
    expect(
      screen.queryByRole('dialog', { name: 'Update results' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Refresh preview' }))
    expect(
      await screen.findByRole('checkbox', { name: 'gpt-new' })
    ).toBeChecked()
    expect(
      screen.getByRole('button', { name: 'Review selected changes' })
    ).toBeEnabled()
    expect(post).toHaveBeenCalledTimes(3)
  })
})
