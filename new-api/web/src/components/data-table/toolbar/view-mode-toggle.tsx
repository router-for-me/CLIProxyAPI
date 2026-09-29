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
import { Grid2X2, Table2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import {
  DATA_TABLE_VIEW_MODES,
  type DataTableViewMode,
} from '../hooks/use-data-table-view-mode'

export type DataTableViewModeToggleProps = {
  value: DataTableViewMode
  onChange: (mode: DataTableViewMode) => void
  className?: string
}

type Segment = {
  value: DataTableViewMode
  icon: React.ComponentType<{ className?: string }>
  tooltip: string
}

/**
 * Reusable icon segmented control for switching a data table between table and
 * card views. Shared, accessible version of the local control used by the
 * model square (`pricing-toolbar.tsx`).
 */
export function DataTableViewModeToggle(props: DataTableViewModeToggleProps) {
  const { t } = useTranslation()

  const segments: Segment[] = [
    {
      value: DATA_TABLE_VIEW_MODES.CARD,
      icon: Grid2X2,
      tooltip: t('Card view'),
    },
    {
      value: DATA_TABLE_VIEW_MODES.TABLE,
      icon: Table2,
      tooltip: t('Table view'),
    },
  ]

  return (
    <div
      role='group'
      aria-label={t('View mode')}
      className={cn(
        'bg-muted/60 inline-flex h-8 items-center rounded-lg border p-0.5',
        props.className
      )}
    >
      {segments.map((segment) => {
        const Icon = segment.icon
        const isActive = segment.value === props.value
        return (
          <Tooltip key={segment.value}>
            <TooltipTrigger
              render={
                <Button
                  type='button'
                  variant={isActive ? 'default' : 'ghost'}
                  size='icon-sm'
                  onClick={() => props.onChange(segment.value)}
                  aria-label={segment.tooltip}
                  aria-pressed={isActive}
                  className='h-full w-7'
                >
                  <Icon className='size-3.5' />
                </Button>
              }
            />
            <TooltipContent side='bottom' className='text-xs'>
              {segment.tooltip}
            </TooltipContent>
          </Tooltip>
        )
      })}
    </div>
  )
}
