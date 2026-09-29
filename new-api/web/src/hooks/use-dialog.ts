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
  useState,
  useCallback,
  useMemo,
  type Dispatch,
  type SetStateAction,
} from 'react'

// ============================================================================
// Types
// ============================================================================

export interface DialogHandlers {
  open: () => void
  close: () => void
  toggle: () => void
}

export interface DialogStateHandlers {
  reset: () => void
  isOpen: boolean
}

export interface DialogsHandlers<T extends string> {
  open: (key: T) => void
  close: (key: T) => void
  toggle: (key: T) => void
  isOpen: (key: T) => boolean
  closeAll: () => void
  hasAnyOpen: boolean
}

// ============================================================================
// Dialog State Management Hooks
// ============================================================================

/**
 * Simple hook for managing a single dialog state with boolean value
 * @param initialOpen Initial dialog open state (default: false)
 * @returns Tuple of [isOpen, handlers]
 * @example
 * const [isOpen, handlers] = useDialog()
 * handlers.open()
 * handlers.close()
 * handlers.toggle()
 */
export function useDialog(
  initialOpen = false
): readonly [boolean, DialogHandlers] {
  const [isOpen, setIsOpen] = useState(initialOpen)

  const handlers: DialogHandlers = useMemo(
    () => ({
      open: () => setIsOpen(true),
      close: () => setIsOpen(false),
      toggle: () => setIsOpen((prev) => !prev),
    }),
    []
  )

  return [isOpen, handlers] as const
}

/**
 * Hook for managing dialog state with custom value types
 * Useful for dialogs that need to track which item is being edited/viewed
 * @param initialState Initial dialog state (default: null)
 * @returns Tuple of [state, setState, handlers]
 * @example
 * const [status, setStatus, handlers] = useDialogState<"approve" | "reject">()
 * setStatus('approve')
 * if (handlers.isOpen) {}
 * handlers.reset()
 *
 * // Or with objects:
 * const [user, setUser, handlers] = useDialogState<User>()
 * setUser({ id: 1, name: 'John' })
 */
export function useDialogState<T = unknown>(
  initialState: T | null = null
): readonly [
  T | null,
  Dispatch<SetStateAction<T | null>>,
  DialogStateHandlers,
] {
  const [state, setState] = useState<T | null>(initialState)

  const reset = useCallback(() => setState(null), [])

  const handlers: DialogStateHandlers = useMemo(
    () => ({
      reset,
      isOpen: state !== null,
    }),
    [state, reset]
  )

  return [state, setState, handlers] as const
}

/**
 * Hook for managing multiple independent dialog states
 * Useful when you have multiple dialogs that can be open simultaneously
 * @returns Object with methods to manage multiple dialogs
 * @example
 * const dialogs = useDialogs<'create' | 'edit' | 'delete'>()
 * dialogs.open('create')
 * dialogs.close('edit')
 * dialogs.toggle('delete')
 * dialogs.isOpen('create')
 * dialogs.closeAll()
 */
export function useDialogs<T extends string>(): DialogsHandlers<T> {
  const [openDialogs, setOpenDialogs] = useState<Set<T>>(new Set())

  const open = useCallback((key: T) => {
    setOpenDialogs((prev) => {
      if (prev.has(key)) return prev
      const next = new Set(prev)
      next.add(key)
      return next
    })
  }, [])

  const close = useCallback((key: T) => {
    setOpenDialogs((prev) => {
      if (!prev.has(key)) return prev
      const next = new Set(prev)
      next.delete(key)
      return next
    })
  }, [])

  const toggle = useCallback((key: T) => {
    setOpenDialogs((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }, [])

  const closeAll = useCallback(() => {
    setOpenDialogs((prev) => (prev.size === 0 ? prev : new Set()))
  }, [])

  const hasAnyOpen = useMemo(() => openDialogs.size > 0, [openDialogs])

  return useMemo(
    () => ({
      open,
      close,
      toggle,
      isOpen: (key: T) => openDialogs.has(key),
      closeAll,
      hasAnyOpen,
    }),
    [open, close, toggle, openDialogs, closeAll, hasAnyOpen]
  )
}

/**
 * @deprecated Use named import useDialogState instead
 */
export default useDialogState
