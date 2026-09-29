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
import { useState, useCallback, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { copyToClipboard as copyToClipboardUtil } from '@/lib/copy-to-clipboard'

type UseCopyToClipboardOptions = {
  /** Whether to show a global toast notification (default: true) */
  notify?: boolean
  /** Success message (default: localized 'Copied to clipboard') */
  successMessage?: string
  /** Error message (default: localized 'Failed to copy to clipboard') */
  errorMessage?: string
  /** Time to automatically reset copiedText (milliseconds, default: 2000) */
  resetAfterMs?: number
}

export function useCopyToClipboard(options?: UseCopyToClipboardOptions) {
  const {
    notify = true,
    successMessage,
    errorMessage,
    resetAfterMs = 2000,
  } = options || {}
  const { t } = useTranslation()

  const [copiedText, setCopiedText] = useState<string | null>(null)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined
  )

  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current)
      }
    }
  }, [])

  const copyToClipboard = useCallback(
    async (text: string): Promise<boolean> => {
      const resolvedSuccessMessage = successMessage ?? t('Copied to clipboard')
      const resolvedErrorMessage =
        errorMessage ?? t('Failed to copy to clipboard')
      const success = await copyToClipboardUtil(text)

      if (success) {
        setCopiedText(text)
        if (notify) {
          toast.success(resolvedSuccessMessage)
        }

        // Clear previous timeout
        if (timeoutRef.current) {
          clearTimeout(timeoutRef.current)
        }

        // Auto-reset after 2 seconds
        timeoutRef.current = setTimeout(() => {
          setCopiedText(null)
        }, resetAfterMs)

        return true
      } else {
        // eslint-disable-next-line no-console
        console.warn('All copy methods failed')
        if (notify) {
          toast.error(resolvedErrorMessage)
        }
        setCopiedText(null)
        return false
      }
    },
    [notify, successMessage, errorMessage, resetAfterMs, t]
  )

  return { copiedText, copyToClipboard }
}
