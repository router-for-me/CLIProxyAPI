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
import type { Cell, Row } from '@tanstack/react-table'
import * as React from 'react'

import { StatusBadgeTypeContext } from '@/components/status-badge'

import { getCellLabel, renderCellContent } from './card-cell-utils'

function orderCardCells<TData>(
  cells: Cell<TData, unknown>[]
): Cell<TData, unknown>[] {
  return [...cells].sort((a, b) => {
    const aOrder = a.column.columnDef.meta?.mobileOrder
    const bOrder = b.column.columnDef.meta?.mobileOrder

    if (aOrder == null && bOrder == null) return 0
    if (aOrder == null) return 1
    if (bOrder == null) return -1
    return aOrder - bOrder
  })
}

/**
 * Shared, column-meta-driven card content rendering for TanStack rows.
 *
 * Both {@link MobileCardList} (mobile) and {@link DataTableCardGrid} (desktop
 * card view) render the same inner content; only the surrounding container
 * differs (single bordered list vs. responsive grid of cards). Keeping the
 * per-row content here guarantees the two stay visually consistent.
 *
 * Column meta extensions (see `card-cell-utils.ts`):
 * - `mobileTitle`  — card header (left, larger text)
 * - `mobileBadge`  — inline with title (right, e.g. status badge)
 * - `mobileHidden` — hidden in card content
 */

/**
 * Compact content — structured layout with title header + side-by-side fields.
 * Used when columns define mobileTitle or mobileBadge meta.
 *
 * Visual structure:
 *   [Title content]             [Badge]
 *   [Field1 label] [Field2 label]
 *   [Field1 value] [Field2 value]
 *                          [Actions ⋯]
 */
function CompactContent<TData>({ row }: { row: Row<TData> }) {
  const allCells = row
    .getVisibleCells()
    .filter((cell) => cell.column.id !== 'select')

  // Read each cell's meta once, then reuse for all categorisation checks.
  const cellMetas = React.useMemo(
    () => allCells.map((c) => c.column.columnDef.meta),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [allCells.map((c) => c.id).join(',')]
  )

  const titleCell = allCells.find((_, i) => cellMetas[i]?.mobileTitle)
  const badgeCell = allCells.find((_, i) => cellMetas[i]?.mobileBadge)
  const actionsCell = allCells.find((c) => c.column.id === 'actions')
  const fieldCells = orderCardCells(
    allCells.filter(
      (c, i) =>
        c !== titleCell &&
        c !== badgeCell &&
        c !== actionsCell &&
        !cellMetas[i]?.mobileHidden
    )
  )

  return (
    <>
      {/* Row 1: Title + Badge */}
      <div className='flex items-center justify-between gap-2'>
        {titleCell && (
          <div className='min-w-0 flex-1 text-sm font-medium [&_[data-slot=status-badge]]:max-w-full [&_[data-slot=status-badge]]:whitespace-normal'>
            {renderCellContent(titleCell)}
          </div>
        )}
        {badgeCell && (
          <div className='flex-none [&_[data-slot=status-badge]]:max-w-none'>
            {renderCellContent(badgeCell)}
          </div>
        )}
      </div>

      {/* Row 2: Key fields wrap into compact columns instead of squeezing */}
      {fieldCells.length > 0 && (
        <div className='mt-1.5 grid grid-cols-2 gap-x-3 gap-y-1.5'>
          {fieldCells.map((cell) => {
            const label = getCellLabel(cell)
            return (
              <div key={cell.id} className='min-w-0 flex-1 overflow-hidden'>
                {label && (
                  <div className='text-muted-foreground mb-0.5 text-[10px] leading-none select-none'>
                    {label}
                  </div>
                )}
                <div className='min-w-0 overflow-hidden text-xs [&_:is([data-slot=badge-cell],[data-slot=provider-badge],[data-slot=status-badge])]:ml-0'>
                  <StatusBadgeTypeContext.Provider value='text'>
                    {renderCellContent(cell) ?? '-'}
                  </StatusBadgeTypeContext.Provider>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {/* Actions */}
      {actionsCell && (
        <div className='mt-1 -mb-0.5 flex justify-end'>
          {renderCellContent(actionsCell)}
        </div>
      )}
    </>
  )
}

/**
 * Fallback content — condensed label:value pairs for tables without
 * mobileTitle/mobileBadge. Still respects mobileHidden.
 */
function FallbackContent<TData>({ row }: { row: Row<TData> }) {
  const allCells = row
    .getVisibleCells()
    .filter((cell) => cell.column.id !== 'select')

  const cellMetas = React.useMemo(
    () => allCells.map((c) => c.column.columnDef.meta),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [allCells.map((c) => c.id).join(',')]
  )

  const actionsCell = allCells.find((c) => c.column.id === 'actions')
  const contentCells = orderCardCells(
    allCells.filter(
      (c, i) => c.column.id !== 'actions' && !cellMetas[i]?.mobileHidden
    )
  )

  return (
    <>
      {contentCells.map((cell) => {
        const label = getCellLabel(cell)

        if (!label) {
          return (
            <div
              key={cell.id}
              className='flex justify-end overflow-hidden [&_:is([data-slot=badge-cell],[data-slot=provider-badge],[data-slot=status-badge])]:ml-0'
            >
              <StatusBadgeTypeContext.Provider value='text'>
                {renderCellContent(cell)}
              </StatusBadgeTypeContext.Provider>
            </div>
          )
        }

        return (
          <div
            key={cell.id}
            className='flex items-start justify-between gap-2 overflow-hidden'
          >
            <span className='text-muted-foreground shrink-0 text-[10px] font-medium select-none'>
              {label}
            </span>
            <div className='flex min-w-0 flex-1 items-center justify-end overflow-hidden text-xs [&_:is([data-slot=badge-cell],[data-slot=provider-badge],[data-slot=status-badge])]:ml-0'>
              <StatusBadgeTypeContext.Provider value='text'>
                {renderCellContent(cell) ?? '-'}
              </StatusBadgeTypeContext.Provider>
            </div>
          </div>
        )
      })}
      {actionsCell && (
        <div className='-mb-0.5 flex justify-end pt-0.5'>
          {renderCellContent(actionsCell)}
        </div>
      )}
    </>
  )
}

/**
 * Renders a single row's card content, auto-selecting the compact or fallback
 * layout. Callers compute `compact` once per table (via `tableHasCompactMeta`)
 * and pass it down to avoid recomputation per row.
 */
export function CardRowContent<TData>(props: {
  row: Row<TData>
  compact: boolean
}) {
  return props.compact ? (
    <CompactContent row={props.row} />
  ) : (
    <FallbackContent row={props.row} />
  )
}
