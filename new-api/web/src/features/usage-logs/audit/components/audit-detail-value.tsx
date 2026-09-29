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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { cn } from '@/lib/utils'

export function AuditDetailValue(props: {
  label: string
  value: string
  mono?: boolean
  copyable?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className='flex min-w-0 items-start gap-1 text-xs'>
      <span
        className={cn(
          'min-w-0 flex-1 leading-5 wrap-anywhere break-normal whitespace-pre-wrap',
          props.mono && 'font-mono'
        )}
      >
        {props.value}
      </span>
      {(props.copyable || props.value.length > 48) && (
        <CopyButton
          value={props.value}
          className='size-5'
          iconClassName='size-3'
          aria-label={t('Copy {{field}}', { field: props.label })}
        />
      )}
    </div>
  )
}
