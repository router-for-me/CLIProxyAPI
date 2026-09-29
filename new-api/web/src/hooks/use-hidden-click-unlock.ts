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
import { useCallback, useRef, useState } from 'react'

type HiddenClickUnlockOptions = {
  requiredClicks?: number
  disabled?: boolean
  onUnlock?: () => void
}

type HiddenClickUnlockResult = {
  unlocked: boolean
  handleClick: () => void
  reset: () => void
}

/** Unlock hidden UI after a repeated click gesture. */
export function useHiddenClickUnlock(
  options: HiddenClickUnlockOptions = {}
): HiddenClickUnlockResult {
  const disabled = options.disabled ?? false
  const requiredClicks = options.requiredClicks ?? 3
  const onUnlock = options.onUnlock
  const clickCountRef = useRef(0)
  const [unlocked, setUnlocked] = useState(false)

  const reset = useCallback((): void => {
    clickCountRef.current = 0
    setUnlocked(false)
  }, [])

  const handleClick = useCallback((): void => {
    if (disabled || unlocked) return

    const nextClickCount = clickCountRef.current + 1
    clickCountRef.current = nextClickCount

    if (nextClickCount >= requiredClicks) {
      clickCountRef.current = 0
      setUnlocked(true)
      onUnlock?.()
    }
  }, [disabled, onUnlock, requiredClicks, unlocked])

  return {
    unlocked,
    handleClick,
    reset,
  }
}
