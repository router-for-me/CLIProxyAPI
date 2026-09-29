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
import { Zap } from 'lucide-react'
/* eslint-disable react-refresh/only-export-components */
import { useState } from 'react'

import { DataTableColumnHeader } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { formatTimestampToDate, formatTokens } from '@/lib/format'
import { cn } from '@/lib/utils'

import { formatDuration } from '../../lib/format'
import { FailReasonDialog } from '../dialogs/fail-reason-dialog'

/**
 * Cache tooltip component for token display
 */
export function CacheTooltip({
  tokens,
  label,
  color,
}: {
  tokens: number
  label: string
  color: string
}) {
  if (tokens <= 0) return null

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          render={<Zap className={`size-3 flex-shrink-0 ${color}`} />}
        ></TooltipTrigger>
        <TooltipContent side='top'>
          <p className='text-xs'>
            {label}: {formatTokens(tokens)}
          </p>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// ============================================================================
// Column Definition Factories
// ============================================================================

/**
 * Create a timestamp column - compact mono style matching common logs
 */
export function createTimestampColumn<T>(config: {
  accessorKey: string
  title: string
  unit?: 'seconds' | 'milliseconds'
}): ColumnDef<T> {
  const { accessorKey, title, unit = 'milliseconds' } = config

  return {
    accessorKey,
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title={title} />
    ),
    cell: ({ row }) => {
      const timestamp = row.getValue(accessorKey) as number
      if (!timestamp) {
        return <span className='text-muted-foreground/60 text-xs'>-</span>
      }
      return (
        <span className='font-mono text-xs tabular-nums'>
          {formatTimestampToDate(timestamp, unit)}
        </span>
      )
    },
    meta: { label: title },
  }
}

/**
 * Create a duration column - pill style matching common logs timing
 */
export function createDurationColumn<T>(config: {
  submitTimeKey: string
  finishTimeKey: string
  unit?: 'seconds' | 'milliseconds'
  headerLabel: string
  warningThresholdSec?: number
}): ColumnDef<T> {
  const {
    submitTimeKey,
    finishTimeKey,
    unit = 'milliseconds',
    headerLabel,
    warningThresholdSec = 60,
  } = config

  return {
    id: 'duration',
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title={headerLabel} />
    ),
    cell: ({ row }) => {
      const log = row.original as Record<string, unknown>
      const duration = formatDuration(
        log[submitTimeKey] as number | undefined,
        log[finishTimeKey] as number | undefined,
        unit
      )

      if (!duration) {
        return <span className='text-muted-foreground/60 text-xs'>-</span>
      }

      const variant =
        duration.durationSec > warningThresholdSec ? 'danger' : 'success'

      const durationBgMap: Record<string, string> = {
        success:
          'border border-emerald-200/40 bg-emerald-50/35 !text-emerald-600 dark:border-emerald-900/40 dark:bg-emerald-950/15 dark:!text-emerald-400',
        warning:
          'border border-amber-200/45 bg-amber-50/35 !text-amber-600 dark:border-amber-900/40 dark:bg-amber-950/15 dark:!text-amber-400',
        danger:
          'border border-rose-200/50 bg-rose-50/35 !text-red-600 dark:border-rose-900/40 dark:bg-rose-950/15 dark:!text-red-400',
      }

      return (
        <StatusBadge
          label={`${duration.durationSec.toFixed(1)}s`}
          variant={variant}
          size='sm'
          copyable={false}
          className={cn('rounded-md font-mono', durationBgMap[variant])}
        />
      )
    },
    meta: { label: headerLabel },
  }
}

/**
 * Create a channel column (admin only) - #id badge matching common logs
 */
export function createChannelColumn<T>(config: {
  accessorKey?: string
  headerLabel: string
}): ColumnDef<T> {
  const { accessorKey = 'channel_id', headerLabel } = config

  return {
    accessorKey,
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title={headerLabel} />
    ),
    cell: ({ row }) => {
      const channelId = row.getValue(accessorKey) as number
      if (!channelId) {
        return <span className='text-muted-foreground/60 text-xs'>-</span>
      }
      return (
        <StatusBadge
          label={`#${channelId}`}
          autoColor={String(channelId)}
          copyText={String(channelId)}
          size='sm'
          showDot={false}
          className='font-mono'
        />
      )
    },
    meta: { label: headerLabel },
  }
}

/**
 * Create a fail reason column - text-xs truncate, hover underline, dialog
 */
export function createFailReasonColumn<T>(config: {
  accessorKey?: string
  headerLabel: string
  cellTitle: string
}): ColumnDef<T> {
  const { accessorKey = 'fail_reason', headerLabel, cellTitle } = config

  return {
    accessorKey,
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title={headerLabel} />
    ),
    cell: function FailReasonCell({ row }) {
      const failReason = row.getValue(accessorKey) as string
      const [dialogOpen, setDialogOpen] = useState(false)

      if (!failReason) {
        return <span className='text-muted-foreground/60 text-xs'>-</span>
      }

      return (
        <>
          <button
            type='button'
            className='group flex max-w-[200px] items-center gap-1 text-left text-xs'
            onClick={() => setDialogOpen(true)}
            title={cellTitle}
          >
            <span className='truncate leading-snug text-red-600 group-hover:underline dark:text-red-400'>
              {failReason}
            </span>
          </button>
          <FailReasonDialog
            failReason={failReason}
            open={dialogOpen}
            onOpenChange={setDialogOpen}
          />
        </>
      )
    },
    meta: { label: headerLabel },
  }
}

/**
 * Create a progress column - compact mono pill
 */
export function createProgressColumn<T>(config: {
  accessorKey?: string
  headerLabel: string
}): ColumnDef<T> {
  const { accessorKey = 'progress', headerLabel } = config

  return {
    accessorKey,
    header: ({ column }) => (
      <DataTableColumnHeader column={column} title={headerLabel} />
    ),
    cell: ({ row }) => {
      const progress = row.getValue(accessorKey) as string
      if (!progress) {
        return <span className='text-muted-foreground/60 text-xs'>-</span>
      }
      return (
        <span className='border-border/60 bg-muted/30 inline-flex items-center rounded-md border px-1.5 py-0.5 font-mono text-xs'>
          {progress}
        </span>
      )
    },
    meta: { label: headerLabel },
  }
}
