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
import {
  flexRender,
  type Cell,
  type Row,
  type Table as TanstackTable,
} from '@tanstack/react-table'
import * as React from 'react'

import { TableCell, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'

import { TruncatedCell } from './truncated-cell'
import type { DataTableColumnClassName } from './types'

type DataTableRowProps<TData> = {
  row: Row<TData>
  className?: string
  getColumnClassName?: DataTableColumnClassName
  cellRenderColumns?: TanstackTable<TData>['options']['columns']
} & Omit<React.ComponentProps<typeof TableRow>, 'children'>

type DataTableRowInnerProps<TData> = DataTableRowProps<TData> & {
  isSelected: boolean
  /**
   * Stable signature of currently visible leaf columns for this row.
   * Captured outside the memo comparator so visibility toggles re-render
   * even when the TanStack row object reference stays the same.
   */
  visibleColumnIds: string
}

function DataTableRowInner<TData>({
  row,
  isSelected,
  className,
  getColumnClassName,
  cellRenderColumns,
  visibleColumnIds,
  ...rowProps
}: DataTableRowInnerProps<TData>) {
  // Destructured only to keep them out of `rowProps` (not valid DOM attrs)
  // and to feed the memo comparator below; intentionally unused here.
  void cellRenderColumns
  void visibleColumnIds

  return (
    <TableRow
      data-state={isSelected ? 'selected' : undefined}
      className={className}
      {...rowProps}
    >
      {row.getVisibleCells().map((cell) => {
        const renderedCell = renderCellContent(cell)

        return (
          <TableCell
            key={cell.id}
            data-column-id={cell.column.id}
            className={cn(
              'max-w-full min-w-0',
              renderedCell.isPrimitive && 'overflow-hidden',
              getColumnClassName?.(cell.column.id, 'cell')
            )}
          >
            {renderedCell.content}
          </TableCell>
        )
      })}
    </TableRow>
  )
}

const MemoizedDataTableRow = React.memo(DataTableRowInner, (prev, next) => {
  // Do not read row.getIsSelected() / row.getVisibleCells() inside the
  // comparator: TanStack row objects keep a stable reference while selection
  // and columnVisibility mutate on the table instance. Reading them here would
  // compare identical live values and miss those updates. Both are lifted to
  // explicit props, captured per render in DataTableRow.
  //
  // Column cell renderers (and getColumnClassName) can close over external
  // state while the row stays stable, so column definitions and the class
  // resolver are part of the render identity and must be compared too.
  return (
    prev.row === next.row &&
    prev.className === next.className &&
    prev.isSelected === next.isSelected &&
    prev.visibleColumnIds === next.visibleColumnIds &&
    prev.getColumnClassName === next.getColumnClassName &&
    prev.cellRenderColumns === next.cellRenderColumns
  )
}) as typeof DataTableRowInner

export function DataTableRow<TData>(props: DataTableRowProps<TData>) {
  const visibleColumnIds = props.row
    .getVisibleCells()
    .map((cell) => cell.column.id)
    .join('\0')

  return (
    <MemoizedDataTableRow
      {...props}
      isSelected={props.row.getIsSelected()}
      visibleColumnIds={visibleColumnIds}
    />
  )
}

function renderCellContent<TData>(cell: Cell<TData, unknown>) {
  const content = flexRender(cell.column.columnDef.cell, cell.getContext())
  const textContent = getPrimitiveTextContent(content)

  if (!textContent) {
    return { content, isPrimitive: false }
  }

  return {
    content: (
      <TruncatedCell tooltipContent={textContent}>{content}</TruncatedCell>
    ),
    isPrimitive: true,
  }
}

function getPrimitiveTextContent(content: React.ReactNode): string | null {
  if (typeof content === 'string' || typeof content === 'number') {
    return String(content)
  }

  return null
}
