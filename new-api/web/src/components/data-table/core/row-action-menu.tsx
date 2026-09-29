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
import { MoreHorizontal } from 'lucide-react'
import * as React from 'react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

type DataTableRowActionMenuProps = {
  children: React.ReactNode
  ariaLabel: string
  contentClassName?: string
  modal?: boolean
  onOpenChange?: (open: boolean) => void
}

export function DataTableRowActionMenu(props: DataTableRowActionMenuProps) {
  return (
    <DropdownMenu modal={props.modal} onOpenChange={props.onOpenChange}>
      <DropdownMenuTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            className='data-popup-open:bg-muted'
            aria-label={props.ariaLabel}
          />
        }
      >
        <MoreHorizontal aria-hidden='true' />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align='end'
        className={cn('w-48', props.contentClassName)}
      >
        {props.children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
