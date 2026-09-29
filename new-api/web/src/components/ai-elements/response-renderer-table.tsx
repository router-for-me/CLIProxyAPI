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
import type { ReactNode } from 'react'
import type { TableCellNode, TableNode } from 'stream-markdown-parser'

import { cn } from '@/lib/utils'

import { getNodeKey } from './response-content'
import type { BlockRendererOptions } from './response-types'

function getTableCellAlignClass(
  align: TableCellNode['align'] | undefined
): string {
  if (align === 'right') {
    return 'text-right'
  }

  if (align === 'center') {
    return 'text-center'
  }

  return 'text-left'
}

function renderTableCell(
  node: TableCellNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  const alignClass = getTableCellAlignClass(node.align)

  if (node.header) {
    return (
      <th
        className={cn(
          'text-muted-foreground px-3 py-2 text-xs font-semibold whitespace-nowrap',
          alignClass
        )}
        key={key}
      >
        {options.renderChildren(node.children)}
      </th>
    )
  }

  return (
    <td className={cn('px-3 py-2 align-top', alignClass)} key={key}>
      {options.renderChildren(node.children)}
    </td>
  )
}

export function renderTable(
  node: TableNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  return (
    <div
      className='border-border/70 my-4 w-full overflow-x-auto rounded-lg border'
      key={key}
    >
      <table className='my-0 w-full min-w-max border-separate border-spacing-0 text-sm'>
        <thead className='bg-muted/60'>
          <tr className='border-border/70'>
            {node.header.cells.map((cell, index) =>
              renderTableCell(cell, getNodeKey(cell, index), options)
            )}
          </tr>
        </thead>
        <tbody className='divide-border/70 divide-y'>
          {node.rows.map((row, rowIndex) => (
            <tr className='border-border/70' key={getNodeKey(row, rowIndex)}>
              {row.cells.map((cell, cellIndex) =>
                renderTableCell(cell, getNodeKey(cell, cellIndex), options)
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
