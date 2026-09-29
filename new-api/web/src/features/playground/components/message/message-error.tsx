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
import { AlertCircle, AlertTriangle, Settings } from 'lucide-react'
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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth-store'

import {
  FALLBACK_ERROR_CONTENT,
  getMessageErrorState,
  isAdminRole,
  MODEL_PRICING_SETTINGS_PATH,
} from '../../lib'
import type { Message } from '../../types'

interface MessageErrorProps {
  message: Message
  className?: string
  actions?: ReactNode
}

/**
 * Display error messages using Alert component
 * Following ai-elements pattern for error handling
 */
export function MessageError({
  message,
  className = '',
  actions,
}: MessageErrorProps) {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const errorState = getMessageErrorState(message, isAdminRole(user?.role))

  if (!errorState) {
    return null
  }

  if (errorState.kind === 'model-price') {
    const content =
      errorState.content === FALLBACK_ERROR_CONTENT
        ? t(FALLBACK_ERROR_CONTENT)
        : errorState.content

    return (
      <Alert variant='default' className={className}>
        <AlertTriangle className='text-orange-500' />
        <AlertTitle>{t('Model Price Not Configured')}</AlertTitle>
        <AlertDescription className='space-y-2'>
          <p>{content}</p>
          {errorState.showSettingsLink && (
            <Button
              variant='outline'
              size='sm'
              onClick={() => window.open(MODEL_PRICING_SETTINGS_PATH, '_blank')}
            >
              <Settings className='mr-1 h-3.5 w-3.5' />
              {t('Go to Settings')}
            </Button>
          )}
          {actions}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Alert variant='destructive' className={className}>
      <AlertCircle />
      <AlertTitle>{t('Error')}</AlertTitle>
      <AlertDescription className='space-y-2'>
        <p>
          {errorState.content === FALLBACK_ERROR_CONTENT
            ? t(FALLBACK_ERROR_CONTENT)
            : errorState.content}
        </p>
        {actions}
      </AlertDescription>
    </Alert>
  )
}
