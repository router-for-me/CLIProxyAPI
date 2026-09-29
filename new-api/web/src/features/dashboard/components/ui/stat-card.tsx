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
import type { LucideIcon } from 'lucide-react'
import { useId, type ReactNode } from 'react'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

type StatCardTone = 'accent-1' | 'accent-2' | 'accent-3'
type StatCardSparklineVariant = 'bars' | 'line'
type StatCardDetailTone =
  | 'default'
  | 'muted'
  | 'success'
  | 'warning'
  | 'destructive'

export interface StatCardDetail {
  label: string
  value: string
  tone?: StatCardDetailTone
}

interface StatCardProps {
  title: string
  value: string | number
  description: string
  icon: LucideIcon
  sparkline?: number[]
  sparklineVariant?: StatCardSparklineVariant
  details?: StatCardDetail[]
  tone?: StatCardTone
  loading?: boolean
  error?: boolean
  action?: ReactNode
  iconTone?: IconBadgeTone
  compactMobile?: boolean
}

const TONE_CLASSES: Record<StatCardTone, string> = {
  'accent-1':
    'from-overview-accent-1/80 via-overview-accent-1/45 to-overview-accent-1/5 dark:from-overview-accent-1/70 dark:via-overview-accent-1/30',
  'accent-2':
    'from-overview-accent-2/80 via-overview-accent-2/45 to-overview-accent-2/5 dark:from-overview-accent-2/70 dark:via-overview-accent-2/30',
  'accent-3':
    'from-overview-accent-3/80 via-overview-accent-3/45 to-overview-accent-3/5 dark:from-overview-accent-3/70 dark:via-overview-accent-3/30',
}

const LINE_TONE_CLASSES: Record<StatCardTone, string> = {
  'accent-1': 'text-overview-accent-1',
  'accent-2': 'text-overview-accent-2',
  'accent-3': 'text-overview-accent-3',
}

const ICON_TONE_BY_STAT_TONE: Record<StatCardTone, IconBadgeTone> = {
  'accent-1': 'chart-1',
  'accent-2': 'chart-2',
  'accent-3': 'chart-3',
}

const DETAIL_TONE_CLASSES: Record<StatCardDetailTone, string> = {
  default: 'text-foreground',
  muted: 'text-muted-foreground',
  success: 'text-success',
  warning: 'text-warning',
  destructive: 'text-destructive',
}

interface SparklineBucket {
  position: number
  height: number
}

function normalizeSparkline(values?: number[]): SparklineBucket[] {
  if (!values?.length) return []

  const sanitized = values.map((value) => Math.max(0, Number(value) || 0))
  const max = Math.max(...sanitized)
  if (max <= 0) {
    return sanitized.map((_, position) => ({ position, height: 0 }))
  }

  return sanitized.map((value, position) => ({
    position,
    height: Math.max(8, (value / max) * 100),
  }))
}

function buildLineSparkline(values?: number[]) {
  if (!values?.length) return null

  const sanitized = values.map((value) => Math.max(0, Number(value) || 0))
  const width = 160
  const height = 36
  const padding = 3
  const max = Math.max(...sanitized)
  const min = Math.min(...sanitized)
  const range = max - min

  const points = sanitized.map((value, index) => {
    const x =
      sanitized.length === 1
        ? width / 2
        : (index / (sanitized.length - 1)) * width
    let normalized = 0
    if (range > 0) {
      normalized = (value - min) / range
    } else if (max > 0) {
      normalized = 0.5
    }
    const y = height - padding - normalized * (height - padding * 2)

    return { x, y }
  })

  const linePath = points
    .map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x} ${point.y}`)
    .join(' ')
  const firstPoint = points.at(0)
  const lastPoint = points.at(-1)
  if (!firstPoint || !lastPoint) return null
  const areaPath = `${linePath} L ${lastPoint.x} ${height} L ${firstPoint.x} ${height} Z`

  return {
    areaPath,
    linePath,
  }
}

function LineSparkline(props: { values?: number[]; tone: StatCardTone }) {
  const rawGradientId = useId()
  const gradientId = `stat-card-line-${rawGradientId.replaceAll(':', '')}`
  const paths = buildLineSparkline(props.values)

  if (!paths) return <div className='h-8' aria-hidden='true' />

  return (
    <div
      className={cn(
        'relative h-8 overflow-hidden rounded-lg',
        LINE_TONE_CLASSES[props.tone]
      )}
      aria-hidden='true'
    >
      <svg
        viewBox='0 0 160 36'
        preserveAspectRatio='none'
        className='size-full'
      >
        <defs>
          <linearGradient id={gradientId} x1='0' x2='0' y1='0' y2='1'>
            <stop offset='0%' stopColor='currentColor' stopOpacity='0.24' />
            <stop offset='100%' stopColor='currentColor' stopOpacity='0' />
          </linearGradient>
        </defs>
        <path d={paths.areaPath} fill={`url(#${gradientId})`} />
        <path
          d={paths.linePath}
          fill='none'
          stroke='currentColor'
          strokeLinecap='round'
          strokeLinejoin='round'
          strokeWidth='2.25'
          vectorEffect='non-scaling-stroke'
        />
      </svg>
    </div>
  )
}

