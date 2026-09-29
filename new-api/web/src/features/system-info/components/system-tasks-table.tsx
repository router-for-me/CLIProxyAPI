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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type {
  SystemTask,
  SystemTaskStatus,
} from '@/features/system-settings/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatTimestampRelative, formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { SYSTEM_TASK_TYPE_LABEL } from '../constants'

const STATUS_VARIANT: Record<SystemTaskStatus, 'secondary' | 'destructive'> = {
  pending: 'secondary',
  running: 'secondary',
  succeeded: 'secondary',
  failed: 'destructive',
}

const STATUS_CLASS_NAME: Record<SystemTaskStatus, string> = {
  pending:
    'bg-amber-50 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
  running:
    'bg-sky-50 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300 [&_span]:bg-sky-500',
  succeeded:
    'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
  failed: '',
}

const STATUS_DOT_CLASS_NAME: Record<SystemTaskStatus, string> = {
  pending: 'bg-amber-500',
  running: 'bg-sky-500',
  succeeded: 'bg-emerald-500',
  failed: 'bg-destructive',
}

const PROGRESS_BAR_CLASS_NAME: Record<SystemTaskStatus, string> = {
  pending: '[&_[data-slot=progress-indicator]]:bg-amber-500',
  running: '[&_[data-slot=progress-indicator]]:bg-sky-500',
  succeeded: '[&_[data-slot=progress-indicator]]:bg-emerald-500',
  failed: '[&_[data-slot=progress-indicator]]:bg-destructive',
}

const TYPE_DISPLAY_ID: Record<string, string> = {
  midjourney_poll: 'drawing_task_poll',
}

function getProgress(task: SystemTask): number | null {
  const progress = (task.state as { progress?: unknown } | undefined)?.progress
  if (typeof progress !== 'number' || Number.isNaN(progress)) return null
  return Math.min(100, Math.max(0, progress))
}

type SystemTasksTableProps = {
  tasks: SystemTask[]
}

export function SystemTasksTable(props: SystemTasksTableProps) {
  const { t, i18n } = useTranslation()

  return (
    <StaticDataTable tableClassName='min-w-[900px]'>
      <TableHeader>
        <TableRow className='bg-muted/40 hover:bg-muted/40'>
          <TableHead className='h-9 w-[260px] px-4 text-xs'>
            {t('Type')}
          </TableHead>
          <TableHead className='h-9 w-[130px] text-xs'>{t('Status')}</TableHead>
          <TableHead className='h-9 w-[180px] text-xs'>
            {t('Progress')}
          </TableHead>
          <TableHead className='h-9 min-w-[260px] text-xs'>
            {t('Executor')}
          </TableHead>
          <TableHead className='h-9 w-[190px] text-xs'>
            {t('Updated')}
          </TableHead>
          <TableHead className='h-9 w-[220px] pr-4 text-xs'>
            {t('Detail')}
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.tasks.map((task) => {
          const progress = getProgress(task)
          return (
            <TableRow key={task.task_id} className='hover:bg-muted/30'>
              <TableCell className='px-4 py-3 align-middle'>
                <div className='space-y-0.5'>
                  <div className='font-medium'>
                    {t(SYSTEM_TASK_TYPE_LABEL[task.type] ?? task.type)}
                  </div>
                  <div className='text-muted-foreground font-mono text-[11px]'>
                    {TYPE_DISPLAY_ID[task.type] ?? task.type}
                  </div>
                </div>
              </TableCell>
              <TableCell className='py-3 align-middle'>
                <Badge
                  variant={STATUS_VARIANT[task.status]}
                  className={cn('gap-1.5', STATUS_CLASS_NAME[task.status])}
                >
                  <span
                    className={cn(
                      'size-1.5 rounded-full',
                      STATUS_DOT_CLASS_NAME[task.status]
                    )}
                    aria-hidden='true'
                  />
                  {t(task.status)}
                </Badge>
              </TableCell>
              <TableCell className='py-3 align-middle'>
                <div className='flex items-center gap-2'>
                  <Progress
                    value={progress ?? 0}
                    className={cn('w-24', PROGRESS_BAR_CLASS_NAME[task.status])}
                  />
                  <span className='text-muted-foreground w-10 text-right text-xs tabular-nums'>
                    {progress === null ? '-' : `${progress}%`}
                  </span>
                </div>
              </TableCell>
              <TableCell className='text-muted-foreground max-w-[280px] truncate py-3 align-middle font-mono text-xs'>
                {task.locked_by || '-'}
              </TableCell>
              <TableCell
                className='text-muted-foreground py-3 align-middle text-xs whitespace-nowrap'
                title={formatTimestampToDate(task.updated_at)}
              >
                {formatTimestampRelative(
                  task.updated_at,
                  'seconds',
                  toIntlLocale(i18n.language)
                )}
              </TableCell>
              <TableCell
                className='text-destructive max-w-[220px] truncate py-3 pr-4 align-middle text-xs'
                title={task.error || undefined}
              >
                {task.error || '-'}
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </StaticDataTable>
  )
}
