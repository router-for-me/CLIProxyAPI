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
import { AlertTriangle, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { getLobeIcon } from '@/lib/lobe-icon'
import { resolveModelProvider } from '@/lib/model-provider'
import { cn } from '@/lib/utils'

import { isResponseModelMismatch } from '../lib/response-model'
import type { LogOtherData } from '../types'
import { DetailRow } from './dialogs/log-detail-layout'

interface ModelBadgeProps {
  modelName: string
  actualModel?: string
  responseModel?: LogOtherData['response_model']
  className?: string
  wrapText?: boolean
  onInspect?: () => void
}

function ModelBadgeContent(props: ModelBadgeProps & { copyable: boolean }) {
  const provider = resolveModelProvider(props.modelName)

  return (
    <StatusBadge
      copyText={props.modelName}
      copyable={props.copyable}
      size='sm'
      showDot={!provider?.icon}
      autoColor={provider?.icon ? undefined : props.modelName}
      className={cn(
        'border-border/60 bg-muted/30 h-6 max-w-none gap-1.5 rounded-md border px-2 [font-family:var(--font-body)]',
        provider?.icon && 'text-foreground',
        props.wrapText && 'h-auto min-h-6 max-w-full py-px whitespace-normal',
        props.className
      )}
    >
      <span
        className={cn(
          'flex items-center gap-1.5',
          props.wrapText ? 'max-w-full min-w-0' : 'max-w-none'
        )}
      >
        {provider?.icon && (
          <span
            className='flex h-[18px] w-[18px] shrink-0 items-center justify-center'
            title={provider.label ?? provider.name}
            aria-label={provider.label ?? provider.name}
          >
            {getLobeIcon(provider.icon, 18)}
          </span>
        )}
        <span
          className={
            props.wrapText
              ? 'line-clamp-2 leading-5 [overflow-wrap:anywhere]'
              : 'whitespace-nowrap'
          }
        >
          {props.modelName}
        </span>
      </span>
    </StatusBadge>
  )
}

export function ModelBadge(props: ModelBadgeProps) {
  const { t } = useTranslation()
  const mismatch = isResponseModelMismatch(props.responseModel)
  const responseModelLabel =
    mismatch && props.responseModel
      ? t('Response model: {{model}}', {
          model: props.responseModel.returned_model,
        })
      : ''
  const modelLabel = `${t('Model')}: ${props.modelName}${responseModelLabel ? `, ${responseModelLabel}` : ''}`
  const hasDetails =
    !!props.actualModel ||
    !!(
      props.responseModel &&
      (mismatch ||
        props.responseModel.returned_model !==
          props.responseModel.requested_model ||
        (props.responseModel.upstream_model &&
          props.responseModel.upstream_model !==
            props.responseModel.requested_model))
    )

  if (!hasDetails) {
    if (props.onInspect) {
      return (
        <CopyButton
          value={props.modelName}
          aria-label={modelLabel}
          size='sm'
          iconClassName='hidden'
          className='h-auto min-h-8 max-w-full min-w-0 justify-start px-0 py-0 text-left font-normal whitespace-normal'
        >
          <ModelBadgeContent {...props} copyable={false} />
        </CopyButton>
      )
    }
    return <ModelBadgeContent {...props} copyable />
  }

  const content = (
    <>
      <ModelBadgeContent {...props} copyable={false} />
      {mismatch && (
        <StatusBadge
          icon={AlertTriangle}
          label={responseModelLabel}
          variant='warning'
          copyable={false}
        />
      )}
      {!mismatch && props.actualModel && (
        <Route
          className='text-muted-foreground size-3 shrink-0'
          aria-hidden='true'
        />
      )}
    </>
  )

  if (props.onInspect) {
    return (
      <Button
        variant='ghost'
        aria-label={modelLabel}
        aria-haspopup='dialog'
        onClick={props.onInspect}
        className='h-auto min-h-8 max-w-full min-w-0 flex-wrap justify-start gap-1 px-0 py-0 text-left font-normal whitespace-normal'
      >
        {content}
      </Button>
    )
  }

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant='ghost'
            aria-label={modelLabel}
            className='h-auto max-w-full min-w-0 flex-wrap justify-start gap-1 p-0 font-normal'
          />
        }
      >
        {content}
      </PopoverTrigger>
      <PopoverContent className='w-96 max-w-[calc(100vw-2rem)]'>
        {props.responseModel ? (
          <ResponseModelDetails observation={props.responseModel} />
        ) : (
          <div className='space-y-2'>
            <div className='flex items-start justify-between gap-3'>
              <span className='text-muted-foreground text-xs'>
                {t('Request Model:')}
              </span>
              <span className='truncate font-mono text-xs font-medium'>
                {props.modelName}
              </span>
            </div>
            <div className='flex items-start justify-between gap-3'>
              <span className='text-muted-foreground text-xs'>
                {t('Actual Model:')}
              </span>
              <span className='truncate font-mono text-xs font-medium'>
                {props.actualModel}
              </span>
            </div>
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}

export function ResponseModelDetails(props: {
  observation: NonNullable<LogOtherData['response_model']>
}) {
  const { t } = useTranslation()
  const mismatch = isResponseModelMismatch(props.observation)

  return (
    <div className='min-w-0 space-y-2'>
      {mismatch && (
        <StatusBadge
          icon={AlertTriangle}
          label={t('Response model: {{model}}', {
            model: props.observation.returned_model,
          })}
          variant='warning'
          copyable={false}
          className='h-auto whitespace-normal'
        />
      )}
      <DetailRow
        label={t('Request Model')}
        value={props.observation.requested_model}
        mono
      />
      <DetailRow
        label={t('Upstream Model')}
        value={
          props.observation.upstream_model || props.observation.requested_model
        }
        mono
      />
      <DetailRow
        label={t('Response Model')}
        value={props.observation.returned_model}
        mono
      />
      {mismatch && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'The upstream returned a model name different from both the requested and upstream models. Aliases or dated versions may also cause this; this warning alone does not prove model substitution.'
          )}
        </p>
      )}
    </div>
  )
}
