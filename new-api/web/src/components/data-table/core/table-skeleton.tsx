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
import type { Table } from '@tanstack/react-table'

import { Skeleton } from '@/components/ui/skeleton'
import { TableRow, TableCell } from '@/components/ui/table'
import { cn } from '@/lib/utils'

const SKELETON_WIDTHS = [
  '75%',
  '60%',
  '85%',
  '50%',
  '70%',
  '90%',
  '55%',
  '80%',
  '65%',
  '45%',
]

interface TableSkeletonProps<TData> {
  table: Table<TData>
  rowCount?: number
  rowHeight?: string
  keyPrefix?: string
}

export function TableSkeleton<TData>({
  table,
  rowCount,
  rowHeight = 'h-[52px]',
  keyPrefix = 'skeleton',
}: TableSkeletonProps<TData>) {
  const visibleColumns = table.getVisibleLeafColumns()

  const finalRowCount =
    rowCount ?? Math.min(table.getState().pagination?.pageSize || 20, 20)

  return (
    <>
      {Array.from({ length: finalRowCount }, (_, rowIndex) => (
        <TableRow
          key={`${keyPrefix}-${rowIndex}`}
          className={cn(rowHeight, 'border-b')}
        >
          {visibleColumns.map((column, colIndex) => {
            const isSelectColumn = column.id === 'select'
            const widthIndex =
              (rowIndex * visibleColumns.length + colIndex) %
              SKELETON_WIDTHS.length

            return (
              <TableCell key={column.id} className='py-3'>
                <Skeleton
                  className={cn(
                    'h-4 rounded-sm',
                    isSelectColumn ? 'size-4' : undefined
                  )}
                  style={
                    isSelectColumn
                      ? undefined
                      : { width: SKELETON_WIDTHS[widthIndex] }
                  }
                />
              </TableCell>
            )
          })}
        </TableRow>
      ))}
    </>
  )
}
