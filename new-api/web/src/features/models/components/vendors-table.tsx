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
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { MoreHorizontal } from 'lucide-react'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { formatTimestampToDate } from '@/lib/format'
import { getLobeIcon } from '@/lib/lobe-icon'
import { createServerError } from '@/lib/server-error-message'

import { searchVendors } from '../api'
import { vendorsQueryKeys } from '../lib'
import type { Vendor } from '../types'
import { vendorErrorMessage, type VendorOperation } from '../vendor-api'
import { VendorOperationDialog } from './dialogs/vendor-operation-dialog'
import { useModels } from './models-provider'

const route = getRouteApi('/_authenticated/models/$section')

export function VendorsTable() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { setCurrentVendor, setOpen } = useModels()
  const [operation, setOperation] = useState<VendorOperation | null>(null)
  const state = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: {
      pageKey: 'vPage',
      pageSizeKey: 'vPageSize',
      defaultPageSize: 20,
    },
    globalFilter: { enabled: true, key: 'vFilter' },
    columnFilters: [
      { columnId: 'model_count', searchKey: 'vAssociation', type: 'array' },
    ],
  })
  const association = (
    state.columnFilters.find((filter) => filter.id === 'model_count')?.value as
      | string[]
      | undefined
  )?.[0]
  const params = {
    keyword: state.globalFilter,
    association,
    p: state.pagination.pageIndex + 1,
    page_size: state.pagination.pageSize,
  }
  const query = useQuery({
    queryKey: vendorsQueryKeys.list(params),
    queryFn: async () => {
      const response = await searchVendors(params)
      if (!response.success) {
        throw createServerError(response, t('Failed to load vendors'))
      }
      return response.data
    },
  })
  const columns: ColumnDef<Vendor>[] = [
    {
      id: 'select',
      enableHiding: false,
      enableSorting: false,
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
          aria-label={t('Select {{name}}', { name: row.original.name })}
        />
      ),
    },
    {
      accessorKey: 'name',
      header: t('Vendor'),
      enableHiding: false,
      size: 250,
      meta: { mobileTitle: true },
      cell: ({ row }) => (
        <div className='flex min-w-0 items-center gap-2'>
          <span className='flex size-7 shrink-0 items-center justify-center'>
            {getLobeIcon(row.original.icon, 24)}
          </span>
          <Button
            variant='link'
            className='text-foreground h-auto min-w-0 justify-start p-0 text-left'
            title={row.original.name}
            onClick={() => {
              setCurrentVendor(row.original)
              setOpen('update-vendor')
            }}
          >
            <span className='max-w-72 truncate'>{row.original.name}</span>
          </Button>
        </div>
      ),
    },
    {
      accessorKey: 'description',
      header: t('Description'),
      size: 350,
      cell: ({ row }) => (
        <p
          className='text-muted-foreground line-clamp-2 max-w-lg break-words whitespace-normal'
          title={row.original.description}
        >
          {row.original.description || '—'}
        </p>
      ),
    },
    {
      accessorKey: 'model_count',
      header: t('Linked models'),
      size: 160,
      cell: ({ row }) => (
        <Button
          variant='link'
          className='h-auto p-0 tabular-nums'
          onClick={() =>
            void navigate({
              to: '/models/$section',
              params: { section: 'metadata' },
              search: { vendor: [String(row.original.id)], page: 1 },
            })
          }
        >
          {row.original.model_count ?? 0}
        </Button>
      ),
    },
    { accessorKey: 'id', header: t('ID'), size: 80 },
    {
      accessorKey: 'created_time',
      header: t('Created At'),
      cell: ({ row }) => formatTimestampToDate(row.original.created_time),
    },
    {
      accessorKey: 'updated_time',
      header: t('Updated At'),
      cell: ({ row }) => formatTimestampToDate(row.original.updated_time),
    },
    {
      id: 'actions',
      header: t('Actions'),
      enableHiding: false,
      size: 70,
      cell: ({ row }) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button variant='ghost' size='icon' aria-label={t('Open menu')} />
            }
          >
            <MoreHorizontal className='size-4' />
          </DropdownMenuTrigger>
          <DropdownMenuContent align='end'>
            <DropdownMenuItem
              onClick={() => {
                setCurrentVendor(row.original)
                setOpen('update-vendor')
              }}
            >
              {t('Edit')}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant='destructive'
              onClick={() =>
                setOperation({
                  action: 'delete',
                  vendor_ids: [row.original.id],
                })
              }
            >
              {t('Delete')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
    },
  ]
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns,
    totalCount: query.data?.total ?? 0,
    getRowId: (vendor) => String(vendor.id),
    ...state,
    enableRowSelection: true,
    manualPagination: true,
    manualFiltering: true,
    initialColumnVisibility: {
      id: false,
      created_time: false,
      updated_time: false,
    },
  })
  const selectedIds = table
    .getFilteredSelectedRowModel()
    .rows.map((row) => row.original.id)
  if (query.isError) {
    return (
      <ErrorState
        description={vendorErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <>
      <DataTablePage
        showMobileBulkActions
        mobileProps={{ enableRowSelection: true }}
        table={table}
        columns={columns}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        emptyTitle={t('No vendors found')}
        emptyDescription={t('Add a vendor or adjust your search.')}
        applyHeaderSize
        toolbarProps={{
          searchPlaceholder: t('Search vendor names or descriptions'),
          searchDebounceMs: 300,
          filters: [
            {
              columnId: 'model_count',
              title: t('Model assignments'),
              singleSelect: true,
              options: [
                { label: t('With linked models'), value: 'linked' },
                { label: t('Without linked models'), value: 'unlinked' },
              ],
            },
          ],
        }}
        bulkActions={
          <DataTableBulkActions table={table} entityName={t('vendor')}>
            <Button
              variant='outline'
              size='sm'
              onClick={() =>
                setOperation({ action: 'merge', vendor_ids: selectedIds })
              }
            >
              {t('Merge vendors')}
            </Button>
            <Button
              variant='destructive'
              size='sm'
              onClick={() =>
                setOperation({ action: 'delete', vendor_ids: selectedIds })
              }
            >
              {t('Delete vendors')}
            </Button>
          </DataTableBulkActions>
        }
      />
      {operation && (
        <VendorOperationDialog
          selection={operation}
          onClose={() => setOperation(null)}
          onSuccess={() => table.resetRowSelection()}
        />
      )}
    </>
  )
}
