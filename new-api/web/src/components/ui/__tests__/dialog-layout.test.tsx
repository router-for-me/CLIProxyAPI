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
import { describe, expect, test, vi } from 'vitest'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import { Dialog as DialogRoot, DialogContent, DialogTitle } from '../dialog'

describe('dialog viewport layout', () => {
  test('long content shrinks independently of a stacked action footer', async () => {
    const onConfirm = vi.fn()
    render(
      <Dialog
        open
        title='System updates'
        contentHeight='720px'
        footer={
          <>
            <Button onClick={onConfirm}>Check again</Button>
            <Button>Ignore this version</Button>
            <Button>Go to GitHub</Button>
          </>
        }
      >
        <p>Long release notes</p>
      </Dialog>
    )
    const dialog = screen.getByRole('dialog', { name: 'System updates' })
    // Keep a dynamic-viewport fallback when global CSS variables are missing.
    expect(dialog).toHaveClass(
      'max-h-[var(--dialog-available-height,calc(100dvh-2rem))]'
    )
    expect(dialog).toHaveClass('top-[var(--dialog-viewport-center,50dvh)]')
    // If header and footer alone exceed a short viewport, the popup itself
    // must remain scrollable instead of clipping its actions.
    expect(dialog).toHaveClass('overflow-y-auto', 'overscroll-contain')
    const body =
      screen.getByText('Long release notes').parentElement?.parentElement
    expect(body).toHaveClass('min-h-0', 'overflow-y-auto')
    expect(body).not.toHaveClass('max-h-[calc(100vh-14rem)]')
    const footer = dialog.querySelector('[data-slot=dialog-footer]')
    expect(footer).toHaveClass('flex-shrink-0')
    expect(body?.contains(footer)).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  test('unwrapped dialogs bound and scroll long content within the visible viewport', () => {
    render(
      <DialogRoot open>
        <DialogContent>
          <DialogTitle>Long dialog</DialogTitle>
          <p>Long content</p>
        </DialogContent>
      </DialogRoot>
    )
    expect(screen.getByRole('dialog', { name: 'Long dialog' })).toHaveClass(
      'top-[var(--dialog-viewport-center,50dvh)]',
      'max-h-[var(--dialog-available-height,calc(100dvh-2rem))]',
      'overflow-y-auto',
      'overscroll-contain'
    )
  })

  test('confirmation dialogs scroll overflowing content and keep confirmation usable', async () => {
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title='Confirm changes'
        desc='A long confirmation description'
        handleConfirm={onConfirm}
      />
    )
    expect(
      screen.getByRole('alertdialog', { name: 'Confirm changes' })
    ).toHaveClass(
      'top-[var(--dialog-viewport-center,50dvh)]',
      'max-h-[var(--dialog-available-height,calc(100dvh-2rem))]',
      'overflow-y-auto',
      'overscroll-contain'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })
})
