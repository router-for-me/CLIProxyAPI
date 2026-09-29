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
import { Edit, RefreshCw, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { MessageActionButton } from './message-action-button'

type MessageErrorActionsProps = {
  disabled?: boolean
  onDelete?: () => void
  onEditPrompt?: () => void
  onRetry?: () => void
}

export function MessageErrorActions({
  disabled = false,
  onDelete,
  onEditPrompt,
  onRetry,
}: MessageErrorActionsProps) {
  const { t } = useTranslation()

  if (!onRetry && !onEditPrompt && !onDelete) {
    return null
  }

  return (
    <div className='flex flex-wrap items-center gap-0.5 pt-2'>
      {onRetry && (
        <MessageActionButton
          disabled={disabled}
          icon={RefreshCw}
          label={t('Retry')}
          onClick={onRetry}
        />
      )}

      {onEditPrompt && (
        <MessageActionButton
          disabled={disabled}
          icon={Edit}
          label={t('Edit')}
          onClick={onEditPrompt}
        />
      )}

      {onDelete && (
        <MessageActionButton
          disabled={disabled}
          icon={Trash2}
          label={t('Delete')}
          onClick={onDelete}
          variant='destructive'
        />
      )}
    </div>
  )
}
