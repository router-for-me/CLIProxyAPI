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
import { AlertTriangle } from 'lucide-react'
import { memo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import {
  formatPricingAmount,
  USD_PRICING_CURRENCY,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'
import {
  getTaskUsagePriceUnitLabelKey,
  getTaskUsageQuantityUnitLabelKey,
} from '@/features/pricing/lib/dynamic-price'
import {
  createDefaultTaskMatrixConfig,
  evaluateTaskVisualConfig,
  generateTaskExprFromConfig,
  getTaskEnumCombinations,
  getTaskEnumFields,
  getTaskNumberFields,
  taskMatrixToTiers,
  tryParseTaskMatrixConfig,
  tryParseTaskVisualConfig,
  type TaskMatrixRow,
  type TaskVisualConfig,
} from '@/features/pricing/lib/task-expr'
import {
  taskPriceLabel,
  taskUsageUnitLabel,
  taskEnumLabel,
  taskPricingConditions,
} from '@/features/pricing/lib/task-price-display'
import type {
  BillingUsageExample,
  BillingUsageSchema,
} from '@/features/pricing/types'

import { formatPricingNumber } from './pricing-format'
import { RequestSimulation } from './request-simulation'
import { TaskPricingMatrix } from './task-pricing-matrix'

type TaskUsagePricingEditorProps = {
  currency?: PricingCurrency
  billingExpr: string
  requestRuleExpr: string
  usageSchema: BillingUsageSchema
  usageExamples?: BillingUsageExample[]
  onBillingExprChange: (next: string) => void
  onRequestRuleExprChange: (next: string) => void
}

type EditorMode = 'visual' | 'raw'

type TaskBillingPreviewProps = {
  currency?: PricingCurrency
  config: TaskVisualConfig | null
  requestRuleExpr: string
  sample: Record<string, number | string>
  usageSchema: BillingUsageSchema
  usageExamples?: BillingUsageExample[]
  onSampleChange: (field: string, value: number | string) => void
  onSampleReplace: (sample: Record<string, number | string>) => void
}

function TaskBillingPreview(props: TaskBillingPreviewProps) {
  const { t, i18n } = useTranslation()
  const enumFields = getTaskEnumFields(props.usageSchema)
  const numberFields = getTaskNumberFields(props.usageSchema)
  const result = props.config
    ? evaluateTaskVisualConfig(props.config, props.sample, props.usageSchema)
    : null

  const formulaParts = (result?.parts ?? []).map((part) => {
    if (part.kind === 'constant') {
      return `${t('Additional charge')}: ${formatPricingAmount(part.amount, props.currency)}`
    }

    const definition = props.usageSchema[part.field ?? '']
    const quantityUnitKey = getTaskUsageQuantityUnitLabelKey(definition?.unit)
    const priceUnitKey = getTaskUsagePriceUnitLabelKey(definition?.unit)
    const quantityUnitLabel = taskUsageUnitLabel(
      definition,
      i18n.language,
      t(quantityUnitKey)
    )
    const priceUnitLabel = taskUsageUnitLabel(
      definition,
      i18n.language,
      t(priceUnitKey)
    )
    const quantityLabel =
      definition?.unit === 'second'
        ? `${formatPricingNumber(part.quantity)}${quantityUnitLabel}`
        : `${formatPricingNumber(part.quantity)} ${quantityUnitLabel}`
    return `${taskPriceLabel(definition?.description, part.field ?? '', i18n.language)}: ${quantityLabel} × ${formatPricingAmount(part.unitPrice ?? 0, props.currency)}/${priceUnitLabel}`
  })
  const formulaLeft =
    formulaParts.length > 0
      ? formulaParts.join(' + ')
      : formatPricingAmount(0, props.currency)
  const formula = `${formulaLeft} = ${formatPricingAmount(result?.total ?? 0, props.currency)}`

  return (
    <div className='bg-muted/30 flex flex-col gap-3 rounded-md border p-3'>
      <div className='flex flex-col gap-1'>
        <h4 className='text-sm font-medium'>{t('Cost calculator')}</h4>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Enter sample usage to estimate the cost. Group and request multipliers are not included.'
          )}
          {props.requestRuleExpr ? (
            <> {t('Request rules apply on top of this amount.')}</>
          ) : null}
        </p>
      </div>
      {props.usageExamples && props.usageExamples.length > 0 ? (
        <Field className='gap-1.5'>
          <FieldLabel>{t('Example spec')}</FieldLabel>
          <Combobox
            options={props.usageExamples.map((example) => ({
              value: example.label,
              label: example.label,
            }))}
            value={
              props.usageExamples.find((example) =>
                Object.entries(example.facts).every(
                  ([field, value]) => props.sample[field] === value
                )
              )?.label ?? null
            }
            onValueChange={(label) => {
              const example = props.usageExamples?.find(
                (item) => item.label === label
              )
              if (example) props.onSampleReplace({ ...example.facts })
            }}
            className='w-full'
            placeholder={t('Example spec')}
          />
        </Field>
      ) : null}
      {enumFields.length + numberFields.length > 0 ? (
        <div className='grid gap-3 sm:grid-cols-2'>
          {enumFields.map(([field, definition]) => {
            const items = (definition.enum ?? []).map((value) => ({
              value,
              label: taskEnumLabel(definition, value, i18n.language),
            }))
            return (
              <Field key={field} className='gap-1.5'>
                <FieldLabel>
                  {taskPriceLabel(definition.description, field, i18n.language)}
                </FieldLabel>
                <Combobox
                  aria-label={taskPriceLabel(
                    definition.description,
                    field,
                    i18n.language
                  )}
                  options={items}
                  value={String(props.sample[field] ?? '')}
                  onValueChange={(value) =>
                    value !== null && props.onSampleChange(field, value)
                  }
                  className='w-full'
                />
              </Field>
            )
          })}
          {numberFields.map(([field, definition]) => (
            <Field key={field} className='gap-1.5'>
              <FieldLabel>
                {t('Usage · {{price}}', {
                  price: taskPriceLabel(
                    definition.description,
                    field,
                    i18n.language
                  ),
                })}
              </FieldLabel>
              <div className='flex items-center gap-2'>
                <Input
                  type='number'
                  aria-label={t('Usage · {{price}}', {
                    price: taskPriceLabel(
                      definition.description,
                      field,
                      i18n.language
                    ),
                  })}
                  min={0}
                  step={1}
                  value={props.sample[field] ?? 0}
                  onChange={(event) => {
                    const value = Number(event.target.value)
                    props.onSampleChange(
                      field,
                      Number.isFinite(value) && value >= 0 ? value : 0
                    )
                  }}
                  className='font-mono'
                />
                <span className='text-muted-foreground shrink-0 text-xs'>
                  {taskUsageUnitLabel(
                    definition,
                    i18n.language,
                    t(getTaskUsageQuantityUnitLabelKey(definition.unit))
                  )}
                </span>
              </div>
            </Field>
          ))}
        </div>
      ) : null}
      {result ? (
        <div className='border-primary/50 bg-primary/10 flex flex-col gap-2 rounded-md border p-3 text-sm'>
          <Badge variant='outline' className='text-xs'>
            {t('Current pricing conditions')}:{' '}
            {taskPricingConditions(
              enumFields.map(([field]) => ({
                field,
                value: String(props.sample[field] ?? ''),
              })),
              props.usageSchema,
              i18n.language,
              t
            ) || t('All requests')}
          </Badge>
          <code className='font-mono text-xs break-words'>{formula}</code>
        </div>
      ) : (
        <p className='text-muted-foreground text-xs'>
          {t('Preview is unavailable for custom expressions.')}
        </p>
      )}
    </div>
  )
}

