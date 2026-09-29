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
import { type RefObject, useLayoutEffect, useState } from 'react'

export type RedirectPanelPlacement = {
  x: number
  y: number
  width: number
}

/** Below this viewport width the floating panel is not offered at all. */
export const REDIRECT_PANEL_MIN_VIEWPORT = 1280
const GAP = 16
const WIDTH = 420

/**
 * Picks where the redirect window first appears: beside the channel sheet on
 * its free side, or overlapping the sheet's inner edge when the viewport has
 * no room there. The window is draggable afterwards, so this is only a start
 * position. `null` means the viewport is too narrow for a floating window.
 */
export function useRedirectPanelPlacement(
  anchorRef: RefObject<HTMLElement | null>,
  enabled: boolean,
  sheetSide: 'left' | 'right'
): RedirectPanelPlacement | null {
  const [placement, setPlacement] = useState<RedirectPanelPlacement | null>(
    null
  )

  useLayoutEffect(() => {
    if (!enabled || window.innerWidth < REDIRECT_PANEL_MIN_VIEWPORT) {
      setPlacement(null)
      return
    }
    const anchor =
      anchorRef.current?.closest<HTMLElement>('[data-slot="sheet-content"]') ??
      anchorRef.current
    if (!anchor) {
      setPlacement(null)
      return
    }
    const rect = anchor.getBoundingClientRect()
    const beside =
      sheetSide === 'left' ? rect.right + GAP : rect.left - GAP - WIDTH
    const x = Math.min(Math.max(GAP, beside), window.innerWidth - WIDTH - GAP)
    setPlacement({ x, y: rect.top + GAP, width: WIDTH })
  }, [anchorRef, enabled, sheetSide])

  return placement
}
