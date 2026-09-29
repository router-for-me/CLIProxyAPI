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
'use client'

import { ChevronDownIcon, SearchIcon } from 'lucide-react'
import type { ComponentProps } from 'react'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

export type TaskItemFileProps = ComponentProps<'div'>

export const TaskItemFile = ({
  children,
  className,
  ...props
}: TaskItemFileProps) => (
  <div
    className={cn(
      'bg-secondary text-foreground inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs',
      className
    )}
    {...props}
  >
    {children}
  </div>
)

export type TaskItemProps = ComponentProps<'div'>

export const TaskItem = ({ children, className, ...props }: TaskItemProps) => (
  <div className={cn('text-muted-foreground text-sm', className)} {...props}>
    {children}
  </div>
)

export type TaskProps = ComponentProps<typeof Collapsible>

export const Task = ({
  defaultOpen = true,
  className,
  ...props
}: TaskProps) => (
  <Collapsible className={cn(className)} defaultOpen={defaultOpen} {...props} />
)

export type TaskTriggerProps = ComponentProps<typeof CollapsibleTrigger> & {
  title: string
}

export const TaskTrigger = ({
  children,
  className,
  title,
  ...props
}: TaskTriggerProps) => (
  <CollapsibleTrigger
    className={cn('group', className)}
    {...props}
    render={
      <div className='text-muted-foreground hover:text-foreground flex w-full cursor-pointer items-center gap-2 text-sm transition-colors'>
        {children ?? (
          <>
            <SearchIcon className='size-4' />
            <p className='text-sm'>{title}</p>
            <ChevronDownIcon className='size-4 transition-transform group-data-[panel-open]:rotate-180' />
          </>
        )}
      </div>
    }
  />
)

export type TaskContentProps = ComponentProps<typeof CollapsibleContent>

export const TaskContent = ({
  children,
  className,
  ...props
}: TaskContentProps) => (
  <CollapsibleContent
    className={cn(
      'data-closed:fade-out-0 data-closed:slide-out-to-top-2 data-open:slide-in-from-top-2 text-popover-foreground data-closed:animate-out data-open:animate-in outline-none',
      className
    )}
    {...props}
  >
    <div className='border-muted mt-4 space-y-2 border-l-2 pl-4'>
      {children}
    </div>
  </CollapsibleContent>
)