export const TaskUsagePricingEditor = memo(function TaskUsagePricingEditor(
  props: TaskUsagePricingEditorProps
) {
  const { t, i18n } = useTranslation()
  const [editorMode, setEditorMode] = useState<EditorMode>(() =>
    props.billingExpr &&
    !tryParseTaskMatrixConfig(props.billingExpr, props.usageSchema)
      ? 'raw'
      : 'visual'
  )
  const [matrixRows, setMatrixRows] = useState<TaskMatrixRow[]>(() => {
    const parsed = tryParseTaskMatrixConfig(
      props.billingExpr,
      props.usageSchema
    )
    return (parsed ?? createDefaultTaskMatrixConfig(props.usageSchema)).rows
  })
  const [confirmVisualSwitch, setConfirmVisualSwitch] = useState(false)
  const [rawExpr, setRawExpr] = useState(() =>
    combineBillingExpr(props.billingExpr, props.requestRuleExpr)
  )
  const [previewSample, setPreviewSample] = useState<
    Record<string, number | string>
  >(() => {
    if (props.usageExamples?.[0]) {
      return { ...props.usageExamples[0].facts }
    }
    const sample: Record<string, number | string> = {}
    for (const [field, definition] of getTaskEnumFields(props.usageSchema)) {
      sample[field] = definition.enum?.[0] ?? ''
    }
    for (const [field, definition] of getTaskNumberFields(props.usageSchema)) {
      sample[field] = definition.unit === 'second' ? 5 : 1
    }
    return sample
  })
  const enumFields = getTaskEnumFields(props.usageSchema)
  const numberFields = getTaskNumberFields(props.usageSchema)
  const combinations = getTaskEnumCombinations(props.usageSchema)
  const visualTiers = taskMatrixToTiers({ rows: matrixRows }, props.usageSchema)

  let previewConfig: TaskVisualConfig | null = null
  let previewRequestRuleExpr = props.requestRuleExpr
  let matchedRowIndex: number | null = null
  if (editorMode === 'visual') {
    const generatedExpression = generateTaskExprFromConfig(
      { tiers: visualTiers },
      props.usageSchema
    )
    if (generatedExpression) previewConfig = { tiers: visualTiers }
    const nextMatchedRowIndex = combinations.findIndex((combination) =>
      Object.entries(combination).every(
        ([field, value]) => previewSample[field] === value
      )
    )
    if (nextMatchedRowIndex >= 0) {
      matchedRowIndex = nextMatchedRowIndex
    }
  } else {
    const split = splitBillingExprAndRequestRules(rawExpr)
    previewConfig = tryParseTaskVisualConfig(
      split.billingExpr,
      props.usageSchema
    )
    previewRequestRuleExpr = split.requestRuleExpr
  }

  const publishRows = (nextRows: TaskMatrixRow[]) => {
    setMatrixRows(nextRows)
    props.onBillingExprChange(
      generateTaskExprFromConfig(
        {
          tiers: taskMatrixToTiers({ rows: nextRows }, props.usageSchema),
        },
        props.usageSchema
      )
    )
  }

  const handleRowChange = (index: number, next: TaskMatrixRow) => {
    const nextRows = [...matrixRows]
    nextRows[index] = next
    publishRows(nextRows)
  }

  const handleFillColumn = (priceKey: string, value: number) => {
    const nextRows = matrixRows.map((row) => {
      if (priceKey === 'constant') return { ...row, constant: value }
      return {
        ...row,
        unitPrices: { ...row.unitPrices, [priceKey]: value },
      }
    })
    publishRows(nextRows)
  }

  const handleRawChange = (value: string) => {
    setRawExpr(value)
    const split = splitBillingExprAndRequestRules(value)
    props.onBillingExprChange(split.billingExpr)
    props.onRequestRuleExprChange(split.requestRuleExpr)
  }

  const rawSplit = splitBillingExprAndRequestRules(rawExpr)
  const rawMatrix = tryParseTaskMatrixConfig(
    rawSplit.billingExpr,
    props.usageSchema
  )

  const handleModeChange = (nextMode: EditorMode) => {
    if (nextMode === editorMode) return
    if (nextMode === 'visual') {
      setConfirmVisualSwitch(true)
      return
    }
    setRawExpr(combineBillingExpr(props.billingExpr, props.requestRuleExpr))
    setEditorMode('raw')
  }

  const handleConfirmVisualSwitch = () => {
    const nextRows = (
      rawMatrix ?? createDefaultTaskMatrixConfig(props.usageSchema)
    ).rows
    setMatrixRows(nextRows)
    props.onBillingExprChange(
      generateTaskExprFromConfig(
        { tiers: taskMatrixToTiers({ rows: nextRows }, props.usageSchema) },
        props.usageSchema
      )
    )
    props.onRequestRuleExprChange(rawMatrix ? rawSplit.requestRuleExpr : '')
    setConfirmVisualSwitch(false)
    setEditorMode('visual')
  }

  const handlePreviewSampleChange = (field: string, value: number | string) => {
    setPreviewSample((current) => ({ ...current, [field]: value }))
  }

  const allRowsFree = matrixRows.every(
    (row) =>
      row.constant === 0 &&
      numberFields.every(([field]) => !(row.unitPrices[field] > 0))
  )

  return (
    <div className='space-y-5'>
      <ConfirmDialog
        open={confirmVisualSwitch}
        onOpenChange={setConfirmVisualSwitch}
        title={t('Switch to visual pricing?')}
        desc={
          rawMatrix
            ? t(
                'Switching regenerates the expression from the price table and replaces its original formatting and tier names. Changes apply only after saving.'
              )
            : t(
                'This expression cannot be represented by the price table. Switching discards the entire expression, including request rules, and resets all prices to zero. Configure prices before saving.'
              )
        }
        confirmText={t('Switch to visual editor')}
        destructive={!rawMatrix}
        handleConfirm={handleConfirmVisualSwitch}
      />
      <div className='grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end'>
        <Field className='gap-2'>
          <FieldLabel>{t('Editor mode')}</FieldLabel>
          <Select
            items={[
              { value: 'visual', label: t('Visual editor') },
              { value: 'raw', label: t('Expression editor') },
            ]}
            value={editorMode}
            onValueChange={(value) =>
              value !== null && handleModeChange(value as EditorMode)
            }
          >
            <SelectTrigger
              aria-label={t('Editor mode')}
              className='w-full sm:w-56'
              size='sm'
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                <SelectItem value='visual'>{t('Visual editor')}</SelectItem>
                <SelectItem value='raw'>{t('Expression editor')}</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
      </div>

      <Alert>
        <AlertDescription className='text-xs'>
          {t(
            'Prices are in {{currency}}, with units shown below. Use USD when editing expressions directly.',
            { currency: (props.currency ?? USD_PRICING_CURRENCY).label }
          )}
        </AlertDescription>
      </Alert>

      <div className='bg-muted/30 space-y-3 rounded-md border p-3'>
        {editorMode === 'visual' ? (
          <>
            {enumFields.length > 0 ? (
              <div className='flex flex-col gap-3'>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Set prices for each set of conditions below. Cost = usage × unit price + additional charge. Token prices are per million tokens.'
                  )}
                </p>
                <TaskPricingMatrix
                  currency={props.currency}
                  rows={matrixRows}
                  usageSchema={props.usageSchema}
                  matchedRowIndex={matchedRowIndex}
                  onRowChange={handleRowChange}
                  onFillColumn={handleFillColumn}
                />
              </div>
            ) : (
              <>
                {allRowsFree ? (
                  <Alert className='border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-200'>
                    <AlertTriangle aria-hidden='true' />
                    <AlertDescription className='text-xs text-current'>
                      {t(
                        'All combinations are priced at zero. Matching requests will be billed as free.'
                      )}
                    </AlertDescription>
                  </Alert>
                ) : null}
                <div className='flex flex-col gap-4'>
                  <div className='flex flex-col gap-2'>
                    <Label className='text-xs font-medium'>
                      {t('Usage prices')}
                    </Label>
                    <div className='grid gap-3 sm:grid-cols-2'>
                      {numberFields.map(([field, definition]) => {
                        const description = taskPriceLabel(
                          definition.description,
                          field,
                          i18n.language
                        )
                        return (
                          <Field key={field} className='gap-1.5'>
                            <FieldLabel>{description}</FieldLabel>
                            <div className='flex items-center gap-2'>
                              <PricingAmountInput
                                currency={props.currency}
                                aria-label={field}
                                min={0}
                                step={0.000001}
                                value={matrixRows[0].unitPrices[field] ?? 0}
                                onFocus={(event) => {
                                  if (Number(event.currentTarget.value) === 0) {
                                    event.currentTarget.select()
                                  }
                                }}
                                onChange={(usd) => {
                                  const value = Number(usd)
                                  handleRowChange(0, {
                                    ...matrixRows[0],
                                    unitPrices: {
                                      ...matrixRows[0].unitPrices,
                                      [field]:
                                        Number.isFinite(value) && value >= 0
                                          ? value
                                          : 0,
                                    },
                                  })
                                }}
                                className='font-mono'
                              />
                              <span className='text-muted-foreground shrink-0 text-xs'>
                                {
                                  (props.currency ?? USD_PRICING_CURRENCY)
                                    .symbol
                                }
                                /
                                {taskUsageUnitLabel(
                                  definition,
                                  i18n.language,
                                  t(
                                    getTaskUsagePriceUnitLabelKey(
                                      definition.unit
                                    )
                                  )
                                )}
                              </span>
                            </div>
                          </Field>
                        )
                      })}
                      <Field className='gap-1.5'>
                        <FieldLabel>{t('Additional charge')}</FieldLabel>
                        <div className='flex items-center gap-2'>
                          <PricingAmountInput
                            currency={props.currency}
                            aria-label={t('Additional charge')}
                            min={0}
                            step={0.000001}
                            value={matrixRows[0].constant}
                            onFocus={(event) => {
                              if (Number(event.currentTarget.value) === 0) {
                                event.currentTarget.select()
                              }
                            }}
                            onChange={(usd) => {
                              const value = Number(usd)
                              handleRowChange(0, {
                                ...matrixRows[0],
                                constant:
                                  Number.isFinite(value) && value >= 0
                                    ? value
                                    : 0,
                              })
                            }}
                            className='font-mono'
                          />
                          <span className='text-muted-foreground shrink-0 text-xs'>
                            {(props.currency ?? USD_PRICING_CURRENCY).symbol}/
                            {t('request')}
                          </span>
                        </div>
                      </Field>
                    </div>
                  </div>
                </div>
              </>
            )}

            <TaskBillingPreview
              currency={props.currency}
              config={previewConfig}
              requestRuleExpr={previewRequestRuleExpr}
              sample={previewSample}
              usageSchema={props.usageSchema}
              usageExamples={props.usageExamples}
              onSampleChange={handlePreviewSampleChange}
              onSampleReplace={setPreviewSample}
            />

            <Field className='gap-2 border-t pt-3'>
              <FieldLabel>{t('Request rule pricing')}</FieldLabel>
              <Textarea
                value={props.requestRuleExpr}
                onChange={(event) =>
                  props.onRequestRuleExprChange(event.target.value)
                }
                placeholder='(header("x-priority") == "high" ? 2 : 1)'
                rows={3}
                className='font-mono text-xs'
                spellCheck={false}
              />
              <FieldDescription>
                {t(
                  'Optional request-rule multiplier expression. Leave empty when no request rule applies.'
                )}
              </FieldDescription>
            </Field>
          </>
        ) : (
          <div className='space-y-3'>
            <Alert>
              <AlertDescription className='space-y-1 text-xs'>
                <div>
                  {t('Usage parameters')}:{' '}
                  {Object.keys(props.usageSchema)
                    .sort((left, right) => left.localeCompare(right))
                    .map((field) => `u(${JSON.stringify(field)})`)
                    .join(', ')}
                </div>
                <div>
                  {t('Functions')}: <code>tier(name, value)</code>,{' '}
                  <code>header(name)</code>, <code>param(path)</code>
                </div>
                {!rawMatrix ? (
                  <div>
                    {t(
                      'This expression cannot be represented by the price table. You can switch to visual editing by confirming that the expression will be discarded.'
                    )}
                  </div>
                ) : null}
              </AlertDescription>
            </Alert>
            <Textarea
              aria-label={t('Billing expression')}
              value={rawExpr}
              onChange={(event) => handleRawChange(event.target.value)}
              placeholder='tier("base", u("seconds") * 0.4)'
              rows={7}
              className='font-mono text-xs'
              spellCheck={false}
            />
            <TaskBillingPreview
              currency={props.currency}
              config={previewConfig}
              requestRuleExpr={previewRequestRuleExpr}
              sample={previewSample}
              usageSchema={props.usageSchema}
              usageExamples={props.usageExamples}
              onSampleChange={handlePreviewSampleChange}
              onSampleReplace={setPreviewSample}
            />
          </div>
        )}
      </div>
      <RequestSimulation
        expression={
          editorMode === 'raw'
            ? rawExpr
            : combineBillingExpr(
                generateTaskExprFromConfig(
                  { tiers: visualTiers },
                  props.usageSchema
                ),
                props.requestRuleExpr
              )
        }
        usage={previewSample}
        usageSchema={props.usageSchema}
        currency={props.currency}
        mode='task'
      />
    </div>
  )
})
