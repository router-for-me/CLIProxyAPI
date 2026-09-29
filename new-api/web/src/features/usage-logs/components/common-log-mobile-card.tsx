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
import { flexRender, type Cell } from '@tanstack/react-table'
import { ChevronRight, KeyRound } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import dayjs from '@/lib/dayjs'
import { formatLogQuota, formatTimestampToDate } from '@/lib/format'

import type { UsageLog } from '../data/schema'
import { formatModelName, parseLogOther } from '../lib/format'
import {
  getLogTypeConfig,
  isDisplayableLogType,
  isTimingLogType,
} from '../lib/utils'
import { ModelBadge, ResponseModelDetails } from './model-badge'
import { StreamTpsCell, TimingMetricsCell } from './timing-metrics-cell'
import { useUsageLogsContext } from './usage-logs-provider'

type FieldName =
  | 'model'
  | 'cost'
  | 'user'
  | 'channel'
  | 'token'
  | 'group'
  | 'time'
type LogField = {
  label: string
  value: string
  visible: boolean
  sensitive?: boolean
}

/** Mobile summaries use tappable fields; desktop tooltip cells cannot reveal full text on touch. */
export function CommonLogMobileCard<TData>(props: {
  log: UsageLog
  cells: Map<string, Cell<TData, unknown>>
}) {
  const { t } = useTranslation()
  const context = useUsageLogsContext()
  const [selectedField, setSelectedField] = useState<FieldName | null>(null)
  const log = props.log
  const other = parseLogOther(log.other)
  const displayable = isDisplayableLogType(log.type)
  const timing = isTimingLogType(log.type)
  const model = formatModelName(log)
  const config = getLogTypeConfig(log.type)
  const group = log.group || other?.group || ''
  const groupRatio =
    other?.user_group_ratio != null && other.user_group_ratio !== -1
      ? other.user_group_ratio
      : other?.group_ratio
  const fields: Record<FieldName, LogField> = {
    model: {
      label: t('Model'),
      value: model.name,
      visible: displayable && props.cells.has('model_name') && !!model.name,
    },
    cost: {
      label: t('Cost'),
      value: formatLogQuota(log.quota),
      visible: displayable && props.cells.has('quota'),
    },
    time: {
      label: t('Time'),
      value: formatTimestampToDate(log.created_at),
      visible: props.cells.has('created_at'),
    },
    user: {
      label: t('User'),
      value: log.username,
      visible: props.cells.has('user') && !!log.username,
      sensitive: true,
    },
    channel: {
      label: t('Channel'),
      value: [log.channel_name, `#${log.channel}`].filter(Boolean).join(' '),
      visible: displayable && props.cells.has('channel'),
      sensitive: true,
    },
    token: {
      label: t('Token'),
      value: log.token_name,
      visible: displayable && props.cells.has('token_name') && !!log.token_name,
      sensitive: true,
    },
    group: {
      label: t('Group'),
      value: group,
      visible: displayable && props.cells.has('token_name') && !!group,
      sensitive: true,
    },
  }
  const selected = selectedField ? fields[selectedField] : undefined
  const activeField =
    selected?.visible && (!selected.sensitive || context.sensitiveVisible)
      ? selected
      : undefined
  const metadata: FieldName[] = ['user', 'channel', 'token', 'group']
  const visibleMetadata = metadata.filter((id) => fields[id].visible)
  const costCell = props.cells.get('quota')
  const contentCell = props.cells.get('content')
  const channelCell = props.cells.get('channel')
  const cacheRead = other?.cache_tokens || 0
  const cacheWrite =
    (other?.cache_creation_tokens_5m || 0) +
      (other?.cache_creation_tokens_1h || 0) ||
    other?.cache_creation_tokens ||
    0
  const showTokens =
    displayable &&
    props.cells.has('prompt_tokens') &&
    (log.prompt_tokens > 0 ||
      log.completion_tokens > 0 ||
      cacheRead > 0 ||
      cacheWrite > 0)

  return (
    <div className='min-w-0 space-y-2.5 text-sm leading-5'>
      <div className='flex min-w-0 flex-wrap items-start gap-x-3 gap-y-2'>
        {fields.model.visible && (
          <div className='min-w-0 flex-[1_1_10rem]'>
            <ModelBadge
              modelName={model.name}
              actualModel={model.actualModel}
              responseModel={model.responseModel}
              wrapText
              onInspect={() => setSelectedField('model')}
            />
          </div>
        )}
        {fields.cost.visible && costCell && (
          <div className='ml-auto max-w-full min-w-0 self-center [overflow-wrap:anywhere] [&_.inline-flex]:h-auto [&_.inline-flex]:min-h-6 [&_.inline-flex]:max-w-full [&_.inline-flex]:flex-wrap'>
            {flexRender(costCell.column.columnDef.cell, costCell.getContext())}
          </div>
        )}
      </div>
      <div
        className='grid min-w-0 grid-cols-2 items-stretch gap-x-3'
        data-slot='log-time-and-timing'
      >
        {fields.time.visible && (
          <div className='flex min-w-0 flex-col items-start justify-between gap-1'>
            <StatusBadge
              label={t(config.label)}
              variant={config.color as StatusVariant}
              copyable={false}
              showDot
              className='h-5 px-0 text-xs'
            />
            <Button
              variant='ghost'
              aria-label={`${t('Time')}: ${fields.time.value}`}
              aria-haspopup='dialog'
              onClick={() => setSelectedField('time')}
              className='text-muted-foreground h-auto min-h-6 px-0 py-0 text-xs font-normal whitespace-normal tabular-nums'
            >
              {dayjs.unix(log.created_at).format('MM-DD HH:mm:ss')}
            </Button>
          </div>
        )}
        {timing &&
          (props.cells.has('use_time') || props.cells.has('is_stream')) && (
            <div className='col-start-2 flex min-w-0 flex-col items-end gap-1 [overflow-wrap:anywhere]'>
              {props.cells.has('is_stream') && (
                <StreamTpsCell
                  compact
                  className='min-h-5 max-w-full min-w-0 justify-end'
                  isStream={log.is_stream}
                  isTask={other?.is_task === true}
                  isSyncTask={other?.task_sync === true}
                  tokensPerSecond={
                    log.use_time > 0 && log.completion_tokens > 0
                      ? log.completion_tokens / log.use_time
                      : null
                  }
                  streamStatus={other?.stream_status}
                />
              )}
              {props.cells.has('use_time') && (
                <TimingMetricsCell
                  useTimeSec={log.use_time}
                  completionTokens={log.completion_tokens}
                  frtMs={other?.frt}
                  isStream={log.is_stream}
                  indicator='dot'
                  compact
                  className='min-h-6 max-w-full min-w-0 items-center justify-end [&>div]:justify-end'
                />
              )}
            </div>
          )}
      </div>
      {visibleMetadata.length > 0 && (
        <div className='grid min-w-0 grid-cols-2 gap-x-4 gap-y-0.5'>
          {visibleMetadata.map((id) => {
            const field = fields[id]
            let fieldContent = <span className='truncate'>{field.value}</span>
            if (id === 'group') {
              fieldContent = (
                <GroupBadge
                  group={field.value}
                  type='text'
                  className='max-w-full text-sm'
                />
              )
            } else if (id === 'token') {
              fieldContent = (
                <StatusBadge
                  label={field.value}
                  copyable={false}
                  icon={KeyRound}
                  className='border-border/60 bg-muted/30 text-foreground max-w-full rounded-md border px-1.5 py-0.5 text-sm'
                />
              )
            }
            return (
              <div key={id} className='flex min-w-0 items-center gap-2'>
                <span className='text-muted-foreground max-w-[40%] shrink-0 text-xs [overflow-wrap:anywhere]'>
                  {id === 'user' ? (
                    <Avatar className='ring-border/60 size-6 shrink-0 ring-1'>
                      <AvatarFallback
                        className='text-[11px] font-semibold'
                        style={
                          context.sensitiveVisible
                            ? getUserAvatarStyle(log.username)
                            : undefined
                        }
                      >
                        {context.sensitiveVisible
                          ? getUserAvatarFallback(log.username)
                          : '•'}
                      </AvatarFallback>
                    </Avatar>
                  ) : (
                    field.label
                  )}
                </span>
                {context.sensitiveVisible ? (
                  <Button
                    variant='ghost'
                    aria-label={`${field.label}: ${field.value}`}
                    aria-haspopup='dialog'
                    onClick={() => setSelectedField(id)}
                    className='text-foreground h-auto min-h-8 min-w-0 flex-1 shrink justify-start px-0 py-1 text-left text-sm font-normal'
                  >
                    {fieldContent}
                  </Button>
                ) : (
                  <span className='min-w-0 py-1.5'>••••</span>
                )}
              </div>
            )
          })}
          {groupRatio != null &&
            groupRatio !== 1 &&
            Number.isFinite(groupRatio) &&
            props.cells.has('token_name') && (
              <div className='text-muted-foreground col-span-2 [overflow-wrap:anywhere]'>
                {t('Group Ratio')}: {groupRatio}×
              </div>
            )}
        </div>
      )}
      {showTokens && (
        <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs [overflow-wrap:anywhere]'>
          <span>
            {t('Input')}{' '}
            <span className='text-foreground tabular-nums'>
              {log.prompt_tokens.toLocaleString()}
            </span>
          </span>
          <span>
            {t('Output')}{' '}
            <span className='text-foreground tabular-nums'>
              {log.completion_tokens.toLocaleString()}
            </span>
          </span>
          {cacheRead > 0 && (
            <span>
              {t('Cache')} ↓ {cacheRead.toLocaleString()}
            </span>
          )}
          {cacheWrite > 0 && (
            <span>
              {t('Cache')} ↑ {cacheWrite.toLocaleString()}
            </span>
          )}
        </div>
      )}
      {contentCell && (
        <div className='relative min-w-0 border-t pt-2 [&_button]:min-h-8 [&_button]:w-full [&_button]:max-w-full [&_button]:pr-5 [&_button]:text-sm [&_button>span]:line-clamp-2 [&_button>span]:[overflow-wrap:anywhere] [&_button>span]:whitespace-normal'>
          {flexRender(
            contentCell.column.columnDef.cell,
            contentCell.getContext()
          )}
          <ChevronRight
            aria-hidden='true'
            className='text-primary pointer-events-none absolute top-4 right-0 size-4'
          />
        </div>
      )}
      <Dialog
        open={!!activeField}
        onOpenChange={(open) => {
          if (!open) setSelectedField(null)
        }}
        title={activeField?.label ?? t('Details')}
        contentClassName='max-sm:top-auto max-sm:bottom-0 max-sm:max-h-[85dvh] max-sm:max-w-full max-sm:translate-y-0 max-sm:rounded-b-none max-sm:rounded-t-2xl max-sm:pb-[max(1rem,env(safe-area-inset-bottom))] [&_[data-slot=dialog-close]]:size-11'
        footer={
          activeField && (
            <CopyButton
              value={activeField.value}
              variant='default'
              size='default'
              className='min-h-11 w-full'
            >
              {t('Copy')}
            </CopyButton>
          )
        }
      >
        {activeField && (
          <div className='space-y-4'>
            <p className='bg-muted rounded-lg p-4 text-base [overflow-wrap:anywhere] whitespace-pre-wrap'>
              {activeField.value}
            </p>
            {selectedField === 'model' && model.responseModel && (
              <ResponseModelDetails observation={model.responseModel} />
            )}
            {selectedField === 'model' &&
              !model.responseModel &&
              model.actualModel && (
                <div className='space-y-2'>
                  <p className='text-muted-foreground'>{t('Actual Model')}</p>
                  <p className='text-base [overflow-wrap:anywhere]'>
                    {model.actualModel}
                  </p>
                  <CopyButton value={model.actualModel} />
                </div>
              )}
            {selectedField === 'channel' && channelCell && (
              <div>
                {flexRender(
                  channelCell.column.columnDef.cell,
                  channelCell.getContext()
                )}
              </div>
            )}
            {selectedField === 'user' && (
              <Button
                variant='outline'
                className='min-h-11'
                onClick={() => {
                  setSelectedField(null)
                  context.setSelectedUserId(log.user_id)
                  context.setUserInfoDialogOpen(true)
                }}
              >
                {t('User Information')}
              </Button>
            )}
          </div>
        )}
      </Dialog>
    </div>
  )
}
