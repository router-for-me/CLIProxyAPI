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
import { renderHook } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'

import { useRedirectPanelPlacement } from '../use-redirect-panel-placement'

function mockSheet(rect: { left: number; right: number }, innerWidth: number) {
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: innerWidth,
  })
  const anchor = document.createElement('div')
  anchor.dataset.slot = 'sheet-content'
  document.body.append(anchor)
  vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue({
    x: rect.left,
    y: 0,
    left: rect.left,
    top: 0,
    right: rect.right,
    bottom: 800,
    width: rect.right - rect.left,
    height: 800,
    toJSON: () => ({}),
  } as DOMRect)
  return { current: anchor }
}

afterEach(() => {
  document.body.innerHTML = ''
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    writable: true,
    value: 1024,
  })
})

test('a left-hand sheet starts the window in the free space on its right', () => {
  const ref = mockSheet({ left: 0, right: 900 }, 1920)
  const { result } = renderHook(() =>
    useRedirectPanelPlacement(ref, true, 'left')
  )
  expect(result.current).toEqual({ x: 916, y: 16, width: 420 })
})

test('a right-hand sheet starts the window on its left, overlapping the sheet when the viewport has no room beside it', () => {
  const roomy = mockSheet({ left: 1000, right: 1920 }, 1920)
  expect(
    renderHook(() => useRedirectPanelPlacement(roomy, true, 'right')).result
      .current
  ).toEqual({ x: 564, y: 16, width: 420 })
  const cramped = mockSheet({ left: 100, right: 1296 }, 1296)
  expect(
    renderHook(() => useRedirectPanelPlacement(cramped, true, 'right')).result
      .current
  ).toEqual({ x: 16, y: 16, width: 420 })
})

test('there is no placement on narrow viewports or while disabled', () => {
  const narrow = mockSheet({ left: 0, right: 700 }, 1200)
  expect(
    renderHook(() => useRedirectPanelPlacement(narrow, true, 'left')).result
      .current
  ).toBe(null)
  const disabled = mockSheet({ left: 0, right: 900 }, 1920)
  expect(
    renderHook(() => useRedirectPanelPlacement(disabled, false, 'left')).result
      .current
  ).toBe(null)
})
