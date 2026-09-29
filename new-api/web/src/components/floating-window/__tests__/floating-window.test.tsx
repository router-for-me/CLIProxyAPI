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
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { FloatingWindow } from '@/components/floating-window'

afterEach(() => {
  window.localStorage.clear()
})

function renderWindow(props?: { storageKey?: string; onClose?: () => void }) {
  return render(
    <FloatingWindow
      title='Scratch pad'
      defaultPosition={{ x: 100, y: 20 }}
      defaultWidth={300}
      storageKey={props?.storageKey}
      onClose={props?.onClose ?? vi.fn()}
      footer={<span>footer</span>}
    >
      <p>body</p>
    </FloatingWindow>
  )
}

test('renders fixed at the default position and follows a header drag, remembering where it was dropped', () => {
  renderWindow({ storageKey: 'scratch' })
  const window_ = screen.getByRole('complementary', { name: 'Scratch pad' })
  expect(window_).toHaveStyle({
    position: 'fixed',
    left: '100px',
    top: '20px',
    width: '300px',
  })
  expect(screen.getByText('body')).toBeVisible()
  expect(screen.getByText('footer')).toBeVisible()

  const handle = screen.getByTitle('Drag to move')
  fireEvent.pointerDown(handle, {
    button: 0,
    pointerId: 1,
    clientX: 120,
    clientY: 40,
  })
  fireEvent.pointerMove(handle, { pointerId: 1, clientX: 220, clientY: 90 })
  fireEvent.pointerUp(handle, { pointerId: 1, clientX: 220, clientY: 90 })
  expect(window_).toHaveStyle({ left: '200px', top: '70px' })
  expect(
    JSON.parse(window.localStorage.getItem('floating-window:scratch') ?? '{}')
  ).toEqual({ x: 200, y: 70, width: 300, collapsed: false })
})

function resizeHandle(direction: string) {
  const handle = document.querySelector<HTMLElement>(
    `[data-slot="floating-window-resize"][data-direction="${direction}"]`
  )
  if (!handle) throw new Error(`missing ${direction} resize handle`)
  return handle
}

test('the visible resize hint leads to a bottom-right grip that grows both dimensions and persists them', () => {
  renderWindow({ storageKey: 'scratch' })
  const window_ = screen.getByRole('complementary', { name: 'Scratch pad' })
  const handle = resizeHandle('se')
  expect(screen.getByText('Drag to resize')).toBeVisible()
  expect(handle.querySelector('svg')).toBeVisible()
  expect(handle).toHaveClass('size-8', 'cursor-nwse-resize')
  fireEvent.pointerDown(handle, {
    button: 0,
    pointerId: 3,
    clientX: 400,
    clientY: 300,
  })
  fireEvent.pointerMove(handle, { pointerId: 3, clientX: 450, clientY: 380 })
  fireEvent.pointerUp(handle, { pointerId: 3, clientX: 450, clientY: 380 })
  // jsdom renders no height, so the vertical start is the minimum height.
  expect(window_).toHaveStyle({ width: '350px', height: '280px' })
  expect(
    JSON.parse(window.localStorage.getItem('floating-window:scratch') ?? '{}')
  ).toEqual({ x: 100, y: 20, width: 350, height: 280, collapsed: false })
})

test('dragging the left edge keeps the right edge in place and never shrinks below the minimum width', () => {
  renderWindow()
  const window_ = screen.getByRole('complementary', { name: 'Scratch pad' })
  const handle = resizeHandle('w')
  fireEvent.pointerDown(handle, {
    button: 0,
    pointerId: 4,
    clientX: 100,
    clientY: 200,
  })
  fireEvent.pointerMove(handle, { pointerId: 4, clientX: 60, clientY: 200 })
  expect(window_).toHaveStyle({ left: '60px', width: '340px' })

  fireEvent.pointerMove(handle, { pointerId: 4, clientX: 300, clientY: 200 })
  fireEvent.pointerUp(handle, { pointerId: 4, clientX: 300, clientY: 200 })
  expect(window_).toHaveStyle({ left: '120px', width: '280px' })
})

test('a stored size is restored and clamped to the viewport', () => {
  window.localStorage.setItem(
    'floating-window:scratch',
    JSON.stringify({ x: 10, y: 10, width: 5000, height: 120 })
  )
  renderWindow({ storageKey: 'scratch' })
  expect(
    screen.getByRole('complementary', { name: 'Scratch pad' })
  ).toHaveStyle({ width: `${window.innerWidth - 16}px`, height: '200px' })
})

test('a stored position outside the viewport is clamped back inside on mount', () => {
  window.localStorage.setItem(
    'floating-window:scratch',
    JSON.stringify({ x: 5000, y: -50 })
  )
  renderWindow({ storageKey: 'scratch' })
  expect(
    screen.getByRole('complementary', { name: 'Scratch pad' })
  ).toHaveStyle({ left: `${window.innerWidth - 300 - 8}px`, top: '8px' })
})

test('collapses to a pill that keeps the title and reopens, and the close control reports to the caller', async () => {
  const user = userEvent.setup()
  const onClose = vi.fn()
  renderWindow({ onClose })
  await user.click(screen.getByRole('button', { name: 'Collapse panel' }))
  expect(
    screen.queryByRole('complementary', { name: 'Scratch pad' })
  ).not.toBeInTheDocument()
  expect(screen.getByText('Scratch pad')).toBeVisible()
  expect(screen.queryByText('body')).not.toBeInTheDocument()
  expect(screen.queryByText('Drag to resize')).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Expand panel' }))
  expect(
    screen.getByRole('complementary', { name: 'Scratch pad' })
  ).toBeVisible()
  expect(screen.getByText('Drag to resize')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Close panel' }))
  expect(onClose).toHaveBeenCalledTimes(1)
})

test('pressing a header button does not start a drag', () => {
  const onClose = vi.fn()
  renderWindow({ onClose })
  const close = screen.getByRole('button', { name: 'Close panel' })
  fireEvent.pointerDown(close, {
    button: 0,
    pointerId: 2,
    clientX: 380,
    clientY: 30,
  })
  fireEvent.pointerMove(screen.getByTitle('Drag to move'), {
    pointerId: 2,
    clientX: 100,
    clientY: 300,
  })
  expect(
    screen.getByRole('complementary', { name: 'Scratch pad' })
  ).toHaveStyle({ left: '100px', top: '20px' })
})
