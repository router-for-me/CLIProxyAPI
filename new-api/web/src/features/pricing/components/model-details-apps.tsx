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
  ArrowDownRight,
  ArrowUpRight,
  ExternalLink,
  Trophy,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames as tableStyles,
} from '@/components/data-table'
import { cn } from '@/lib/utils'

import {
  buildAppRankings,
  formatTokenVolume,
  type AppRanking,
} from '../lib/mock-stats'
import type { PricingModel } from '../types'

const COMPACT_NUMBER = new Intl.NumberFormat(undefined, {
  notation: 'compact',
  maximumFractionDigits: 1,
})

function RankBadge(props: { rank: number }) {
  const rank = props.rank
  const isPodium = rank <= 3
  const palette =
    rank === 1
      ? 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300'
      : rank === 2
        ? 'bg-slate-100 text-slate-700 dark:bg-slate-500/20 dark:text-slate-300'
        : rank === 3
          ? 'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-300'
          : 'bg-muted text-muted-foreground'
  return (
    <span
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-md font-mono text-xs font-bold tabular-nums',
        palette
      )}
    >
      {isPodium ? <Trophy className='size-3.5' /> : rank}
    </span>
  )
}

function GrowthChip(props: { value: number }) {
  const value = props.value
  const isUp = value > 0
  const isDown = value < 0
  const palette = isUp
    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300'
    : isDown
      ? 'bg-rose-100 text-rose-700 dark:bg-rose-500/20 dark:text-rose-300'
      : 'bg-muted text-muted-foreground'
  const Icon = isUp ? ArrowUpRight : isDown ? ArrowDownRight : null
  const formatted = `${value > 0 ? '+' : ''}${value.toFixed(1)}%`
  return (
    <span
      className={cn(
        'inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 font-mono text-[11px] font-semibold tabular-nums',
        palette
      )}
    >
      {Icon && <Icon className='size-3' />}
      {formatted}
    </span>
  )
}

function AppLink(props: { app: AppRanking }) {
  if (!props.app.url) {
    return <span className='text-foreground'>{props.app.name}</span>
  }
  return (
    <a
      href={props.app.url}
      target='_blank'
      rel='noreferrer'
      className='text-foreground hover:text-primary inline-flex items-center gap-1 transition-colors'
    >
      {props.app.name}
      <ExternalLink className='text-muted-foreground/40 size-3' />
    </a>
  )
}

export function ModelDetailsApps(props: { model: PricingModel }) {
  const { t } = useTranslation()
  const apps = useMemo(() => buildAppRankings(props.model, 12), [props.model])

  if (apps.length === 0) {
    return (
      <div className='text-muted-foreground rounded-lg border p-6 text-center text-sm'>
        {t('No app usage data available for this model.')}
      </div>
    )
  }

  const totalMonthlyTokens = apps.reduce((s, a) => s + a.monthly_tokens, 0)
  const top = apps[0]
  return (
    <div className='flex flex-col gap-4'>
      <div className='grid grid-cols-1 gap-2 sm:grid-cols-3'>
        <div className='bg-muted/20 rounded-lg border p-3'>
          <div className='text-muted-foreground text-[10px] font-medium tracking-wider uppercase'>
            {t('Tracked apps')}
          </div>
          <div className='text-foreground mt-1 font-mono text-lg font-semibold tabular-nums'>
            {apps.length}
          </div>
          <p className='text-muted-foreground/70 text-[11px]'>
            {t('Top integrations using this model')}
          </p>
        </div>
        <div className='bg-muted/20 rounded-lg border p-3'>
          <div className='text-muted-foreground text-[10px] font-medium tracking-wider uppercase'>
            {t('Monthly tokens')}
          </div>
          <div className='text-foreground mt-1 font-mono text-lg font-semibold tabular-nums'>
            {COMPACT_NUMBER.format(totalMonthlyTokens)}
          </div>
          <p className='text-muted-foreground/70 text-[11px]'>
            {t('Aggregated across the apps below')}
          </p>
        </div>
        <div className='bg-muted/20 rounded-lg border p-3'>
          <div className='text-muted-foreground text-[10px] font-medium tracking-wider uppercase'>
            {t('#1 by usage')}
          </div>
          <div className='text-foreground mt-1 truncate text-base font-semibold'>
            {top.name}
          </div>
          <p className='text-muted-foreground/70 truncate text-[11px]'>
            {top.category} · {formatTokenVolume(top.monthly_tokens)}{' '}
            {t('tokens / mo')}
          </p>
        </div>
      </div>

      <StaticDataTable
        className='rounded-lg'
        tableClassName='text-sm'
        headerRowClassName={tableStyles.compactHeaderRow}
        data={apps}
        getRowKey={(app) => `${app.rank}-${app.name}`}
        columns={[
          {
            id: 'rank',
            header: '#',
            className: cn(tableStyles.compactHeaderCell, 'w-12'),
            cellClassName: tableStyles.compactCell,
            cell: (app) => <RankBadge rank={app.rank} />,
          },
          {
            id: 'app',
            header: t('App'),
            className: tableStyles.compactHeaderCell,
            cellClassName: tableStyles.compactCell,
            cell: (app) => (
              <div className='flex items-center gap-3'>
                <span className='bg-muted text-muted-foreground inline-flex size-7 shrink-0 items-center justify-center rounded-md font-bold'>
                  {app.initial}
                </span>
                <div className='min-w-0'>
                  <div className='text-sm font-medium'>
                    <AppLink app={app} />
                  </div>
                  <p className='text-muted-foreground line-clamp-1 text-sm'>
                    {app.description}
                  </p>
                </div>
              </div>
            ),
          },
          {
            id: 'category',
            header: t('Category'),
            className: cn(
              tableStyles.compactHeaderCell,
              'hidden md:table-cell'
            ),
            cellClassName: cn(
              tableStyles.compactMutedCell,
              'hidden md:table-cell'
            ),
            cell: (app) => app.category,
          },
          {
            id: 'monthly-tokens',
            header: t('Monthly tokens'),
            className: tableStyles.compactHeaderCellRight,
            cellClassName: cn(tableStyles.compactNumericCell, 'tabular-nums'),
            cell: (app) => formatTokenVolume(app.monthly_tokens),
          },
          {
            id: 'growth',
            header: t('30d change'),
            className: tableStyles.compactHeaderCellRight,
            cellClassName: cn(tableStyles.compactCell, 'text-right'),
            cell: (app) => <GrowthChip value={app.growth_pct} />,
          },
        ]}
      />

      <p className='text-muted-foreground/60 text-[11px] leading-relaxed'>
        {t(
          'App rankings shown here are simulated for preview purposes and will be replaced with live usage data once the backend integration is complete.'
        )}
      </p>
    </div>
  )
}
