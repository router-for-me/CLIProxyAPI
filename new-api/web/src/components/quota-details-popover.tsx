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
import { Fragment, type ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

type QuotaDetailsPopoverProps = {
  title: string
  triggerLabel: string
  details: ReadonlyArray<{ label: string; value: string }>
  description?: string
  children: ReactNode
  afterTrigger?: ReactNode
  className?: string
  triggerClassName?: string
}

export function QuotaDetailsPopover(props: QuotaDetailsPopoverProps) {
  return (
    <Popover>
      <div className={cn('w-full min-w-0', props.className)}>
        <PopoverTrigger
          render={
            <Button
              variant='ghost'
              aria-label={props.triggerLabel}
              className={cn(
                'h-auto w-full min-w-0 justify-start px-0 py-0.5 text-left font-normal hover:bg-transparent aria-expanded:bg-transparent',
                props.triggerClassName
              )}
            />
          }
        >
          {props.children}
        </PopoverTrigger>
        {props.afterTrigger}
      </div>
      <PopoverContent
        align='start'
        className='w-72 max-w-[calc(100vw-2rem)] gap-3 p-3'
      >
        <PopoverTitle>{props.title}</PopoverTitle>
        <dl className='grid grid-cols-[1fr_auto] gap-x-4 gap-y-2 text-sm tabular-nums'>
          {props.details.map((detail) => (
            <Fragment key={detail.label}>
              <dt className='text-muted-foreground'>{detail.label}</dt>
              <dd className='text-right break-all'>{detail.value}</dd>
            </Fragment>
          ))}
        </dl>
        {props.description && (
          <p className='text-muted-foreground text-xs leading-relaxed'>
            {props.description}
          </p>
        )}
      </PopoverContent>
    </Popover>
  )
}
