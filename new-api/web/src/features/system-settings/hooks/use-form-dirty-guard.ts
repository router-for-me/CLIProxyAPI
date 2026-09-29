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
import { useBlocker } from '@tanstack/react-router'
import { useEffect } from 'react'

/**
 * @deprecated Use FormNavigationGuard component instead for better UX
 *
 * This hook uses browser's native window.confirm() which has poor UX.
 * The new FormNavigationGuard component uses project's native ConfirmDialog.
 *
 * @see {@link ../components/form-navigation-guard.tsx}
 *
 * Prevents navigation away from a form with unsaved changes
 *
 * Shows a confirmation dialog when user tries to:
 * - Navigate to a different route
 * - Close the browser tab/window
 * - Refresh the page
 *
 * @param isDirty - Whether the form has unsaved changes
 * @param message - Custom warning message (optional)
 *
 * @example
 * ```tsx
 * // ❌ Old way (deprecated)
 * const form = useForm()
 * useFormDirtyGuard(form.formState.isDirty)
 *
 * // ✅ New way (recommended)
 * <FormNavigationGuard when={form.formState.isDirty} />
 * ```
 */
export function useFormDirtyGuard(
  isDirty: boolean,
  message: string = 'You have unsaved changes. Are you sure you want to leave?'
) {
  // Block navigation within the app
  useBlocker({
    condition: isDirty,
    blockerFn: () => window.confirm(message),
  })

  // Block browser navigation (close tab, refresh, etc.)
  useEffect(() => {
    if (!isDirty) return

    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      // Modern browsers ignore custom messages and show their own
      e.returnValue = message
      return message
    }

    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [isDirty, message])
}
