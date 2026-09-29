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
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { JsonCodeEditor } from '@/components/json-code-editor'
import { JsonEditor } from '@/components/json-editor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  formatPricingAmount,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { useBillingTime } from '@/features/pricing/hooks/use-billing-time'
import { formatBillingCondition } from '@/features/pricing/lib/billing-expression/condition-display'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { evaluateBillingExpression } from '@/features/pricing/lib/billing-expression/runtime'
import type {
  BillingSimulationContext,
  DiagnosticCode,
} from '@/features/pricing/lib/billing-expression/types'
import { taskPriceLabel } from '@/features/pricing/lib/task-price-display'
import type { BillingUsageSchema } from '@/features/pricing/types'

const DIAGNOSTIC_LABELS: Record<DiagnosticCode, string> = {
  syntax: 'Invalid expression syntax',
  unsupported: 'This expression cannot be simulated in the browser.',
  missing_context: 'Simulation context is missing.',
  type: 'Expression argument types do not match.',
  number: 'Billing values must be finite and non-negative.',
  limit: 'This expression exceeds the browser simulation limits.',
}

type RequestSimulationProps = {
  expression: string
  tokens?: BillingSimulationContext['tokens']
  usage?: BillingSimulationContext['usage']
  currency?: PricingCurrency
  usageSchema?: BillingUsageSchema
  mode: 'token' | 'task'
}

