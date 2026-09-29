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
import {
  Plus,
  MoreHorizontal,
  Settings2,
  Trash2,
  Tags,
  TestTube,
  DollarSign,
  ListChecks,
  SortAsc,
  RefreshCw,
  ArrowUpFromLine,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuCheckboxItem,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  handleDeleteAllDisabled,
  handleFixAbilities,
  handleTestAllChannels,
  handleUpdateAllBalances,
} from '../lib'
import { useChannels } from './channels-provider'

export function ChannelsPrimaryButtons() {
  const { t } = useTranslation()
  const {
    setOpen,
    setCurrentRow,
    enableTagMode,
    setEnableTagMode,
    idSort,
    setIdSort,
    batchMode,
    setBatchMode,
    upstream,
  } = useChannels()
  const queryClient = useQueryClient()
  const [showDeleteDialog, setShowDeleteDialog] = useState(false)
  const [showConsistencyDialog, setShowConsistencyDialog] = useState(false)
  const [isRepairingConsistency, setIsRepairingConsistency] = useState(false)
  const currentUser = useAuthStore((s) => s.auth.user)
  const canEditSensitive = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )

  const handleTagModeToggle = (checked: boolean) => {
    localStorage.setItem('enable-tag-mode', String(checked))
    setEnableTagMode(checked)
  }

  const handleIdSortToggle = (checked: boolean) => {
    localStorage.setItem('channels-id-sort', String(checked))
    setIdSort(checked)
  }

  const handleBatchModeToggle = (checked: boolean) => {
    setBatchMode(checked)
  }

  return (
    <>
      <div className='flex items-center gap-2'>
        {/* Desktop: Toggle switches visible */}
        <div className='hidden items-center gap-2 rounded-md border px-3 py-1.5 sm:flex'>
          <ListChecks className='text-muted-foreground h-4 w-4' />
          <Label
            htmlFor='channel-batch-mode'
            className='cursor-pointer text-sm'
          >
            {t('Batch Operations')}
          </Label>
          <Switch
            id='channel-batch-mode'
            checked={batchMode}
            onCheckedChange={handleBatchModeToggle}
          />
        </div>

        <div className='hidden items-center gap-2 rounded-md border px-3 py-1.5 sm:flex'>
          <Tags className='text-muted-foreground h-4 w-4' />
          <Label htmlFor='tag-mode' className='cursor-pointer text-sm'>
            {t('Tag Mode')}
          </Label>
          <Switch
            id='tag-mode'
            checked={enableTagMode}
            onCheckedChange={handleTagModeToggle}
          />
        </div>

        <div className='hidden items-center gap-2 rounded-md border px-3 py-1.5 sm:flex'>
          <SortAsc className='text-muted-foreground h-4 w-4' />
          <Label htmlFor='id-sort' className='cursor-pointer text-sm'>
            {t('Sort by ID')}
          </Label>
          <Switch
            id='id-sort'
            checked={idSort}
            onCheckedChange={handleIdSortToggle}
          />
        </div>

        {/* Create Channel */}
        <Tooltip>
          <TooltipTrigger render={<span className='inline-flex' />}>
            <Button
              onClick={() => {
                if (!canEditSensitive) return
                setCurrentRow(null)
                setOpen('create-channel')
              }}
              size='sm'
              disabled={!canEditSensitive}
            >
              <Plus className='h-4 w-4' />
              <span className='max-sm:hidden'>{t('Create Channel')}</span>
              <span className='sm:hidden'>{t('Create')}</span>
            </Button>
          </TooltipTrigger>
          {!canEditSensitive && (
            <TooltipContent>
              {t('No permission to perform this action')}
            </TooltipContent>
          )}
        </Tooltip>

        {/* More Actions */}
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant='outline' size='sm' />}>
            <MoreHorizontal className='h-4 w-4' />
          </DropdownMenuTrigger>
          <DropdownMenuContent align='end' className='w-56'>
            {/* Mobile-only: toggle switches */}
            <DropdownMenuCheckboxItem
              className='sm:hidden'
              checked={batchMode}
              onCheckedChange={handleBatchModeToggle}
            >
              <ListChecks className='mr-2 h-4 w-4' />
              {t('Batch Operations')}
            </DropdownMenuCheckboxItem>

            <DropdownMenuCheckboxItem
              className='sm:hidden'
              checked={enableTagMode}
              onCheckedChange={handleTagModeToggle}
            >
              <Tags className='mr-2 h-4 w-4' />
              {t('Tag Mode')}
            </DropdownMenuCheckboxItem>

            <DropdownMenuCheckboxItem
              className='sm:hidden'
              checked={idSort}
              onCheckedChange={handleIdSortToggle}
            >
              <SortAsc className='mr-2 h-4 w-4' />
              {t('Sort by ID')}
            </DropdownMenuCheckboxItem>

            <DropdownMenuSeparator className='sm:hidden' />

            <DropdownMenuItem
              onClick={() => {
                handleTestAllChannels(queryClient)
              }}
            >
              {t('Test All Channels')}
              <DropdownMenuShortcut>
                <TestTube className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>

            <DropdownMenuItem
              onClick={() => {
                handleUpdateAllBalances(queryClient)
              }}
            >
              {t('Update All Balances')}
              <DropdownMenuShortcut>
                <DollarSign className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>

            <DropdownMenuSeparator />

            <DropdownMenuItem
              onClick={() => upstream.detectAllUpdates()}
              disabled={upstream.detectAllLoading}
            >
              {t('Detect All Upstream Updates')}
              <DropdownMenuShortcut>
                <RefreshCw className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>

            <DropdownMenuItem
              onClick={() => upstream.applyAllUpdates()}
              disabled={upstream.applyAllLoading}
            >
              {t('Apply All Upstream Updates')}
              <DropdownMenuShortcut>
                <ArrowUpFromLine className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>

            <DropdownMenuSeparator />

            <DropdownMenuItem
              onSelect={(e) => {
                e.preventDefault()
                setShowConsistencyDialog(true)
              }}
            >
              {t('Repair Channel Consistency')}
              <DropdownMenuShortcut>
                <Settings2 className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>

            <DropdownMenuSeparator />

            <DropdownMenuItem
              onSelect={(e) => {
                e.preventDefault()
                if (!canEditSensitive) return
                setShowDeleteDialog(true)
              }}
              disabled={!canEditSensitive}
              className='text-destructive focus:text-destructive'
            >
              {t('Delete All Disabled')}
              <DropdownMenuShortcut>
                <Trash2 className='h-4 w-4' />
              </DropdownMenuShortcut>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <ConfirmDialog
        open={showDeleteDialog}
        onOpenChange={setShowDeleteDialog}
        title={t('Delete All Disabled Channels?')}
        desc={t(
          'This will permanently delete all manually and automatically disabled channels. This action cannot be undone.'
        )}
        destructive
        handleConfirm={() => {
          if (!canEditSensitive) return
          handleDeleteAllDisabled(queryClient, (_count) => {
            // eslint-disable-next-line no-console
            console.log(`Deleted ${_count} channels`)
          })
          setShowDeleteDialog(false)
        }}
      />

      <ConfirmDialog
        open={showConsistencyDialog}
        onOpenChange={setShowConsistencyDialog}
        title={t('Repair channel consistency?')}
        desc={t(
          'This will rebuild the channel routing index from every channel configuration, including supported models, groups, priorities, and weights. Routing may be briefly incomplete while the rebuild is running. Continue?'
        )}
        confirmText={t('Repair')}
        isLoading={isRepairingConsistency}
        handleConfirm={async () => {
          setIsRepairingConsistency(true)
          try {
            await handleFixAbilities(queryClient, (_result) => {
              // eslint-disable-next-line no-console
              console.log('Repair channel consistency result:', _result)
            })
            setShowConsistencyDialog(false)
          } finally {
            setIsRepairingConsistency(false)
          }
        }}
      />
    </>
  )
}
