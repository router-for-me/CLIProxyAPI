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
import type { Table } from '@tanstack/react-table'
import { Eye, EyeOff, Trash2, Copy, Building2, Unlink } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTableBulkActions as BulkActionsToolbar } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import { handleBatchEnableModels, handleBatchDisableModels } from '../lib'
import type { Model } from '../types'
import type { VendorOperation } from '../vendor-api'
import { ModelDeleteDialog } from './dialogs/model-delete-dialog'
import { VendorOperationDialog } from './dialogs/vendor-operation-dialog'

interface DataTableBulkActionsProps<TData> {
  table: Table<TData>
}

export function DataTableBulkActions<TData>({
  table,
}: DataTableBulkActionsProps<TData>) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [vendorOperation, setVendorOperation] =
    useState<VendorOperation | null>(null)
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false)

  const selectedRows = table.getFilteredSelectedRowModel().rows
  const selectedIds = selectedRows.reduce<number[]>((ids, row) => {
    const id = (row.original as Model).id

    if (typeof id === 'number' && id > 0) {
      ids.push(id)
    }

    return ids
  }, [])

  const hasMissingMetadata = selectedRows.some(
    (row) => !(row.original as Model).id
  )

  const selectedModels = selectedRows.map((row) => row.original as Model)

  const handleClearSelection = () => {
    table.resetRowSelection()
  }

  const handleEnableAll = () => {
    handleBatchEnableModels(selectedIds, queryClient, handleClearSelection)
  }

  const handleDisableAll = () => {
    handleBatchDisableModels(selectedIds, queryClient, handleClearSelection)
  }

  const handleCopyNames = async () => {
    const names = selectedModels.map((m) => m.model_name).join(',')
    const success = await copyToClipboard(names)
    if (success) {
      toast.success(t('Model names copied to clipboard'))
    } else {
      toast.error(t('Failed to copy model names'))
    }
  }

  return (
    <>
      {vendorOperation && (
        <VendorOperationDialog
          selection={vendorOperation}
          onClose={() => setVendorOperation(null)}
          onSuccess={handleClearSelection}
        />
      )}
      <BulkActionsToolbar table={table} entityName='model'>
        {hasMissingMetadata && (
          <Tooltip>
            <TooltipTrigger
              render={
                <span
                  tabIndex={0}
                  aria-description={t(
                    'Add metadata to all selected models first.'
                  )}
                  className='text-muted-foreground max-w-28 truncate text-xs'
                />
              }
            >
              {t('Missing metadata')}
            </TooltipTrigger>
            <TooltipContent>
              {t('Add metadata to all selected models first.')}
            </TooltipContent>
          </Tooltip>
        )}
        <Button
          variant='outline'
          size='icon'
          className='size-8'
          disabled={hasMissingMetadata}
          title={t(
            hasMissingMetadata
              ? 'Add metadata to all selected models first.'
              : 'Change vendor'
          )}
          aria-label={t('Change vendor')}
          onClick={() =>
            setVendorOperation({ action: 'assign', model_ids: selectedIds })
          }
        >
          <Building2 />
        </Button>
        <Button
          variant='outline'
          size='icon'
          className='size-8'
          disabled={hasMissingMetadata}
          title={t(
            hasMissingMetadata
              ? 'Add metadata to all selected models first.'
              : 'Clear vendor'
          )}
          aria-label={t('Clear vendor')}
          onClick={() =>
            setVendorOperation({
              action: 'assign',
              model_ids: selectedIds,
              target_vendor_id: 0,
            })
          }
        >
          <Unlink />
        </Button>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='outline'
                size='icon'
                disabled={hasMissingMetadata}
                onClick={handleEnableAll}
                className='size-8'
                aria-label={t('Show selected models in model square')}
                title={t('Show selected models in model square')}
              />
            }
          >
            <Eye />
            <span className='sr-only'>
              {t('Show selected models in model square')}
            </span>
          </TooltipTrigger>
          <TooltipContent>
            <p>
              {t(
                hasMissingMetadata
                  ? 'Add metadata to all selected models first.'
                  : 'Show selected models in model square'
              )}
            </p>
          </TooltipContent>
        </Tooltip>

        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='outline'
                size='icon'
                disabled={hasMissingMetadata}
                onClick={handleDisableAll}
                className='size-8'
                aria-label={t('Hide selected models from model square')}
                title={t('Hide selected models from model square')}
              />
            }
          >
            <EyeOff />
            <span className='sr-only'>
              {t('Hide selected models from model square')}
            </span>
          </TooltipTrigger>
          <TooltipContent>
            <p>
              {t(
                hasMissingMetadata
                  ? 'Add metadata to all selected models first.'
                  : 'Hide selected models from model square'
              )}
            </p>
          </TooltipContent>
        </Tooltip>

        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='outline'
                size='icon'
                onClick={handleCopyNames}
                className='size-8'
                aria-label={t('Copy model names')}
                title={t('Copy model names')}
              />
            }
          >
            <Copy />
            <span className='sr-only'>{t('Copy model names')}</span>
          </TooltipTrigger>
          <TooltipContent>
            <p>{t('Copy model names')}</p>
          </TooltipContent>
        </Tooltip>

        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='destructive'
                size='icon'
                disabled={hasMissingMetadata}
                onClick={() => setShowDeleteConfirm(true)}
                className='size-8'
                aria-label={t('Delete selected models')}
                title={t('Delete selected models')}
              />
            }
          >
            <Trash2 />
            <span className='sr-only'>{t('Delete selected models')}</span>
          </TooltipTrigger>
          <TooltipContent>
            <p>
              {t(
                hasMissingMetadata
                  ? 'Add metadata to all selected models first.'
                  : 'Delete selected models'
              )}
            </p>
          </TooltipContent>
        </Tooltip>
      </BulkActionsToolbar>

      {showDeleteConfirm && (
        <ModelDeleteDialog
          models={selectedModels}
          onClose={() => setShowDeleteConfirm(false)}
          onSuccess={handleClearSelection}
        />
      )}
    </>
  )
}
