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
import { flexRender, type Cell, type Table } from '@tanstack/react-table'
import type { ReactNode } from 'react'

/**
 * Shared cell helpers for the column-meta-driven card content used by both the
 * mobile list and the desktop card grid. Kept separate from the card content
 * component so the module exports only non-component utilities.
 */

export function getCellLabel<TData>(cell: Cell<TData, unknown>): string | null {
  const { header, meta } = cell.column.columnDef
  if (typeof header === 'string') {
    return header
  }
  if (meta?.label) {
    return meta.label
  }
  return null
}

export function renderCellContent<TData>(
  cell: Cell<TData, unknown>
): ReactNode {
  const cellRenderer = cell.column.columnDef.cell
  if (cellRenderer) {
    return flexRender(cellRenderer, cell.getContext())
  }
  return cell.getValue() as ReactNode
}

/**
 * Whether any visible column declares `mobileTitle`/`mobileBadge` meta. When
 * true the compact two-tier layout is used; otherwise the condensed
 * label:value fallback layout is used.
 */
export function tableHasCompactMeta<TData>(table: Table<TData>): boolean {
  return table.getVisibleLeafColumns().some((col) => {
    const meta = col.columnDef.meta
    return Boolean(meta?.mobileTitle || meta?.mobileBadge)
  })
}
