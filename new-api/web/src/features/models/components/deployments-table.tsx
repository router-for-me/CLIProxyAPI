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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTablePage, useDataTable } from '@/components/data-table'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { deleteDeployment, listDeployments, searchDeployments } from '../api'
import { getDeploymentStatusOptions } from '../constants'
import { deploymentsQueryKeys } from '../lib'
import type { Deployment } from '../types'
import { useDeploymentsColumns } from './deployments-columns'
import { ExtendDeploymentDialog } from './dialogs/extend-deployment-dialog'
import { RenameDeploymentDialog } from './dialogs/rename-deployment-dialog'
import { UpdateConfigDialog } from './dialogs/update-config-dialog'
import { ViewDetailsDialog } from './dialogs/view-details-dialog'
import { ViewLogsDialog } from './dialogs/view-logs-dialog'

const route = getRouteApi('/_authenticated/models/$section')

export function DeploymentsTable() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isMobile = useMediaQuery('(max-width: 640px)')

  // URL state (use dedicated keys so it won't collide with metadata table)
  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: {
      pageKey: 'dPage',
      pageSizeKey: 'dPageSize',
      defaultPage: 1,
      defaultPageSize: isMobile ? 8 : 10,
    },
    globalFilter: { enabled: true, key: 'dFilter' },
    columnFilters: [
      { columnId: 'status', searchKey: 'dStatus', type: 'array' },
    ],
  })

  const keyword = globalFilter ?? ''
  const statusFilter =
    (columnFilters.find((f) => f.id === 'status')?.value as string[]) || []
  const activeStatus =
    statusFilter.length > 0 && !statusFilter.includes('all')
      ? statusFilter[0]
      : undefined

  // Dialog state
  const [logsOpen, setLogsOpen] = useState(false)
  const [logsDeploymentId, setLogsDeploymentId] = useState<
    string | number | null
  >(null)
  const [detailsOpen, setDetailsOpen] = useState(false)
  const [detailsDeploymentId, setDetailsDeploymentId] = useState<
    string | number | null
  >(null)
  const [updateOpen, setUpdateOpen] = useState(false)
  const [updateDeploymentId, setUpdateDeploymentId] = useState<
    string | number | null
  >(null)
  const [extendOpen, setExtendOpen] = useState(false)
  const [extendDeploymentId, setExtendDeploymentId] = useState<
    string | number | null
  >(null)
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameDeploymentId, setRenameDeploymentId] = useState<
    string | number | null
  >(null)
  const [renameCurrentName, setRenameCurrentName] = useState<string>('')

  // Delete confirm
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Deployment | null>(null)
  const [isDeleting, setIsDeleting] = useState(false)

  const { data, isLoading, isFetching } = useQuery({
    queryKey: deploymentsQueryKeys.list({
      keyword,
      status: activeStatus,
      p: pagination.pageIndex + 1,
      page_size: pagination.pageSize,
    }),
    queryFn: async () => {
      if (keyword.trim()) {
        return requireServerSuccess(
          await searchDeployments({
            keyword,
            status: activeStatus,
            p: pagination.pageIndex + 1,
            page_size: pagination.pageSize,
          })
        )
      }
      return requireServerSuccess(
        await listDeployments({
          status: activeStatus,
          p: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
        })
      )
    },
    placeholderData: (prev) => prev,
  })

  const deployments = data?.data?.items || []
  const totalCount = data?.data?.total || 0

  const handleDelete = async () => {
    if (!deleteTarget) return
    setIsDeleting(true)
    try {
      const res = await deleteDeployment(deleteTarget.id)
      if (res?.success) {
        toast.success(t('Deleted successfully'))
        queryClient.invalidateQueries({
          queryKey: deploymentsQueryKeys.lists(),
        })
      } else {
        handleServerError(res, t('Delete failed'))
      }
    } catch (err) {
      handleServerError(err, t('Delete failed'))
    } finally {
      setIsDeleting(false)
      setDeleteOpen(false)
      setDeleteTarget(null)
    }
  }

  const columns = useDeploymentsColumns({
    onViewLogs: (id) => {
      setLogsDeploymentId(id)
      setLogsOpen(true)
    },
    onViewDetails: (id) => {
      setDetailsDeploymentId(id)
      setDetailsOpen(true)
    },
    onUpdateConfig: (id) => {
      setUpdateDeploymentId(id)
      setUpdateOpen(true)
    },
    onExtend: (id) => {
      setExtendDeploymentId(id)
      setExtendOpen(true)
    },
    onRename: (id, currentName) => {
      setRenameCurrentName(currentName)
      setRenameDeploymentId(id)
      setRenameOpen(true)
    },
    onDelete: (deployment) => {
      setDeleteTarget(deployment)
      setDeleteOpen(true)
    },
  })

  const { table } = useDataTable({
    data: deployments,
    columns,
    totalCount,
    columnFilters,
    pagination,
    globalFilter,
    onColumnFiltersChange,
    onPaginationChange,
    onGlobalFilterChange,
    manualPagination: true,
    manualFiltering: true,
    withSortedRowModel: false,
    ensurePageInRange,
  })

  const statusFilterOptions = useMemo(() => {
    return [...getDeploymentStatusOptions(t)].map((opt) => ({
      label: opt.label,
      value: opt.value,
    }))
  }, [t])

  return (
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No Deployments Found')}
        emptyDescription={t(
          'No deployments available. Create one to get started.'
        )}
        skeletonKeyPrefix='deployment-skeleton'
        applyHeaderSize
        toolbarProps={{
          searchPlaceholder: t('Search deployments...'),
          searchDebounceMs: 500,
          filters: [
            {
              columnId: 'status',
              title: t('Status'),
              options: statusFilterOptions,
              singleSelect: true,
            },
          ],
        }}
      />

      <ViewLogsDialog
        open={logsOpen}
        onOpenChange={(open) => {
          setLogsOpen(open)
          if (!open) setLogsDeploymentId(null)
        }}
        deploymentId={logsDeploymentId}
      />

      <ViewDetailsDialog
        open={detailsOpen}
        onOpenChange={(open) => {
          setDetailsOpen(open)
          if (!open) setDetailsDeploymentId(null)
        }}
        deploymentId={detailsDeploymentId}
      />

      <UpdateConfigDialog
        open={updateOpen}
        onOpenChange={(open) => {
          setUpdateOpen(open)
          if (!open) setUpdateDeploymentId(null)
        }}
        deploymentId={updateDeploymentId}
      />

      <ExtendDeploymentDialog
        open={extendOpen}
        onOpenChange={(open) => {
          setExtendOpen(open)
          if (!open) setExtendDeploymentId(null)
        }}
        deploymentId={extendDeploymentId}
      />

      <RenameDeploymentDialog
        open={renameOpen}
        onOpenChange={(open) => {
          setRenameOpen(open)
          if (!open) setRenameDeploymentId(null)
        }}
        deploymentId={renameDeploymentId}
        currentName={renameCurrentName}
      />

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('Confirm delete')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                'Are you sure you want to delete deployment "{{name}}"? This action cannot be undone.',
                {
                  name:
                    deleteTarget?.container_name ||
                    deleteTarget?.deployment_name ||
                    deleteTarget?.id,
                }
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={isDeleting}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={isDeleting}
              variant='destructive'
            >
              {isDeleting ? t('Deleting...') : t('Delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
