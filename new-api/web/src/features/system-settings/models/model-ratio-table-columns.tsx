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
import type { ColumnDef } from '@tanstack/react-table'

import { DataTableColumnHeader } from '@/components/data-table/core/column-header'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { StatusBadge } from '@/components/status-badge'
import { Checkbox } from '@/components/ui/checkbox'

import {
  getModeLabel,
  getModeVariant,
  getPriceDetail,
  getPriceSummary,
  type ModelRow,
} from './model-pricing-snapshots'

export const TASK_PRICING_MODE_FILTER = 'tiered_expr_task'

const filterBySelectedValues = (
  rowValue: unknown,
  filterValue: unknown
): boolean => {
  if (!Array.isArray(filterValue) || filterValue.length === 0) return true
  return filterValue.includes(String(rowValue))
}

type BuildModelRatioColumnsOptions = {
  onDelete: (name: string) => void
  onEdit: (model: ModelRow) => void
  deleteDisabled?: boolean
  taskModelNames?: Set<string>
  t: (key: string) => string
}

export function buildModelRatioColumns({
  onDelete,
  onEdit,
  deleteDisabled,
  taskModelNames,
  t,
}: BuildModelRatioColumnsOptions): ColumnDef<ModelRow>[] {
  return [
    {
      id: 'select',
      header: ({ table }) => (
        <Checkbox
          checked={table.getIsAllPageRowsSelected()}
          indeterminate={table.getIsSomePageRowsSelected()}
          onCheckedChange={(value) => table.toggleAllPageRowsSelected(!!value)}
          aria-label={t('Select all')}
          className='translate-y-[2px]'
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          checked={row.getIsSelected()}
          onCheckedChange={(value) => row.toggleSelected(!!value)}
          aria-label={t('Select row')}
          className='translate-y-[2px]'
        />
      ),
      enableSorting: false,
      enableHiding: false,
      size: 40,
    },
    {
      accessorKey: 'name',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Model name')} />
      ),
      cell: ({ row }) => {
        const isTaskModel = Boolean(taskModelNames?.has(row.original.name))
        const hasConfiguredTaskPricing =
          row.original.billingMode === 'tiered_expr' &&
          Boolean(row.original.billingExpr)
        const showTaskPricingBadge = isTaskModel && hasConfiguredTaskPricing
        const showTieredBadge =
          row.original.billingMode === 'tiered_expr' && !isTaskModel
        const showUnconfiguredTaskBadge = isTaskModel && !hasConfiguredTaskPricing

        return (
          <div className='flex min-w-0 items-center gap-2 font-medium'>
            <span className='min-w-0 truncate'>{row.getValue('name')}</span>
            {showTieredBadge ? (
              <StatusBadge
                label={t('Tiered')}
                variant='info'
                copyable={false}
                className='shrink-0'
              />
            ) : null}
            {showTaskPricingBadge ? (
              <StatusBadge
                label={t('Task pricing')}
                variant='info'
                copyable={false}
                className='shrink-0'
              />
            ) : null}
            {row.original.hasConflict && (
              <StatusBadge
                label={t('Conflict')}
                variant='danger'
                copyable={false}
                className='shrink-0'
              />
            )}
            {showUnconfiguredTaskBadge ? (
              <StatusBadge
                label={t('Task pricing not configured')}
                variant='warning'
                copyable={false}
                className='shrink-0'
              />
            ) : null}
          </div>
        )
      },
      enableHiding: false,
    },
    {
      accessorKey: 'billingMode',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Mode')} />
      ),
      cell: ({ row }) => (
        <StatusBadge
          label={t(getModeLabel(row.original.billingMode))}
          variant={getModeVariant(row.original.billingMode)}
          copyable={false}
          showDot={false}
          className='-ml-1.5 px-0'
        />
      ),
      filterFn: (row, id, value) => {
        if (filterBySelectedValues(row.getValue(id), value)) return true
        if (!Array.isArray(value) || !value.includes(TASK_PRICING_MODE_FILTER)) {
          return false
        }
        return (
          Boolean(taskModelNames?.has(row.original.name)) &&
          row.original.billingMode === 'tiered_expr' &&
          Boolean(row.original.billingExpr)
        )
      },
      meta: { label: t('Mode') },
    },
    {
      id: 'priceSummary',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Price summary')} />
      ),
      cell: ({ row }) => (
        <div className='flex min-w-0 flex-col gap-1'>
          <span className='truncate font-medium'>
            {getPriceSummary(row.original, t)}
          </span>
          <span className='text-muted-foreground truncate text-xs'>
            {getPriceDetail(row.original, t)}
          </span>
        </div>
      ),
      sortingFn: (rowA, rowB) =>
        getPriceSummary(rowA.original, t).localeCompare(
          getPriceSummary(rowB.original, t)
        ),
      meta: { label: t('Price summary') },
    },
    {
      id: 'actions',
      header: () => <div>{t('Actions')}</div>,
      cell: ({ row }) => (
        <StaticRowActions
          editLabel={t('Edit')}
          deleteLabel={t('Delete')}
          menuLabel={t('Open menu')}
          onEdit={() => onEdit(row.original)}
          onDelete={() => onDelete(row.original.name)}
          deleteDisabled={deleteDisabled}
        />
      ),
      enableHiding: false,
    },
  ]
}
