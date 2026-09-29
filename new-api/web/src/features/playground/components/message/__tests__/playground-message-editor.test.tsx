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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { beforeAll, describe, expect, test, vi } from 'vitest'

import type { Message } from '../../../types'
import { PlaygroundMessageEditor } from '../playground-message-editor'

const leavePrompt = 'You have unsaved changes. Are you sure you want to leave?'

const userMessage: Message = {
  key: 'msg-1',
  from: 'user',
  versions: [{ id: 'v1', content: 'original' }],
}

function renderEditor(options: {
  editText: string
  onCancelEdit?: (open: boolean) => void
}) {
  return render(
    <PlaygroundMessageEditor
      editText={options.editText}
      message={userMessage}
      onCancelEdit={options.onCancelEdit}
      onEditTextChange={() => undefined}
      originalText='original'
    />
  )
}

describe('PlaygroundMessageEditor leave warning', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      Cancel: 'Cancel',
      Leave: 'Leave',
      Stay: 'Stay',
      [leavePrompt]: leavePrompt,
    })
  })

  test('cancels immediately when the edit has no unsaved changes', async () => {
    const user = userEvent.setup()
    const onCancelEdit = vi.fn()

    renderEditor({ editText: 'original', onCancelEdit })

    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(onCancelEdit).toHaveBeenCalledWith(false)
    expect(screen.queryByText(leavePrompt)).not.toBeInTheDocument()
  })

  test('leaves the editor after confirming unsaved changes', async () => {
    const user = userEvent.setup()
    const onCancelEdit = vi.fn()

    renderEditor({ editText: 'changed', onCancelEdit })

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Leave' }))

    expect(onCancelEdit).toHaveBeenCalledWith(false)
  })

  test('keeps the editor open after staying with unsaved changes', async () => {
    const user = userEvent.setup()
    const onCancelEdit = vi.fn()

    renderEditor({ editText: 'changed', onCancelEdit })

    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Stay' }))

    expect(onCancelEdit).not.toHaveBeenCalled()
    expect(screen.queryByText(leavePrompt)).not.toBeInTheDocument()
  })
})

describe('PlaygroundMessageEditor beforeunload guard', () => {
  test('blocks page unload while the edit has unsaved changes', () => {
    renderEditor({ editText: 'changed' })

    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
  })

  test('stops blocking page unload after the edit reverts to the original text', () => {
    const { rerender } = renderEditor({ editText: 'changed' })

    rerender(
      <PlaygroundMessageEditor
        editText='original'
        message={userMessage}
        onEditTextChange={() => undefined}
        originalText='original'
      />
    )

    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(false)
  })
})
