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
import { ChevronDown } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

type DataTableMobileFilterPanelProps = {
  children: ReactNode
  actions: ReactNode
  compact?: boolean
  className?: string
}

export function DataTableMobileFilterPanel(
  props: DataTableMobileFilterPanelProps
) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className={cn(
        'bg-card/50 flex min-w-0 flex-col rounded-lg border p-2.5',
        open && (props.compact ? 'gap-2.5' : 'gap-2'),
        props.className
      )}
    >
      <CollapsibleContent>{props.children}</CollapsibleContent>
      <div
        className={cn(
          props.compact
            ? 'grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-2'
            : 'flex items-center justify-end gap-1.5'
        )}
      >
        <CollapsibleTrigger
          aria-label={open ? t('Collapse') : t('Expand')}
          render={
            <Button
              variant='ghost'
              size='icon'
              className={cn(
                'text-muted-foreground hover:text-foreground',
                props.compact ? 'size-9' : 'mr-auto size-7'
              )}
            />
          }
        >
          <ChevronDown
            aria-hidden='true'
            className={cn(
              'transition-transform duration-200',
              props.compact ? 'size-4' : 'size-3.5',
              open && 'rotate-180'
            )}
          />
        </CollapsibleTrigger>
        <div
          role='group'
          aria-label={t('Actions')}
          className={cn(
            'flex items-center justify-end gap-1.5',
            props.compact &&
              'min-w-0 flex-wrap [&>button]:h-auto [&>button]:min-h-9 [&>button]:max-w-full [&>button]:[overflow-wrap:anywhere] [&>button]:whitespace-normal'
          )}
        >
          {props.actions}
        </div>
      </div>
    </Collapsible>
  )
}
