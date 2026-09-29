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
import { type ReactNode, useId } from 'react'
import { type UseFormReturn, useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { FieldGroup } from '@/components/ui/field'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { SettingsSwitchField } from '@/features/system-settings/components/settings-form-layout'
import { ChannelHealthSource } from '@/features/system-settings/request-policies/related-policy-link'
import { cn } from '@/lib/utils'

import { CHANNEL_TYPE_TASK_PLUGIN, MODEL_FETCHABLE_TYPES } from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import {
  getHeaderPassthroughState,
  setHeaderPassthrough,
} from '../lib/header-passthrough'
import { supportsResponsesWebSocket } from '../lib/responses-websocket'
import type { PassthroughKind } from './dialogs/passthrough-warning-dialog'

type ChannelQuickOptionsProps = {
  channelType: number
  sensitiveLocked: boolean
  disabled: boolean
  /**
   * `stacked` is the full list with descriptions used inside the form;
   * `inline` is a compact toggle strip for the drawer header on wide screens.
   */
  layout?: 'stacked' | 'inline'
  className?: string
  /** Required when rendered outside the form provider, e.g. in the drawer header. */
  form?: UseFormReturn<ChannelFormValues>
  /**
   * Asks the user to acknowledge the consequences before a passthrough option
   * is switched on. Resolves `false` when they decline; switching off never asks.
   */
  confirmEnablePassthrough: (kind: PassthroughKind) => Promise<boolean>
}

type QuickOption = {
  key: string
  label: string
  description: ReactNode
  checked: boolean
  onCheckedChange: (value: boolean) => void
  disabled: boolean
}

export function ChannelQuickOptions(props: ChannelQuickOptionsProps) {
  const { t } = useTranslation()
  const id = useId()
  const formContext = useFormContext<ChannelFormValues>()
  const form = props.form ?? formContext
  const [passthrough, headerOverride, autoBan, modelCheck, websocket] =
    useWatch({
      control: form.control,
      name: [
        'pass_through_body_enabled',
        'header_override',
        'auto_ban',
        'upstream_model_update_check_enabled',
        'responses_websocket_enabled',
      ],
    })
  const sensitiveDisabled = props.sensitiveLocked || props.disabled
  const setOption = (
    name:
      | 'pass_through_body_enabled'
      | 'upstream_model_update_check_enabled'
      | 'responses_websocket_enabled',
    value: boolean
  ) => form.setValue(name, value, { shouldDirty: true, shouldValidate: true })
  // The header switch is a shortcut for the "*" rule in Request Header
  // Override, so it reads and writes that JSON instead of a separate flag.
  const headerPassthroughState = getHeaderPassthroughState(headerOverride)
  const applyHeaderPassthrough = (value: boolean) =>
    form.setValue(
      'header_override',
      setHeaderPassthrough(form.getValues('header_override'), value),
      { shouldDirty: true, shouldValidate: true }
    )

  const options: QuickOption[] = []
  if (props.channelType !== CHANNEL_TYPE_TASK_PLUGIN) {
    options.push({
      key: 'passthrough',
      label: t('Pass Through Body'),
      description: t(
        'Preserve upstream-specific fields when API formats match; bypasses model redirect, parameter override and format conversion'
      ),
      checked: passthrough === true,
      onCheckedChange: (value) => {
        if (!value) {
          setOption('pass_through_body_enabled', false)
          return
        }
        void props.confirmEnablePassthrough('body').then((confirmed) => {
          if (confirmed) setOption('pass_through_body_enabled', true)
        })
      },
      disabled: sensitiveDisabled,
    })
    options.push({
      key: 'passthrough-headers',
      label: t('Pass Through Request Headers'),
      description:
        headerPassthroughState === 'invalid'
          ? t('Fix the Request Header Override JSON first')
          : t(
              'Enable when the upstream needs client information from Codex or Claude Code headers; may expose client information'
            ),
      checked: headerPassthroughState === 'enabled',
      onCheckedChange: (value) => {
        if (!value) {
          applyHeaderPassthrough(false)
          return
        }
        void props.confirmEnablePassthrough('headers').then((confirmed) => {
          if (confirmed) applyHeaderPassthrough(true)
        })
      },
      disabled: sensitiveDisabled || headerPassthroughState === 'invalid',
    })
  }
  options.push({
    key: 'auto-ban',
    label: t('Auto-disable channel'),
    description: <ChannelHealthSource autoBan={(autoBan ?? 1) === 1} />,
    checked: (autoBan ?? 1) === 1,
    onCheckedChange: (value) =>
      form.setValue('auto_ban', value ? 1 : 0, {
        shouldDirty: true,
        shouldValidate: true,
      }),
    disabled: props.disabled,
  })
  if (MODEL_FETCHABLE_TYPES.has(props.channelType)) {
    options.push({
      key: 'model-check',
      label: t('Detect model updates'),
      description: t('Check for new upstream models'),
      checked: modelCheck === true,
      onCheckedChange: (value) =>
        setOption('upstream_model_update_check_enabled', value),
      disabled: sensitiveDisabled,
    })
  }
  if (supportsResponsesWebSocket(props.channelType)) {
    options.push({
      key: 'websocket',
      label: t('Responses WebSocket'),
      description: t('Requires upstream WebSocket support'),
      checked: websocket === true,
      onCheckedChange: (value) =>
        setOption('responses_websocket_enabled', value),
      disabled: sensitiveDisabled,
    })
  }

  if (props.layout === 'inline') {
    return (
      <div
        role='group'
        aria-label={t('Quick options')}
        className={cn('flex flex-wrap items-center gap-2', props.className)}
      >
        {options.map((option) => (
          <div
            key={option.key}
            title={
              typeof option.description === 'string'
                ? option.description
                : undefined
            }
            className='border-border/60 bg-muted/30 flex items-center gap-2 rounded-md border px-2.5 py-1.5'
          >
            <Switch
              id={`${id}-${option.key}`}
              size='sm'
              checked={option.checked}
              onCheckedChange={option.onCheckedChange}
              disabled={option.disabled}
            />
            <Label
              htmlFor={`${id}-${option.key}`}
              className='cursor-pointer text-xs font-medium'
            >
              {option.label}
            </Label>
          </div>
        ))}
      </div>
    )
  }

  return (
    <section
      role='group'
      aria-labelledby={`${id}-title`}
      className={cn(
        'border-border/60 @container border-t px-4 pt-4 pb-2',
        props.className
      )}
    >
      <h3 id={`${id}-title`} className='mb-1 text-sm font-semibold'>
        {t('Quick options')}
      </h3>
      <FieldGroup className='grid gap-x-6 gap-y-0 @sm:grid-cols-2'>
        {options.map((option) => (
          <SettingsSwitchField
            key={option.key}
            controlId={`${id}-${option.key}`}
            checked={option.checked}
            onCheckedChange={option.onCheckedChange}
            label={option.label}
            description={option.description}
            disabled={option.disabled}
          />
        ))}
      </FieldGroup>
    </section>
  )
}
