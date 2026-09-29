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
import {
  Blend,
  FileText,
  HelpCircle,
  ImageIcon,
  Maximize2,
  Move,
  Paintbrush,
  RefreshCw,
  Scissors,
  Shuffle,
  Upload,
  UserRound,
  Video,
  WandSparkles,
  ZoomIn,
  type LucideIcon,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { formatTimestampToDate } from '@/lib/format'

import { MJ_TASK_TYPES } from '../../constants'
import {
  mjTaskTypeMapper,
  mjStatusMapper,
  mjSubmitResultMapper,
} from '../../lib/mappers'
import type { MidjourneyLog } from '../../types'
import { ImageDialog } from '../dialogs/image-dialog'
import { PromptDialog } from '../dialogs/prompt-dialog'
import {
  createDurationColumn,
  createChannelColumn,
  createProgressColumn,
  createFailReasonColumn,
} from './column-helpers'

const drawingTypeIconMap: Record<string, LucideIcon> = {
  [MJ_TASK_TYPES.IMAGINE]: ImageIcon,
  [MJ_TASK_TYPES.UPSCALE]: Maximize2,
  [MJ_TASK_TYPES.VIDEO]: Video,
  [MJ_TASK_TYPES.EDITS]: Paintbrush,
  [MJ_TASK_TYPES.VARIATION]: Shuffle,
  [MJ_TASK_TYPES.HIGH_VARIATION]: Shuffle,
  [MJ_TASK_TYPES.LOW_VARIATION]: Shuffle,
  [MJ_TASK_TYPES.PAN]: Move,
  [MJ_TASK_TYPES.DESCRIBE]: FileText,
  [MJ_TASK_TYPES.BLEND]: Blend,
  [MJ_TASK_TYPES.UPLOAD]: Upload,
  [MJ_TASK_TYPES.SHORTEN]: Scissors,
  [MJ_TASK_TYPES.REROLL]: RefreshCw,
  [MJ_TASK_TYPES.INPAINT]: WandSparkles,
  [MJ_TASK_TYPES.SWAP_FACE]: UserRound,
  [MJ_TASK_TYPES.ZOOM]: ZoomIn,
  [MJ_TASK_TYPES.CUSTOM_ZOOM]: ZoomIn,
}

function getDrawingTypeIcon(action: string): LucideIcon {
  return drawingTypeIconMap[action] ?? HelpCircle
}

export function useDrawingLogsColumns(
  isAdmin: boolean
): ColumnDef<MidjourneyLog>[] {
  const { t } = useTranslation()
  return useMemo(() => {
    const columns: ColumnDef<MidjourneyLog>[] = [
      {
        accessorKey: 'submit_time',
        header: t('Submit Time'),
        cell: ({ row }) => {
          const log = row.original
          const submitTime = row.getValue('submit_time') as number

          return (
            <div className='flex min-w-0 flex-col gap-0.5'>
              <span className='truncate font-mono text-xs tabular-nums'>
                {formatTimestampToDate(submitTime, 'milliseconds')}
              </span>
              <StatusBadge
                label={t(mjStatusMapper.getLabel(log.status))}
                variant={mjStatusMapper.getVariant(log.status)}
                size='sm'
                copyable={false}
              />
            </div>
          )
        },
        size: 180,
      },
    ]

    if (isAdmin) {
      columns.push(
        createChannelColumn<MidjourneyLog>({ headerLabel: t('Channel') })
      )
    }

    columns.push({
      accessorKey: 'action',
      header: t('Type'),
      cell: ({ row }) => {
        const action = row.getValue('action') as string
        return (
          <StatusBadge
            label={t(mjTaskTypeMapper.getLabel(action))}
            variant={mjTaskTypeMapper.getVariant(action)}
            icon={getDrawingTypeIcon(action)}
            size='sm'
            copyable={false}
            className='-ml-1.5'
          />
        )
      },
    })

    columns.push({
      accessorKey: 'mj_id',
      header: t('Task ID'),
      cell: ({ row }) => {
        const mjId = row.getValue('mj_id') as string

        if (!mjId) {
          return <span className='text-muted-foreground/60 text-xs'>-</span>
        }

        return (
          <div className='flex max-w-[160px] flex-col gap-0.5'>
            <StatusBadge
              label={mjId}
              copyText={mjId}
              variant='neutral'
              size='sm'
              className='border-border/60 bg-muted/30 !text-foreground max-w-full truncate rounded-md border px-1.5 py-0.5 font-mono'
            />
          </div>
        )
      },
      meta: { mobileTitle: true },
    })

    columns.push(
      createDurationColumn<MidjourneyLog>({
        submitTimeKey: 'submit_time',
        finishTimeKey: 'finish_time',
        headerLabel: t('Duration'),
      })
    )

    if (isAdmin) {
      columns.push({
        accessorKey: 'code',
        header: t('Submit Result'),
        cell: ({ row }) => {
          const code = row.getValue('code') as number

          return (
            <StatusBadge
              label={t(mjSubmitResultMapper.getLabel(String(code)))}
              variant={mjSubmitResultMapper.getVariant(String(code))}
              size='sm'
              copyable={false}
              className='-ml-1.5'
            />
          )
        },
      })
    }

    columns.push(
      createProgressColumn<MidjourneyLog>({ headerLabel: t('Progress') }),
      {
        accessorKey: 'image_url',
        header: t('Image'),
        cell: function ImageCell({ row }) {
          const log = row.original
          const imageUrl = row.getValue('image_url') as string
          const [dialogOpen, setDialogOpen] = useState(false)

          if (!imageUrl) {
            return <span className='text-muted-foreground/60 text-xs'>-</span>
          }

          return (
            <>
              <button
                type='button'
                className='group text-left text-xs'
                onClick={() => setDialogOpen(true)}
                title={t('Click to view image')}
              >
                <span className='text-foreground truncate leading-snug group-hover:underline'>
                  {t('View')}
                </span>
              </button>
              <ImageDialog
                imageUrl={imageUrl}
                taskId={log.mj_id}
                open={dialogOpen}
                onOpenChange={setDialogOpen}
              />
            </>
          )
        },
      },
      {
        accessorKey: 'prompt',
        header: t('Prompt'),
        cell: function PromptCell({ row }) {
          const log = row.original
          const prompt = row.getValue('prompt') as string
          const [dialogOpen, setDialogOpen] = useState(false)

          if (!prompt) {
            return <span className='text-muted-foreground/60 text-xs'>-</span>
          }

          return (
            <>
              <button
                type='button'
                className='group flex max-w-[220px] items-center text-left text-xs'
                onClick={() => setDialogOpen(true)}
                title={t('Click to view full prompt')}
              >
                <span className='text-muted-foreground truncate leading-snug group-hover:underline'>
                  {prompt}
                </span>
              </button>
              <PromptDialog
                prompt={prompt}
                promptEn={log.prompt_en}
                open={dialogOpen}
                onOpenChange={setDialogOpen}
              />
            </>
          )
        },
        size: 200,
        maxSize: 220,
      },
      createFailReasonColumn<MidjourneyLog>({
        headerLabel: t('Fail Reason'),
        cellTitle: t('Click to view full error message'),
      })
    )

    return columns
  }, [t, isAdmin])
}
