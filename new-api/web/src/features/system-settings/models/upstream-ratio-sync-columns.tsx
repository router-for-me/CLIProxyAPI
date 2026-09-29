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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTableColumnHeader } from '@/components/data-table'

import {
  SyncPriceCell,
  SyncSourceHeader,
  SyncSourcePriceCell,
} from './upstream-price-cells'
import { getUpstreamDisplayName } from './upstream-ratio-sync-helpers'
import type { PricingSyncRow } from './upstream-ratio-sync-table'

export function useUpstreamRatioSyncColumns(
  upstreamNames: string[],
  isMobile: boolean
): ColumnDef<PricingSyncRow>[] {
  const { t } = useTranslation()
  // Selection travels through context so toggling a checkbox does not replace
  // column renderer functions, remount the control and lose keyboard focus.
  return useMemo(
    () => [
      {
        accessorKey: 'model',
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Model')} />
        ),
        size: 220,
        minSize: 180,
        meta: { mobileTitle: true },
        cell: ({ row }) => (
          <span
            className='block max-w-72 truncate font-medium'
            title={row.original.model}
          >
            {row.original.model}
          </span>
        ),
      },
      {
        id: 'current',
        header: t('Current Price'),
        size: upstreamNames.length === 1 ? 420 : 320,
        minSize: 280,
        // Offset source cells' checkbox (pl-7) and ui/table's checkbox pr-0 (-mr-3).
        cell: ({ row }) => (
          <div className='-mr-3 pl-7'>
            <SyncPriceCell values={row.original.prices.current} />
          </div>
        ),
      },
      ...upstreamNames.map(
        (source): ColumnDef<PricingSyncRow> => ({
          id: `upstream_${source}`,
          size: upstreamNames.length === 1 ? 420 : 320,
          minSize: 280,
          header: isMobile
            ? getUpstreamDisplayName(source, t)
            : () => <SyncSourceHeader source={source} />,
          cell: ({ row }) => (
            <SyncSourcePriceCell row={row.original} source={source} />
          ),
        })
      ),
    ],
    [upstreamNames, isMobile, t]
  )
}
