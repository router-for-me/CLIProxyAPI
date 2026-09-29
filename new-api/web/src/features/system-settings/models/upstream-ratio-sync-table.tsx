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
import { Search } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePagination,
  DataTableView,
  MobileCardList,
  useDataTable,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { LoadingState } from '@/components/loading-state'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useDebounce } from '@/hooks/use-debounce'
import { useMediaQuery } from '@/hooks/use-media-query'

import type { DifferencesMap, PricingSyncModels } from '../types'
import { SyncSourceHeader } from './upstream-price-cells'
import { useUpstreamRatioSyncColumns } from './upstream-ratio-sync-columns'
import {
  getSyncPriceKind,
  sameSyncPrice,
  SyncPriceContext,
  type PricingSourceSelection,
  type PricingSourceSelections,
} from './upstream-ratio-sync-helpers'

export type PricingSyncRow = {
  model: string
  prices: PricingSyncModels[string]
  differences: DifferencesMap[string]
}
type UpstreamRatioSyncTableProps = {
  prices: PricingSyncModels
  toolbar?: ReactNode
  differences: DifferencesMap
  selectedSources: PricingSourceSelections
  isDisabled: boolean
  isSyncing: boolean
  onSelectPrices: (selections: PricingSourceSelection[]) => void
  onUnselectPrices: (models: string[]) => void
}
export function UpstreamRatioSyncTable(props: UpstreamRatioSyncTableProps) {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState('__all__')
  const debouncedSearch = useDebounce(search, 250)
  const rows = useMemo(
    () =>
      Object.entries(props.prices)
        .filter(([, values]) =>
          Object.values(values.upstreams).some(
            (candidate) => !sameSyncPrice(values.current, candidate)
          )
        )
        .map(([model, prices]) => ({
          model,
          prices,
          differences: props.differences[model],
        }))
        .sort((a, b) => a.model.localeCompare(b.model)),
    [props.prices, props.differences]
  )
  const filteredRows = useMemo(
    () =>
      rows.filter(
        (row) =>
          row.model
            .toLowerCase()
            .includes(debouncedSearch.trim().toLowerCase()) &&
          (kind === '__all__' ||
            Object.values(row.prices.upstreams).some(
              (price) => getSyncPriceKind(price) === kind
            ))
      ),
    [rows, debouncedSearch, kind]
  )
  const sourceNames = useMemo(
    () =>
      [
        ...new Set(rows.flatMap((row) => Object.keys(row.prices.upstreams))),
      ].sort(),
    [rows]
  )
  const bulkStates = useMemo(
    () =>
      Object.fromEntries(
        sourceNames.map((source) => {
          const selections = filteredRows
            .filter(
              (row) =>
                row.prices.upstreams[source] &&
                !sameSyncPrice(row.prices.current, row.prices.upstreams[source])
            )
            .map((row) => ({ model: row.model, source }))
          return [
            source,
            {
              selections,
              selectedModels: selections
                .filter(
                  (selection) =>
                    props.selectedSources[selection.model] === source
                )
                .map((selection) => selection.model),
            },
          ]
        })
      ),
    [filteredRows, sourceNames, props.selectedSources]
  )
  const columns = useUpstreamRatioSyncColumns(sourceNames, isMobile)
  const { table } = useDataTable({
    data: filteredRows,
    columns,
    getRowId: (row) => row.model,
    initialPagination: { pageIndex: 0, pageSize: 10 },
    withFilteredRowModel: false,
    withSortedRowModel: true,
    withFacetedRowModel: false,
  })
  const types = [
    { value: '__all__', label: t('All Types') },
    { value: 'expression', label: t('Expression pricing') },
    { value: 'token', label: t('Per-token') },
    { value: 'request', label: t('Per-request') },
  ]
  let content: ReactNode
  if (props.isSyncing) {
    content = <LoadingState message={t('Fetching upstream prices...')} />
  } else if (rows.length === 0) {
    content = (
      <EmptyState
        title={t('No upstream price differences found')}
        bordered
        className='min-h-48 flex-1'
      />
    )
  } else if (isMobile) {
    content = (
      <>
        <div className='shrink-0 space-y-2 rounded-md border px-3 py-2'>
          {sourceNames.map((source) => (
            <SyncSourceHeader key={source} source={source} />
          ))}
        </div>
        <div className='min-h-0 flex-1 overflow-y-auto'>
          <MobileCardList table={table} emptyTitle={t('No results found')} />
        </div>
      </>
    )
  } else {
    content = (
      <DataTableView
        table={table}
        containerClassName='min-h-0 flex-1 rounded-md'
        tableContainerClassName='h-full min-h-0'
        tableHeaderClassName='[background-color:var(--table-header)]'
          tableBodyClassName='[&>tr]:h-14'
        splitHeaderScrollClassName='h-full'
        bodyContainerClassName='[scrollbar-gutter:stable]'
        splitHeader
        getColumnClassName={(_, part) =>
          part === 'header'
            ? 'h-11 px-3 align-middle'
            : 'h-14 px-3 py-2 align-middle'
        }
        getRowClassName={() => 'align-middle'}
        emptyContent={t('No results found')}
        emptyCellClassName='h-24 text-center'
      />
    )
  }
  return (
    <SyncPriceContext.Provider
      value={{
        bulkStates,
        selectedSources: props.selectedSources,
        isDisabled: props.isDisabled,
        onSelectPrices: props.onSelectPrices,
        onUnselectPrices: props.onUnselectPrices,
      }}
    >
      <div className='flex h-full min-h-0 flex-col gap-3'>
        <div className='flex shrink-0 flex-wrap items-center gap-2'>
          {props.toolbar}
          <div className='relative min-w-40 flex-1 sm:max-w-72'>
            <Search
              className='text-muted-foreground absolute top-1/2 left-2 size-4 -translate-y-1/2'
              aria-hidden
            />
            <Input
              value={search}
              onChange={(event) => {
                setSearch(event.target.value)
                table.setPageIndex(0)
              }}
              placeholder={t('Search model name...')}
              aria-label={t('Search models')}
              className='ps-8'
              disabled={props.isDisabled}
            />
          </div>
          <Select
            value={kind}
            items={types}
            onValueChange={(value) => {
              if (value) {
                setKind(value)
                table.setPageIndex(0)
              }
            }}
            disabled={props.isDisabled}
          >
            <SelectTrigger className='w-36' aria-label={t('Billing Mode')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              {types.map((type) => (
                <SelectItem value={type.value} key={type.value}>
                  {type.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className='text-muted-foreground text-xs sm:ml-auto'>
            USD / {t('1M token')}
          </span>
        </div>
        {content}
        <div className='shrink-0'>
          <DataTablePagination table={table} />
        </div>
      </div>
    </SyncPriceContext.Provider>
  )
}
