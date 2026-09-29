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
import { Send } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TELEGRAM_BIND_RESULT_MESSAGE } from '@/features/auth/constants'
import { startTelegramBind } from '@/features/profile/api'
import {
  createServerError,
  getServerErrorMessageKey,
} from '@/lib/server-error-message'

// ============================================================================
// Telegram Bind Dialog Component
// ============================================================================

interface TelegramBindDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  botName: string
  onSuccess: () => void
}

export function TelegramBindDialog({
  open,
  onOpenChange,
  botName,
  onSuccess,
}: TelegramBindDialogProps) {
  const { t } = useTranslation()
  const widgetRef = useRef<HTMLDivElement>(null)
  const [callbackUrl, setCallbackUrl] = useState<string | null>(null)
  const [flowToken, setFlowToken] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const createBindFlow = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await startTelegramBind()
      if (!response.success || !response.data?.callback_url) {
        throw createServerError(response, t('Failed to start Telegram binding'))
      }
      setFlowToken(response.data.flow_token)
      setCallbackUrl(
        new URL(response.data.callback_url, window.location.origin).toString()
      )
    } catch (bindError: unknown) {
      setError(
        bindError instanceof Error
          ? bindError.message
          : t('Failed to start Telegram binding')
      )
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    if (!open) {
      setCallbackUrl(null)
      setFlowToken(null)
      setError(null)
      return
    }
    void createBindFlow()
  }, [createBindFlow, open])

  useEffect(() => {
    if (!open || !flowToken) return

    const handleBindResult = (event: MessageEvent<unknown>) => {
      if (event.origin !== window.location.origin) return
      const result = event.data as {
        type?: string
        flow_token?: string
        success?: boolean
        code?: string
      } | null
      if (
        !result ||
        result.type !== TELEGRAM_BIND_RESULT_MESSAGE ||
        result.flow_token !== flowToken
      ) {
        return
      }
      if (!result.success) {
        const messageKey = getServerErrorMessageKey({ code: result.code })
        setError(t(messageKey || 'Telegram binding failed. Please try again.'))
        return
      }
      toast.success(t('Binding successful!'))
      onSuccess()
      onOpenChange(false)
    }

    window.addEventListener('message', handleBindResult)
    return () => window.removeEventListener('message', handleBindResult)
  }, [flowToken, onOpenChange, onSuccess, open, t])

  useEffect(() => {
    const container = widgetRef.current
    if (!container || !callbackUrl) return

    container.replaceChildren()
    const script = document.createElement('script')
    script.async = true
    script.src = 'https://telegram.org/js/telegram-widget.js?22'
    script.setAttribute('data-telegram-login', botName.replace(/^@/, ''))
    script.setAttribute('data-size', 'large')
    script.setAttribute('data-auth-url', callbackUrl)
    script.setAttribute('data-request-access', 'write')
    container.appendChild(script)

    return () => container.replaceChildren()
  }, [botName, callbackUrl])

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Bind Telegram Account')}
      description={t('Click the button below to bind your Telegram account')}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <div className='space-y-4 py-4'>
        <Alert>
          <Send className='h-4 w-4' />
          <AlertDescription>
            {t(
              'You will be redirected to Telegram to complete the binding process.'
            )}
          </AlertDescription>
        </Alert>

        <div className='flex flex-col items-center justify-center gap-4 rounded-lg border p-6'>
          <div className='flex h-12 w-12 items-center justify-center rounded-xl bg-blue-100 dark:bg-blue-900'>
            <Send className='h-6 w-6 text-blue-600 dark:text-blue-400' />
          </div>

          <div className='text-center'>
            <p className='text-muted-foreground text-sm'>
              {t('Bot:')}{' '}
              <span className='font-mono font-semibold'>@{botName}</span>
            </p>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                "After clicking the button, you'll be asked to authorize the bot"
              )}
            </p>
          </div>

          {loading && <Skeleton className='h-10 w-52' />}
          {error && (
            <div className='flex flex-col items-center gap-3 text-center'>
              <p className='text-destructive text-sm'>{error}</p>
              <Button type='button' variant='outline' onClick={createBindFlow}>
                {t('Retry')}
              </Button>
            </div>
          )}
          <div ref={widgetRef} className='flex min-h-10 justify-center' />
        </div>

        <p className='text-muted-foreground text-center text-xs'>
          {t('The binding will complete automatically after authorization')}
        </p>
      </div>
    </Dialog>
  )
}
