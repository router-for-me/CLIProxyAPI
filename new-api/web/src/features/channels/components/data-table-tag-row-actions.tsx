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
import { useQueryClient } from '@tanstack/react-query'
import type { Row } from '@tanstack/react-table'
import { Power, PowerOff, Pencil, Edit } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { handleEnableTagChannels, handleDisableTagChannels } from '../lib'
import type { Channel } from '../types'
import { useChannels } from './channels-provider'

interface DataTableTagRowActionsProps {
  row: Row<Channel & { tag?: string }>
}

export function DataTableTagRowActions({ row }: DataTableTagRowActionsProps) {
  const { t } = useTranslation()
  const tag = row.original.tag
  const { setOpen, setCurrentTag } = useChannels()
  const queryClient = useQueryClient()

  if (!tag) return null

  const handleEnableAll = () => {
    handleEnableTagChannels(tag, queryClient)
  }

  const handleDisableAll = () => {
    handleDisableTagChannels(tag, queryClient)
  }

  const handleBatchEdit = () => {
    setCurrentTag(tag)
    setOpen('tag-batch-edit')
  }

  const handleEditTag = () => {
    setCurrentTag(tag)
    setOpen('edit-tag')
  }

  return (
    <div className='-ml-1.5 flex items-center gap-1'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={handleEditTag}
              aria-label={t('Edit Tag')}
            />
          }
        >
          <Edit />
        </TooltipTrigger>
        <TooltipContent>{t('Edit Tag')}</TooltipContent>
      </Tooltip>

      <DataTableRowActionMenu ariaLabel={t('Open menu')}>
        {/* Batch Edit */}
        <DropdownMenuItem onClick={handleBatchEdit}>
          {t('Batch Edit')}
          <DropdownMenuShortcut>
            <Pencil size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>

        <DropdownMenuSeparator />

        {/* Enable All */}
        <DropdownMenuItem onClick={handleEnableAll}>
          {t('Enable All')}
          <DropdownMenuShortcut>
            <Power size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>

        {/* Disable All */}
        <DropdownMenuItem onClick={handleDisableAll}>
          {t('Disable All')}
          <DropdownMenuShortcut>
            <PowerOff size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
      </DataTableRowActionMenu>
    </div>
  )
}
