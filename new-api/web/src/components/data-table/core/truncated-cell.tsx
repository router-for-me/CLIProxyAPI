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
import * as React from 'react'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

type TruncatedCellProps = {
  children: React.ReactNode
  cellClassName?: string
  className?: string
  contentClassName?: string
  side?: 'top' | 'bottom' | 'left' | 'right'
  tooltipClassName?: string
  tooltipContent?: React.ReactNode
  tabIndex?: number
}

export function TruncatedCell({
  children,
  cellClassName,
  className,
  contentClassName,
  side = 'top',
  tooltipClassName,
  tooltipContent,
  tabIndex,
}: TruncatedCellProps) {
  const content = tooltipContent ?? getTextContent(children)

  if (!content) {
    return (
      <div
        tabIndex={tabIndex}
        className={cn(
          'block max-w-full min-w-0 truncate',
          cellClassName,
          className
        )}
      >
        {children}
      </div>
    )
  }

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <div
            tabIndex={tabIndex}
            className={cn(
              'block max-w-full min-w-0 truncate',
              cellClassName,
              className
            )}
          />
        }
      >
        <div className={cn('truncate', contentClassName)}>{children}</div>
      </TooltipTrigger>
      <TooltipContent
        side={side}
        className={cn('max-w-xs break-all', tooltipClassName)}
      >
        {content}
      </TooltipContent>
    </Tooltip>
  )
}

function getTextContent(node: React.ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(getTextContent).join('')
  return ''
}