function BarSparkline(props: { values?: number[]; tone: StatCardTone }) {
  const sparkline = normalizeSparkline(props.values)

  return (
    <div className='flex h-8 items-end gap-1' aria-hidden='true'>
      {sparkline.map((bucket) => (
        <span
          key={bucket.position}
          className={cn(
            'flex-1 rounded-t-sm bg-linear-to-t',
            bucket.height <= 0 && 'opacity-20',
            TONE_CLASSES[props.tone]
          )}
          style={{ height: `${bucket.height}%` }}
        />
      ))}
    </div>
  )
}

function StatCardDetails(props: { details: StatCardDetail[] }) {
  return (
    <div className='grid grid-cols-2 gap-2'>
      {props.details.map((detail) => (
        <div
          key={detail.label}
          className='bg-muted/40 rounded-lg border border-transparent px-2.5 py-2'
        >
          <div className='text-muted-foreground truncate text-[11px] leading-none font-medium'>
            {detail.label}
          </div>
          <div
            className={cn(
              'mt-1.5 truncate text-xs font-semibold tabular-nums',
              DETAIL_TONE_CLASSES[detail.tone ?? 'default']
            )}
            title={detail.value}
          >
            {detail.value}
          </div>
        </div>
      ))}
    </div>
  )
}

export function StatCard(props: StatCardProps) {
  const Icon = props.icon
  const tone = props.tone ?? 'accent-3'
  const iconTone = props.iconTone ?? ICON_TONE_BY_STAT_TONE[tone]
  const sparklineVariant = props.sparklineVariant ?? 'bars'
  let valueContent: ReactNode
  if (props.loading) {
    valueContent = (
      <div
        className={cn(
          'flex flex-col',
          props.compactMobile ? 'gap-1' : 'gap-1.5'
        )}
      >
        <Skeleton className='h-5 w-16 sm:h-7 sm:w-24' />
        <Skeleton
          className={cn(
            'h-3 w-24 sm:h-3.5 sm:w-32',
            props.compactMobile && 'hidden sm:block'
          )}
        />
      </div>
    )
  } else if (props.error) {
    valueContent = (
      <div className='flex flex-col gap-1'>
        <div className='text-muted-foreground mt-0.5 font-mono text-base font-bold tracking-tight break-all tabular-nums sm:text-2xl'>
          --
        </div>
        <p
          className={cn(
            'text-muted-foreground/60 line-clamp-1 text-[11px] sm:text-xs',
            props.compactMobile && 'hidden sm:block'
          )}
        >
          {props.description}
        </p>
      </div>
    )
  } else {
    valueContent = (
      <div className='flex flex-col gap-1'>
        <div className='text-foreground font-mono text-base font-semibold tracking-tight break-all tabular-nums sm:text-2xl'>
          {props.value}
        </div>
        <p
          className={cn(
            'text-muted-foreground/60 line-clamp-1 text-[11px] leading-relaxed sm:text-xs',
            props.compactMobile && 'hidden sm:block'
          )}
        >
          {props.description}
        </p>
      </div>
    )
  }

  let visualization: ReactNode
  if (props.details?.length) {
    visualization = <StatCardDetails details={props.details} />
  } else if (sparklineVariant === 'line') {
    visualization = <LineSparkline values={props.sparkline} tone={tone} />
  } else {
    visualization = <BarSparkline values={props.sparkline} tone={tone} />
  }

  return (
    <div
      className={cn(
        'group flex flex-col justify-between sm:min-h-32 sm:gap-3',
        props.compactMobile ? 'gap-1' : 'gap-1.5'
      )}
    >
      <div className='flex items-start justify-between gap-1'>
        <div className='text-muted-foreground flex items-center gap-1 text-[11px] font-medium sm:gap-2 sm:text-xs'>
          <IconBadge
            tone={iconTone}
            size='stat'
            className={cn(
              props.compactMobile &&
                'size-4 rounded-sm [&>svg]:size-2.5 sm:size-7 sm:rounded-md sm:[&>svg]:size-3.5'
            )}
          >
            <Icon />
          </IconBadge>
          <span className='line-clamp-1 leading-snug sm:line-clamp-2'>
            {props.title}
          </span>
        </div>
        {props.action && <div className='shrink-0'>{props.action}</div>}
      </div>

      {valueContent}

      <div className='hidden sm:block'>{visualization}</div>
    </div>
  )
}
