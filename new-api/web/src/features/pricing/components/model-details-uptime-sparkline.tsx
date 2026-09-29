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
import { Activity, AlertCircle, CheckCircle2 } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  formatUptimePct,
  getSuccessRateDotClass,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import { cn } from '@/lib/utils'

import { aggregateUptime, type UptimeDayPoint } from '../lib/mock-stats'

// ---------------------------------------------------------------------------
// Uptime sparkline
// ---------------------------------------------------------------------------
//
// Compact 30-day uptime visualisation: a row of small coloured bars where:
//   - Bar colour reflects per-day uptime (green / amber / red)
//   - Bar height reflects severity (the worse the day, the shorter the bar)
//   - Hovering a bar reveals the exact date and uptime
//
// Useful as a header strip ("at-a-glance" status) and as a per-row visual
// inside the per-group performance table.

type SparklineSize = 'sm' | 'md'

type UptimeSparklineProps = {
  series: UptimeDayPoint[]
  overallSuccessRate?: number
  size?: SparklineSize
  showOverall?: boolean
  emptyLabel?: string
  className?: string
}

function heightFor(uptime: number): string {
  if (uptime >= 99.9) return 'h-full'
  if (uptime >= 99.0) return 'h-[88%]'
  if (uptime >= 95.0) return 'h-[72%]'
  if (uptime >= 90.0) return 'h-[55%]'
  return 'h-[40%]'
}

export function UptimeSparkline(props: UptimeSparklineProps) {
  const size = props.size ?? 'md'
  const showOverall = props.showOverall ?? true

  if (props.series.length === 0) {
    return (
      <span className={cn('text-muted-foreground text-xs', props.className)}>
        {props.emptyLabel ?? '—'}
      </span>
    )
  }

  const overall =
    props.overallSuccessRate ??
    props.series.reduce((s, p) => s + p.uptime_pct, 0) / props.series.length

  const containerHeight = size === 'sm' ? 'h-3.5' : 'h-5'
  const barWidth = size === 'sm' ? 'w-[3px]' : 'w-1'
  const gap = size === 'sm' ? 'gap-px' : 'gap-[2px]'

  return (
    <div className={cn('flex items-center gap-2', props.className)}>
      <div
        className={cn('flex items-end', containerHeight, gap)}
        role='img'
        aria-label={`30 day uptime ${overall.toFixed(2)}%`}
      >
        {props.series.map((day) => (
          <Tooltip key={day.date}>
            <TooltipTrigger
              render={
                <div
                  className={cn(
                    'rounded-sm transition-opacity hover:opacity-80',
                    barWidth,
                    containerHeight,
                    'flex items-end'
                  )}
                />
              }
            >
              <div
                className={cn(
                  'w-full rounded-sm',
                  getSuccessRateDotClass(day.uptime_pct),
                  heightFor(day.uptime_pct)
                )}
                aria-hidden
              />
            </TooltipTrigger>
            <TooltipContent side='top' className='font-mono text-xs'>
              <div className='font-medium'>{day.date}</div>
              <div>{formatUptimePct(day.uptime_pct)}</div>
              {day.outage_minutes > 0 && (
                <div className='text-muted-foreground'>
                  {day.outage_minutes} min outage
                </div>
              )}
            </TooltipContent>
          </Tooltip>
        ))}
      </div>
      {showOverall && (
        <span
          className={cn(
            'font-mono text-sm font-semibold tabular-nums',
            getSuccessRateTextClass(overall)
          )}
        >
          {formatUptimePct(overall)}
        </span>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Uptime status row — sparkline + summary text + status icon
// ---------------------------------------------------------------------------

export function UptimeStatusRow(props: {
  series: UptimeDayPoint[]
  className?: string
}) {
  const { t } = useTranslation()
  const summary = useMemo(() => aggregateUptime(props.series), [props.series])
  const status = useMemo(() => {
    if (summary.uptime_pct >= 99.9) return 'operational'
    if (summary.uptime_pct >= 99.0) return 'minor'
    if (summary.uptime_pct >= 95.0) return 'degraded'
    return 'major'
  }, [summary.uptime_pct])

  let StatusIcon = AlertCircle
  let statusColour = 'text-rose-600 dark:text-rose-400'
  let statusLabel = t('Significant outages detected')
  if (status === 'operational') {
    StatusIcon = CheckCircle2
    statusColour = 'text-emerald-600 dark:text-emerald-400'
    statusLabel = t('All systems operational')
  } else if (status === 'minor') {
    StatusIcon = Activity
    statusColour = 'text-emerald-600 dark:text-emerald-400'
    statusLabel = t('Minor blips in the last 30 days')
  } else if (status === 'degraded') {
    statusColour = 'text-amber-600 dark:text-amber-400'
    statusLabel = t('Degraded performance recently')
  }

  return (
    <div
      className={cn(
        'border-border/60 bg-muted/30 flex flex-wrap items-center gap-3 rounded-lg border px-3 py-2 sm:gap-4 sm:px-4',
        props.className
      )}
    >
      <div className='flex items-center gap-2'>
        <StatusIcon className={cn('size-4 shrink-0', statusColour)} />
        <span className='text-sm font-medium'>{t('Last 30 days uptime')}</span>
      </div>

      <UptimeSparkline series={props.series} className='ml-auto' />

      <div className='flex items-center gap-3 text-xs'>
        <span className={cn('font-medium', statusColour)}>{statusLabel}</span>
        {summary.incidents > 0 && (
          <span className='text-muted-foreground'>
            {summary.incidents}{' '}
            {summary.incidents === 1 ? t('incident') : t('incidents')}
          </span>
        )}
        {summary.outage_minutes > 0 && (
          <span className='text-muted-foreground'>
            {summary.outage_minutes} {t('min downtime')}
          </span>
        )}
        <span className='text-muted-foreground hidden sm:inline'>
          {formatUptimePct(summary.uptime_pct)} {t('overall')}
        </span>
      </div>
    </div>
  )
}
