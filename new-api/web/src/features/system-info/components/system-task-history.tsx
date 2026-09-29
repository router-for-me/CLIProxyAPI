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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTablePagination,
  DataTableToolbar,
  useDataTable,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import { listSystemTasks } from '@/features/system-settings/api'
import type {
  SystemTask,
  SystemTaskFilters,
} from '@/features/system-settings/types'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { deleteSystemTaskHistory } from '../api'
import { SYSTEM_TASK_TYPE_LABEL } from '../constants'
import { SystemTasksTable } from './system-tasks-table'

const EMPTY_TASKS: SystemTask[] = []
const HISTORY_QUERY_KEY = ['system-info', 'system-tasks', 'history']

export function SystemTaskHistory(props: { activeRefreshAt: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [filters, setFilters] = useState<
    Pick<SystemTaskFilters, 'type' | 'status'>
  >({ type: '', status: '' })
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [cleanupOpen, setCleanupOpen] = useState(false)
  const historyQuery = useQuery({
    queryKey: [...HISTORY_QUERY_KEY, filters, pagination],
    queryFn: async () => {
      const res = await listSystemTasks(pagination.pageSize, {
        ...filters,
        scope: 'history',
        offset: pagination.pageIndex * pagination.pageSize,
      })
      if (!res.success || !Array.isArray(res.data)) {
        throw createServerError(res, t('We could not load system tasks.'))
      }
      return { tasks: res.data, total: res.total }
    },
    retry: false,
  })

  // A completed active task must appear in history on the same refresh, even
  // when finishing the last task stops active-task polling.
  useEffect(() => {
    if (props.activeRefreshAt) {
      void queryClient.invalidateQueries({ queryKey: HISTORY_QUERY_KEY })
    }
  }, [props.activeRefreshAt, queryClient])

  const tasks = historyQuery.data?.tasks ?? EMPTY_TASKS
  const { table } = useDataTable({
    data: tasks,
    columns: [],
    totalCount: historyQuery.data?.total ?? 0,
    manualPagination: true,
    columnFilters: [],
    pagination,
    onPaginationChange: setPagination,
    columnVisibilityStorageKey: false,
    columnSizingStorageKey: false,
    ensurePageInRange: (pageCount) => {
      if (
        historyQuery.isSuccess &&
        !historyQuery.isFetching &&
        pagination.pageIndex >= Math.max(1, pageCount)
      ) {
        setPagination((previous) => ({
          ...previous,
          pageIndex: Math.max(0, pageCount - 1),
        }))
      }
    },
  })
  const cleanupMutation = useMutation({
    mutationFn: async () => {
      const res = await deleteSystemTaskHistory(filters)
      if (!res.success) throw createServerError(res, t('Cleanup failed'))
      return res.data?.deleted_count ?? 0
    },
    onSuccess: async (count) => {
      toast.success(t('Deleted {{count}} historical system tasks', { count }))
      setCleanupOpen(false)
      setPagination((previous) => ({ ...previous, pageIndex: 0 }))
      await queryClient.invalidateQueries({ queryKey: HISTORY_QUERY_KEY })
    },
    onError: (error) => handleServerError(error, t('Cleanup failed')),
  })

  return (
    <div className='space-y-3'>
      <div>
        <h4 className='text-sm font-medium'>{t('Task History')}</h4>
        <p className='text-muted-foreground mt-0.5 text-xs'>
          {t('Recently completed or failed system task runs.')}
        </p>
      </div>
      <DataTableToolbar
        table={table}
        hideViewOptions
        hasAdditionalFilters={!!filters.type || !!filters.status}
        onReset={() => {
          setFilters({ type: '', status: '' })
          setPagination((previous) => ({ ...previous, pageIndex: 0 }))
        }}
        customSearch={
          <>
            <NativeSelect
              aria-label={t('Type')}
              className='max-w-full'
              value={filters.type}
              onChange={(event) => {
                setFilters((previous) => ({
                  ...previous,
                  type: event.target.value,
                }))
                setPagination((previous) => ({ ...previous, pageIndex: 0 }))
              }}
            >
              <NativeSelectOption value=''>{t('All Types')}</NativeSelectOption>
              {Object.entries(SYSTEM_TASK_TYPE_LABEL).map(([value, label]) => (
                <NativeSelectOption key={value} value={value}>
                  {t(label)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect
              aria-label={t('Status')}
              className='max-w-full'
              value={filters.status}
              onChange={(event) => {
                setFilters((previous) => ({
                  ...previous,
                  status: event.target.value as '' | 'succeeded' | 'failed',
                }))
                setPagination((previous) => ({ ...previous, pageIndex: 0 }))
              }}
            >
              <NativeSelectOption value=''>
                {t('All Status')}
              </NativeSelectOption>
              <NativeSelectOption value='succeeded'>
                {t('succeeded')}
              </NativeSelectOption>
              <NativeSelectOption value='failed'>
                {t('failed')}
              </NativeSelectOption>
            </NativeSelect>
          </>
        }
        preActions={
          <Button
            variant='destructive'
            size='sm'
            disabled={
              historyQuery.isFetching ||
              historyQuery.isError ||
              !historyQuery.data?.total ||
              cleanupMutation.isPending
            }
            onClick={() => setCleanupOpen(true)}
          >
            {t('Clean task history')}
          </Button>
        }
      />
      <div aria-busy={historyQuery.isFetching}>
        {historyQuery.isLoading && <Skeleton className='h-24 w-full' />}
        {historyQuery.isError && (
          <ErrorState
            title={t('We could not load system tasks.')}
            description={historyQuery.error.message}
            onRetry={() => void historyQuery.refetch()}
          />
        )}
        {!historyQuery.isLoading && !historyQuery.isError && (
          <>
            {tasks.length > 0 ? (
              <SystemTasksTable tasks={tasks} />
            ) : (
              <EmptyState
                title={t('No historical system tasks.')}
                className='min-h-32'
                bordered
              />
            )}
            <div className='mt-3'>
              <DataTablePagination table={table} compact />
            </div>
          </>
        )}
      </div>
      <ConfirmDialog
        open={cleanupOpen}
        onOpenChange={(open) => {
          if (!cleanupMutation.isPending) setCleanupOpen(open)
        }}
        title={t('Clean task history')}
        desc={t(
          'Delete completed tasks matching the current filters across all pages? Active tasks and the latest run of each task type are retained for scheduling. This cannot be undone.'
        )}
        destructive
        isLoading={cleanupMutation.isPending}
        confirmText={cleanupMutation.isPending ? t('Deleting...') : t('Delete')}
        handleConfirm={() => cleanupMutation.mutate()}
      />
    </div>
  )
}
