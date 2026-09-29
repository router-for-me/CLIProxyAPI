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
import { useCallback, useEffect, useState, useMemo } from 'react'

// Simple debounce implementation (no external dependencies)
function debounce(
  fn: (value: string[]) => void,
  delay: number
): ((value: string[]) => void) & { cancel: () => void } {
  let timeoutId: ReturnType<typeof setTimeout> | null = null

  const debounced = ((value: string[]) => {
    if (timeoutId) clearTimeout(timeoutId)
    timeoutId = setTimeout(() => fn(value), delay)
  }) as ((value: string[]) => void) & { cancel: () => void }

  debounced.cancel = () => {
    if (timeoutId) clearTimeout(timeoutId)
  }

  return debounced
}

/**
 * Hook for managing accordion state persistence in localStorage
 * Supports multiple accordion items being open simultaneously
 * Uses debounced writes to reduce I/O operations
 */
export function useAccordionState(pageId: string) {
  const [openItems, setOpenItems] = useState<string[]>([])
  const storageKey = `system-settings-${pageId}-accordion`

  // Initialize state from localStorage (immediate)
  useEffect(() => {
    try {
      const stored = localStorage.getItem(storageKey)
      if (stored) {
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setOpenItems(JSON.parse(stored))
      }
    } catch (_error) {
      // eslint-disable-next-line no-console
      console.error(
        `Failed to load accordion state from localStorage: ${storageKey}`,
        _error
      )
    }
  }, [storageKey])

  // Debounced save function (500ms delay)
  const debouncedSave = useMemo(
    () =>
      debounce((value: string[]) => {
        try {
          localStorage.setItem(storageKey, JSON.stringify(value))
        } catch (_error) {
          // eslint-disable-next-line no-console
          console.error(
            `Failed to save accordion state to localStorage: ${storageKey}`,
            _error
          )
        }
      }, 500),
    [storageKey]
  )

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      debouncedSave.cancel()
    }
  }, [debouncedSave])

  // Handle accordion value changes (supports multiple open items)
  const handleAccordionChange = useCallback(
    (value: string[]) => {
      setOpenItems(value)
      debouncedSave(value)
    },
    [debouncedSave]
  )

  return {
    openItems,
    handleAccordionChange,
  }
}