/** Explicitly opting in supplies an empty request; ordinary price displays never do. */
export function RequestSimulation(props: RequestSimulationProps) {
  const { t, i18n } = useTranslation()
  const bodyId = useId()
  const headersId = useId()
  const timeId = useId()
  const imageCountId = useId()
  const [imageCount, setImageCount] = useState('1')
  const compiled = useMemo(
    () => compileBillingExpression(props.expression),
    [props.expression]
  )
  const usesImageCount =
    compiled.status === 'ready' && compiled.variables.has('image_count')
  const booleanFields = Object.entries(props.usageSchema ?? {}).filter(
    ([, field]) => field.type === 'boolean'
  )
  const [booleanInputs, setBooleanInputs] = useState<{
    sample: BillingSimulationContext['usage']
    values: Record<string, boolean>
  }>({ sample: props.usage, values: {} })
  const usage = useMemo(() => {
    if (!props.usage) return undefined
    return {
      ...props.usage,
      ...(booleanInputs.sample === props.usage ? booleanInputs.values : {}),
    }
  }, [props.usage, booleanInputs])
  const [open, setOpen] = useState(false)
  const [body, setBody] = useState('{}')
  const [headers, setHeaders] = useState('{}')
  const [timeMode, setTimeMode] = useState('current')
  const [fixedTime, setFixedTime] = useState(() => new Date().toISOString())
  const liveTime = useBillingTime(
    props.expression,
    open && timeMode === 'current'
  )

  const result = useMemo(() => {
    if (!open) return null
    let requestBody: unknown
    let requestHeaders: unknown
    try {
      requestBody = JSON.parse(body)
    } catch {
      return { inputError: 'Request body must be a JSON object.' }
    }
    if (
      requestBody === null ||
      typeof requestBody !== 'object' ||
      Array.isArray(requestBody)
    ) {
      return { inputError: 'Request body must be a JSON object.' }
    }
    try {
      requestHeaders = JSON.parse(headers.trim() || '{}')
    } catch {
      return {
        inputError: 'Request headers must be a JSON object with string values.',
      }
    }
    if (
      requestHeaders === null ||
      typeof requestHeaders !== 'object' ||
      Array.isArray(requestHeaders) ||
      Object.values(requestHeaders).some((value) => typeof value !== 'string')
    ) {
      return {
        inputError: 'Request headers must be a JSON object with string values.',
      }
    }
    let now = liveTime === undefined ? new Date() : new Date(liveTime)
    if (timeMode === 'fixed') {
      if (
        !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2})$/.test(
          fixedTime
        )
      ) {
        return {
          inputError: 'Enter an ISO date and time with a timezone offset.',
        }
      }
      now = new Date(fixedTime)
      const calendarDate = new Date(`${fixedTime.slice(0, 10)}T00:00:00Z`)
      if (
        !Number.isFinite(calendarDate.getTime()) ||
        calendarDate.toISOString().slice(0, 10) !== fixedTime.slice(0, 10) ||
        !Number.isFinite(now.getTime())
      ) {
        return {
          inputError: 'Enter an ISO date and time with a timezone offset.',
        }
      }
    }
    for (const [field, schema] of Object.entries(props.usageSchema ?? {})) {
      if (schema.type === 'boolean' && typeof usage?.[field] !== 'boolean') {
        return { inputError: 'Simulation context is missing.' }
      }
    }
    return evaluateBillingExpression(props.expression, {
      imageCount: Number(imageCount),
      tokens: props.tokens,
      usage,
      now,
      request: {
        body: requestBody,
        headers: requestHeaders as Record<string, string>,
      },
    })
  }, [
    open,
    imageCount,
    body,
    headers,
    timeMode,
    fixedTime,
    liveTime,
    props.expression,
    props.tokens,
    usage,
    props.usageSchema,
  ])

  let error = ''
  if (result && 'inputError' in result) error = t(result.inputError)
  else if (result && result.status !== 'success') {
    error = `${t(DIAGNOSTIC_LABELS[result.diagnostic.code])} ${result.diagnostic.detail}`
  }
  const success =
    result && 'status' in result && result.status === 'success' ? result : null

  const occurrences = new Map<string, number>()
  const ruleRows = (success?.requestRules ?? []).map((rule) => {
    const base = `${rule.cond}:${rule.multiplier}`
    const occurrence = occurrences.get(base) ?? 0
    occurrences.set(base, occurrence + 1)
    return { ...rule, key: `${base}:${occurrence}` }
  })

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className='rounded-md border p-3'
    >
      <CollapsibleTrigger
        render={<Button type='button' variant='outline' size='sm' />}
      >
        {t('Request simulation')}
      </CollapsibleTrigger>
      <CollapsibleContent className='mt-3 space-y-4'>
        {usesImageCount && (
          <Field>
            <FieldLabel htmlFor={imageCountId}>
              {t('Billable image count')}
            </FieldLabel>
            <Input
              id={imageCountId}
              type='number'
              min={1}
              max={128}
              step={1}
              value={imageCount}
              onChange={(event) => setImageCount(event.target.value)}
            />
            <FieldDescription>
              {t('Reserve requested images; settle returned images.')}
            </FieldDescription>
          </Field>
        )}
        <p className='text-muted-foreground text-xs'>
          {t(
            'Simulate a request including request rules and excluding group multipliers. Empty objects represent an empty request.'
          )}
        </p>
        {booleanFields.map(([field, schema]) => {
          const label = taskPriceLabel(schema.description, field, i18n.language)
          return (
            <Field key={field}>
              <FieldLabel>{label}</FieldLabel>
              <Select
                value={
                  typeof usage?.[field] === 'boolean'
                    ? String(usage[field])
                    : null
                }
                onValueChange={(value) => {
                  if (value === null) return
                  setBooleanInputs({
                    sample: props.usage,
                    values: {
                      ...(booleanInputs.sample === props.usage
                        ? booleanInputs.values
                        : {}),
                      [field]: value === 'true',
                    },
                  })
                }}
                items={[
                  { value: 'true', label: t('Yes') },
                  { value: 'false', label: t('No') },
                ]}
              >
                <SelectTrigger aria-label={label}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='true'>{t('Yes')}</SelectItem>
                  <SelectItem value='false'>{t('No')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )
        })}
        <div className='grid gap-4 sm:grid-cols-2'>
          <Field>
            <FieldLabel htmlFor={bodyId}>
              {t('Simulated request body')}
            </FieldLabel>
            <JsonCodeEditor
              id={bodyId}
              value={body}
              onChange={setBody}
              ariaLabel={t('Simulated request body')}
              heightClassName='h-40 min-h-40 max-h-40'
            />
          </Field>
          <div role='group' aria-labelledby={headersId} className='space-y-2'>
            <div id={headersId} className='text-sm font-medium'>
              {t('Simulated request headers')}
            </div>
            <JsonEditor
              value={headers}
              onChange={setHeaders}
              valueType='string'
              keyPlaceholder={t('Header')}
              valuePlaceholder={t('Value')}
            />
          </div>
        </div>
        <Field>
          <FieldLabel htmlFor={timeId}>{t('Simulation time')}</FieldLabel>
          <Select
            value={timeMode}
            onValueChange={(value) => value && setTimeMode(value)}
            items={[
              { value: 'current', label: t('Current time') },
              { value: 'fixed', label: t('Specified time') },
            ]}
          >
            <SelectTrigger id={timeId} className='w-full sm:w-56'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='current'>{t('Current time')}</SelectItem>
              <SelectItem value='fixed'>{t('Specified time')}</SelectItem>
            </SelectContent>
          </Select>
          {timeMode === 'fixed' && (
            <Input
              aria-label={t('Specified time')}
              value={fixedTime}
              onChange={(event) => setFixedTime(event.target.value)}
              placeholder='2026-09-07T09:00:00+08:00'
              className='font-mono'
            />
          )}
          <FieldDescription>
            {t('Time functions use the timezone written in the expression.')}
          </FieldDescription>
        </Field>
        {error && (
          <p role='alert' className='text-destructive text-sm'>
            {error}
          </p>
        )}
        {success && (
          <div
            role='status'
            className='bg-muted/40 space-y-2 rounded-md border p-3 text-sm'
          >
            <p className='font-medium'>
              {t('Simulated request cost')}:{' '}
              {formatPricingAmount(
                success.cost / (props.mode === 'token' ? 1_000_000 : 1),
                props.currency
              )}
              {success.billingUnit === 'request' && `/${t('request')}`}
            </p>
            {success.matchedTier && (
              <Badge variant='outline'>
                {t('Hit tier')}: {success.matchedTier}
              </Badge>
            )}
            {success.requestRules.length > 0 && (
              <ul className='space-y-1'>
                {ruleRows.map((rule) => (
                  <li key={rule.key} className='text-xs break-words'>
                    <span>
                      {t(rule.matched ? 'Matched' : 'Not matched')} · ×
                      {rule.multiplier}
                    </span>{' '}
                    <span>
                      {formatBillingCondition(rule.cond, t, i18n.language) ??
                        rule.cond}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}
