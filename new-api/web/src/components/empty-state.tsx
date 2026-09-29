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
import { Database, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { FadeIn } from '@/components/page-transition'
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { cn } from '@/lib/utils'

interface EmptyStateProps {
  icon?: LucideIcon
  title?: string
  description?: string
  action?: ReactNode
  className?: string
  bordered?: boolean
}

export function EmptyState(props: EmptyStateProps) {
  const { t } = useTranslation()
  const Icon = props.icon ?? Database

  return (
    <FadeIn>
      <Empty
        className={cn(
          'min-h-[300px]',
          props.bordered && 'border',
          props.className
        )}
      >
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <Icon className='size-6' />
          </EmptyMedia>
          <EmptyTitle>{props.title ?? t('No Data')}</EmptyTitle>
          {props.description != null && (
            <EmptyDescription>{props.description}</EmptyDescription>
          )}
        </EmptyHeader>
        {props.action != null && <EmptyContent>{props.action}</EmptyContent>}
      </Empty>
    </FadeIn>
  )
}
