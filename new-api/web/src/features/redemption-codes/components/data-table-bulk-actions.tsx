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
import { useMutation } from '@tanstack/react-query'
import type { Table } from '@tanstack/react-table'
import { Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { DataTableBulkActions as BulkActionsToolbar } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { batchDeleteRedemptions } from '../api'
import type { Redemption } from '../types'
import { useRedemptions } from './redemptions-provider'

type DataTableBulkActionsProps = {
  table: Table<Redemption>
}

export function DataTableBulkActions(props: DataTableBulkActionsProps) {
  const { t } = useTranslation()
  const { triggerRefresh } = useRedemptions()
  const [deleteTargets, setDeleteTargets] = useState<Redemption[] | null>(null)
  const selectedRows = props.table.getFilteredSelectedRowModel().rows

  const contentToCopy = useMemo(() => {
    const selectedCodes = selectedRows.map((row) => {
      const redemption = row.original
      return `${redemption.name}\t${redemption.key}`
    })
    return selectedCodes.join('\n')
  }, [selectedRows])

  const deletion = useMutation({
    mutationFn: async (targets: Redemption[]) => {
      const result = await batchDeleteRedemptions(
        targets.map((code) => code.id)
      )
      if (!result.success) throw createServerError(result)
      return result.data ?? 0
    },
    onSuccess: (count, targets) => {
      toast.success(
        t('Successfully deleted {{count}} redemption codes', { count })
      )
      props.table.setRowSelection((previous) => {
        const next = { ...previous }
        for (const code of targets) delete next[String(code.id)]
        return next
      })
      setDeleteTargets(null)
      triggerRefresh()
    },
    onError: (_error, targets) => {
      handleServerError(
        _error,
        t('Failed to delete {{count}} redemption codes', {
          count: targets.length,
        })
      )
    },
  })

  return (
    <>
      <BulkActionsToolbar table={props.table} entityName={t('redemption code')}>
        <CopyButton
          value={contentToCopy}
          variant='outline'
          size='icon'
          className='size-8'
          tooltip={t('Copy selected codes')}
          successTooltip={t('Codes copied!')}
          aria-label={t('Copy selected codes')}
        />
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='destructive'
                size='icon'
                className='size-8'
                aria-label={t('Delete selected redemption codes')}
                disabled={deletion.isPending}
                onClick={() =>
                  setDeleteTargets(selectedRows.map((row) => row.original))
                }
              />
            }
          >
            <Trash2 aria-hidden='true' />
          </TooltipTrigger>
          <TooltipContent>
            {t('Delete selected redemption codes')}
          </TooltipContent>
        </Tooltip>
      </BulkActionsToolbar>
      <ConfirmDialog
        destructive
        open={deleteTargets !== null}
        onOpenChange={(open) => {
          if (!open && !deletion.isPending) setDeleteTargets(null)
        }}
        title={t('Delete {{count}} redemption codes?', {
          count: deleteTargets?.length ?? 0,
        })}
        desc={t('This action cannot be undone.')}
        confirmText={deletion.isPending ? t('Deleting...') : t('Delete')}
        isLoading={deletion.isPending}
        disabled={!deleteTargets?.length}
        handleConfirm={() => {
          if (deleteTargets?.length && !deletion.isPending) {
            deletion.mutate(deleteTargets)
          }
        }}
      />
    </>
  )
}
