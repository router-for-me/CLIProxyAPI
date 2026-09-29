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
import type { Table as TanstackTable } from '@tanstack/react-table'

import { isContentSizedColumn } from './content-sized-columns'

export function DataTableColgroup<TData>({
  table,
}: {
  table: TanstackTable<TData>
}) {
  const columns = table.getVisibleLeafColumns()
  const sizedColumns = columns.filter(
    (column) => !isContentSizedColumn(column.id)
  )
  const totalSize = sizedColumns.reduce((sum, col) => sum + col.getSize(), 0)

  return (
    <colgroup>
      {columns.map((column) => {
        const width = getColumnWidth(
          table,
          column.id,
          column.getSize(),
          totalSize
        )

        return <col key={column.id} style={{ width }} />
      })}
    </colgroup>
  )
}

function getColumnWidth<TData>(
  table: TanstackTable<TData>,
  columnId: string,
  columnSize: number,
  totalSize: number
) {
  if (isContentSizedColumn(columnId)) {
    return '1%'
  }

  if (table.options.enableColumnResizing === true) {
    return `${columnSize}px`
  }

  if (totalSize <= 0) {
    return undefined
  }

  return `${(columnSize / totalSize) * 100}%`
}
