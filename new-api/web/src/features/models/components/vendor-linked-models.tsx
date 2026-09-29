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
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePage,
  DataTableBulkActions,
  useDataTable,
} from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { createServerError } from '@/lib/server-error-message'

import { searchModels } from '../api'
import { getNameRuleConfig } from '../constants'
import { modelsQueryKeys } from '../lib'
import type { Model, Vendor } from '../types'
import { vendorErrorMessage, type VendorOperation } from '../vendor-api'
import { VendorOperationDialog } from './dialogs/vendor-operation-dialog'
import { ModelMutateDrawer } from './drawers/model-mutate-drawer'

export function VendorLinkedModels({
  vendor,
  onNavigate,
}: {
  vendor: Vendor
  onNavigate: (navigate: () => void) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const [keyword, setKeyword] = useState('')
  const [model, setModel] = useState<Model | null>(null)
  const [operation, setOperation] = useState<VendorOperation | null>(null)
  const params = {
    vendor: String(vendor.id),
    keyword,
    p: pagination.pageIndex + 1,
    page_size: pagination.pageSize,
  }
  const query = useQuery({
    queryKey: modelsQueryKeys.list(params),
    queryFn: async () => {
      const response = await searchModels(params)
      if (!response.success) {
        throw createServerError(response, t('Failed to load models'))
      }
      return response.data
    },
  })
  const rules = getNameRuleConfig(t)
  const columns: ColumnDef<Model>[] = [
    {
      id: 'select',
      enableSorting: false,
      enableHiding: false,
      size: 40,
      header: ({ table }) => (
        <Checkbox
          checked={table.getIsAllPageRowsSelected()}
          indeterminate={table.getIsSomePageRowsSelected()}
          onCheckedChange={(checked) =>
            table.toggleAllPageRowsSelected(Boolean(checked))
          }
          aria-label={t('Select all')}
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          checked={row.getIsSelected()}
          onCheckedChange={(checked) => row.toggleSelected(Boolean(checked))}
          aria-label={t('Select {{name}}', { name: row.original.model_name })}
        />
      ),
    },
    {
      accessorKey: 'model_name',
      header: t('Model'),
      enableHiding: false,
      meta: { mobileTitle: true },
      cell: ({ row }) => (
        <Button
          variant='link'
          className='text-foreground h-auto max-w-72 min-w-0 justify-start p-0 font-mono'
          title={row.original.model_name}
          onClick={() => setModel(row.original)}
        >
          <span className='truncate'>{row.original.model_name}</span>
        </Button>
      ),
    },
    {
      accessorKey: 'name_rule',
      header: t('Name Matching Rule'),
      cell: ({ row }) => rules[row.original.name_rule as 0 | 1 | 2 | 3]?.label,
    },
    {
      accessorKey: 'matched_count',
      header: t('Matched models'),
      cell: ({ row }) =>
        row.original.name_rule === 0 ? '—' : (row.original.matched_count ?? 0),
    },
  ]
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns,
    totalCount: query.data?.total ?? 0,
    getRowId: (row) => String(row.id),
    pagination,
    onPaginationChange: setPagination,
    globalFilter: keyword,
    onGlobalFilterChange: (value) => {
      setKeyword(typeof value === 'function' ? value(keyword) : value)
      setPagination((current) => ({ ...current, pageIndex: 0 }))
    },
    enableRowSelection: true,
    columnFilters: [],
    manualPagination: true,
    manualFiltering: true,
    ensurePageInRange: (count) => {
      if (
        !query.isPending &&
        !query.isError &&
        pagination.pageIndex >= count &&
        pagination.pageIndex > 0
      ) {
        setPagination((current) => ({
          ...current,
          pageIndex: Math.max(0, count - 1),
        }))
      }
    },
  })
  const selectedIds = table
    .getFilteredSelectedRowModel()
    .rows.map((row) => row.original.id)
  return (
    <div className='flex min-h-0 flex-1 flex-col gap-3 p-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground text-sm'>
          {t('{{count}} linked model records', {
            count: vendor.model_count ?? 0,
          })}
        </p>
        <Button
          variant='outline'
          size='sm'
          onClick={() => {
            onNavigate(() => {
              void navigate({
                to: '/models/$section',
                params: { section: 'metadata' },
                search: { vendor: [String(vendor.id)], page: 1 },
              })
            })
          }}
        >
          {t('Open filtered model list')}
        </Button>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Linked records are counted once. Matching rules show their covered models separately.'
        )}
      </p>
      <DataTableBulkActions
        table={table}
        entityName={t('model')}
        placement='inline'
      >
        <Button
          size='sm'
          variant='outline'
          onClick={() =>
            setOperation({ action: 'assign', model_ids: selectedIds })
          }
        >
          {t('Change vendor')}
        </Button>
        <Button
          size='sm'
          variant='outline'
          onClick={() =>
            setOperation({
              action: 'assign',
              model_ids: selectedIds,
              target_vendor_id: 0,
            })
          }
        >
          {t('Clear vendor')}
        </Button>
      </DataTableBulkActions>
      {query.isError ? (
        <ErrorState
          description={vendorErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <DataTablePage
          showMobileBulkActions
          mobileProps={{ enableRowSelection: true }}
          table={table}
          columns={columns}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          paginationInFooter={false}
          emptyTitle={t('No linked models')}
          emptyDescription={t(
            'Assign models from the model list to see them here.'
          )}
          toolbarProps={{
            searchPlaceholder: t('Filter by model name...'),
            searchDebounceMs: 300,
          }}
        />
      )}
      <ModelMutateDrawer
        open={Boolean(model)}
        onOpenChange={(open) => {
          if (!open) setModel(null)
        }}
        currentRow={model}
      />
      {operation && (
        <VendorOperationDialog
          selection={operation}
          onClose={() => setOperation(null)}
          onSuccess={() => table.resetRowSelection()}
        />
      )}
    </div>
  )
}
