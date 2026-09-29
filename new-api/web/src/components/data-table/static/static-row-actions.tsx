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
import { Pencil, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'

import { DataTableRowActionMenu } from '../core/row-action-menu'

type StaticRowActionsProps = {
  editLabel: string
  deleteLabel: string
  menuLabel: string
  onEdit: () => void
  onDelete: () => void
  editDisabled?: boolean
  deleteDisabled?: boolean
}

export function StaticRowActions(props: StaticRowActionsProps) {
  return (
    <div className='flex justify-end gap-1'>
      <Button
        variant='ghost'
        size='icon-sm'
        onClick={props.onEdit}
        disabled={props.editDisabled}
        aria-label={props.editLabel}
      >
        <Pencil />
      </Button>
      <DataTableRowActionMenu ariaLabel={props.menuLabel}>
        <DropdownMenuItem
          onClick={props.onDelete}
          disabled={props.deleteDisabled}
          className='text-destructive focus:text-destructive'
        >
          {props.deleteLabel}
          <DropdownMenuShortcut>
            <Trash2 size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
      </DataTableRowActionMenu>
    </div>
  )
}
