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
import { AlertTriangle, ChevronDown, PaintBucket } from 'lucide-react'
import { useRef, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Popover,
  PopoverContent,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  USD_PRICING_CURRENCY,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import { getTaskUsagePriceUnitLabelKey } from '@/features/pricing/lib/dynamic-price'
import {
  getTaskEnumFields,
  getTaskNumberFields,
  type TaskMatrixRow,
} from '@/features/pricing/lib/task-expr'
import {
  taskPriceLabel,
  taskUsageUnitLabel,
  taskEnumLabel,
  taskPricingConditions,
} from '@/features/pricing/lib/task-price-display'
import type {
  BillingUsageFieldSchema,
  BillingUsageSchema,
} from '@/features/pricing/types'
import { cn } from '@/lib/utils'

const TASK_MATRIX_GROUP_THRESHOLD = 24

type TaskPricingMatrixProps = {
  currency?: PricingCurrency
  rows: TaskMatrixRow[]
  usageSchema: BillingUsageSchema
  matchedRowIndex: number | null
  onRowChange: (index: number, next: TaskMatrixRow) => void
  onFillColumn: (priceKey: string, value: number) => void
}

type IndexedTaskMatrixRow = {
  index: number
  row: TaskMatrixRow
}

type FillColumnPopoverProps = {
  currency?: PricingCurrency
  priceKey: string
  initialValue: number
  onFillColumn: (priceKey: string, value: number) => void
}

function FillColumnPopover(props: FillColumnPopoverProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState(props.initialValue)

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) setValue(props.initialValue)
    setOpen(nextOpen)
  }

  const handleSubmit = () => {
    if (inputRef.current && !inputRef.current.reportValidity()) return
    const nextValue = Number(value)
    props.onFillColumn(
      props.priceKey,
      Number.isFinite(nextValue) && nextValue >= 0 ? nextValue : 0
    )
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        render={
          <Button
            type='button'
            variant='outline'
            size='sm'
            className='h-8 min-w-8 shrink-0 px-2'
            aria-label={t('Fill entire column')}
          />
        }
      >
        <PaintBucket aria-hidden='true' />
      </PopoverTrigger>
      <PopoverContent align='end' className='w-56'>
        <PopoverHeader>
          <PopoverTitle>{t('Fill entire column')}</PopoverTitle>
        </PopoverHeader>
        <Field className='gap-2'>
          <FieldLabel className='sr-only'>{t('Fill entire column')}</FieldLabel>
          <PricingAmountInput
            ref={inputRef}
            currency={props.currency}
            min={0}
            step={0.000001}
            value={value}
            aria-label={t('Fill entire column')}
            onFocus={(event) => {
              if (Number(event.currentTarget.value) === 0) {
                event.currentTarget.select()
              }
            }}
            onChange={(usd) => setValue(Number(usd))}
            onKeyDown={(event) => {
              if (event.key !== 'Enter') return
              event.preventDefault()
              handleSubmit()
            }}
            className='font-mono'
          />
          <Button type='button' size='sm' onClick={handleSubmit}>
            {t('Apply to all rows')}
          </Button>
        </Field>
      </PopoverContent>
    </Popover>
  )
}

type TaskMatrixTableProps = {
  currency?: PricingCurrency
  entries: IndexedTaskMatrixRow[]
  enumFields: [string, BillingUsageFieldSchema][]
  numberFields: [string, BillingUsageFieldSchema][]
  hiddenEnumField?: string
  firstRow: TaskMatrixRow
  allRowsFree: boolean
  matchedRowIndex: number | null
  onRowChange: (index: number, next: TaskMatrixRow) => void
  onFillColumn: (priceKey: string, value: number) => void
  onPriceKeyDown: (
    event: KeyboardEvent<HTMLInputElement>,
    rowIndex: number,
    priceKey: string
  ) => void
}

function TaskMatrixTable(props: TaskMatrixTableProps) {
  const { t, i18n } = useTranslation()
  const usageSchema = Object.fromEntries([
    ...props.enumFields,
    ...props.numberFields,
  ])
  const visibleEnumFields = props.enumFields.filter(
    ([field]) => field !== props.hiddenEnumField
  )

  return (
    <Table className='min-w-max'>
      <TableHeader>
        <TableRow>
          {visibleEnumFields.map(([field, definition]) => (
            <TableHead key={field} scope='col'>
              <span className='block max-w-64 break-words whitespace-normal'>
                {taskPriceLabel(definition.description, field, i18n.language)}
              </span>
            </TableHead>
          ))}
          {props.numberFields.map(([field, definition]) => (
            <TableHead key={field} scope='col' className='min-w-40'>
              <div className='flex items-center justify-between gap-2'>
                <div className='flex flex-col gap-0.5'>
                  <span className='max-w-64 break-words whitespace-normal'>
                    {taskPriceLabel(
                      definition.description,
                      t('Unit price: {{field}}', { field }),
                      i18n.language
                    )}
                  </span>
                  <span className='text-muted-foreground text-[11px] font-normal'>
                    {(props.currency ?? USD_PRICING_CURRENCY).symbol}/
                    {taskUsageUnitLabel(
                      definition,
                      i18n.language,
                      t(getTaskUsagePriceUnitLabelKey(definition.unit))
                    )}
                  </span>
                </div>
                <FillColumnPopover
                  currency={props.currency}
                  priceKey={field}
                  initialValue={props.firstRow.unitPrices[field] ?? 0}
                  onFillColumn={props.onFillColumn}
                />
              </div>
            </TableHead>
          ))}
          <TableHead scope='col' className='min-w-40'>
            <div className='flex items-center justify-between gap-2'>
              <div className='flex flex-col gap-0.5'>
                <span>{t('Additional charge')}</span>
                <span className='text-muted-foreground max-w-48 text-xs font-normal whitespace-normal'>
                  {t(
                    'Added to the usage cost. Set to 0 for no additional charge.'
                  )}
                </span>
                <span className='text-muted-foreground text-[11px] font-normal'>
                  {(props.currency ?? USD_PRICING_CURRENCY).symbol}/
                  {t('request')}
                </span>
              </div>
              <FillColumnPopover
                currency={props.currency}
                priceKey='constant'
                initialValue={props.firstRow.constant}
                onFillColumn={props.onFillColumn}
              />
            </div>
          </TableHead>
          <TableHead scope='col' className='w-10 px-1 text-center'>
            <span className='sr-only'>{t('Status')}</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.entries.map((entry) => {
          const isFree =
            entry.row.constant === 0 &&
            props.numberFields.every(
              ([field]) => !(entry.row.unitPrices[field] > 0)
            )
          const rowLabel = taskPricingConditions(
            Object.entries(entry.row.combination).map(([field, value]) => ({
              field,
              value,
            })),
            usageSchema,
            i18n.language,
            t
          )
          return (
            <TableRow
              key={`${rowLabel}:${entry.index}`}
              className={cn(
                props.matchedRowIndex === entry.index &&
                  'bg-primary/5 border-l-primary border-l-2'
              )}
            >
              {visibleEnumFields.map(([field]) => (
                <TableCell key={field}>
                  <span className='break-words whitespace-normal'>
                    {taskEnumLabel(
                      usageSchema[field],
                      entry.row.combination[field],
                      i18n.language
                    )}
                  </span>
                </TableCell>
              ))}
              {props.numberFields.map(([field]) => (
                <TableCell key={field}>
                  <PricingAmountInput
                    currency={props.currency}
                    min={0}
                    step={0.000001}
                    value={entry.row.unitPrices[field] ?? 0}
                    data-matrix-col={field}
                    data-matrix-row={entry.index}
                    aria-label={`${taskPriceLabel(usageSchema[field]?.description, t('Unit price: {{field}}', { field }), i18n.language)}: ${rowLabel}`}
                    onFocus={(event) => {
                      if (Number(event.currentTarget.value) === 0) {
                        event.currentTarget.select()
                      }
                    }}
                    onChange={(usd) => {
                      const value = Number(usd)
                      props.onRowChange(entry.index, {
                        ...entry.row,
                        unitPrices: {
                          ...entry.row.unitPrices,
                          [field]:
                            Number.isFinite(value) && value >= 0 ? value : 0,
                        },
                      })
                    }}
                    onKeyDown={(event) =>
                      props.onPriceKeyDown(event, entry.index, field)
                    }
                    className='min-w-28 font-mono'
                  />
                </TableCell>
              ))}
              <TableCell>
                <PricingAmountInput
                  currency={props.currency}
                  min={0}
                  step={0.000001}
                  value={entry.row.constant}
                  data-matrix-col='constant'
                  data-matrix-row={entry.index}
                  aria-label={`${t('Additional charge')}: ${rowLabel}`}
                  onFocus={(event) => {
                    if (Number(event.currentTarget.value) === 0) {
                      event.currentTarget.select()
                    }
                  }}
                  onChange={(usd) => {
                    const value = Number(usd)
                    props.onRowChange(entry.index, {
                      ...entry.row,
                      constant:
                        Number.isFinite(value) && value >= 0 ? value : 0,
                    })
                  }}
                  onKeyDown={(event) =>
                    props.onPriceKeyDown(event, entry.index, 'constant')
                  }
                  className='min-w-28 font-mono'
                />
              </TableCell>
              <TableCell className='px-1 text-center'>
                {!props.allRowsFree && isFree ? (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <span
                          className='inline-flex cursor-help text-amber-600 dark:text-amber-400'
                          tabIndex={0}
                        />
                      }
                    >
                      <AlertTriangle aria-hidden='true' />
                      <span className='sr-only'>
                        {t('This combination will be billed as free.')}
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      {t('This combination will be billed as free.')}
                    </TooltipContent>
                  </Tooltip>
                ) : null}
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}

type TaskMatrixGroupProps = Omit<TaskMatrixTableProps, 'hiddenEnumField'> & {
  groupField: string
  groupValue: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

function TaskMatrixGroup(props: TaskMatrixGroupProps) {
  const { t, i18n } = useTranslation()
  const freeCount = props.entries.filter(
    (entry) =>
      entry.row.constant === 0 &&
      props.numberFields.every(([field]) => !(entry.row.unitPrices[field] > 0))
  ).length
  const containsMatchedRow = props.entries.some(
    (entry) => entry.index === props.matchedRowIndex
  )

  return (
    <Collapsible open={props.open} onOpenChange={props.onOpenChange}>
      <CollapsibleTrigger
        render={
          <button
            type='button'
            className='hover:bg-muted/40 flex w-full items-center justify-between rounded-lg border px-3 py-2 text-left transition-colors'
            aria-expanded={props.open}
          />
        }
      >
        <span className='flex min-w-0 items-center gap-2'>
          <span className='break-words whitespace-normal'>
            {taskEnumLabel(
              Object.fromEntries(props.enumFields)[props.groupField],
              props.groupValue,
              i18n.language
            )}
          </span>
          <span className='text-muted-foreground text-xs'>
            {t('{{count}} combinations', { count: props.entries.length })}
          </span>
          {!props.allRowsFree && freeCount > 0 ? (
            <span className='inline-flex items-center gap-1 text-xs text-amber-600 dark:text-amber-400'>
              <AlertTriangle aria-hidden='true' />
              {freeCount}
              <span className='sr-only'>
                {t('This combination will be billed as free.')}
              </span>
            </span>
          ) : null}
          {!props.open && containsMatchedRow ? (
            <span className='bg-primary size-2 rounded-full'>
              <span className='sr-only'>{t('Hit tier')}</span>
            </span>
          ) : null}
        </span>
        <ChevronDown
          className={cn(
            'text-muted-foreground size-4 shrink-0 transition-transform',
            props.open && 'rotate-180'
          )}
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <CollapsibleContent className='mt-2'>
        <TaskMatrixTable {...props} hiddenEnumField={props.groupField} />
      </CollapsibleContent>
    </Collapsible>
  )
}

export function TaskPricingMatrix(props: TaskPricingMatrixProps) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const enumFields = getTaskEnumFields(props.usageSchema)
  const numberFields = getTaskNumberFields(props.usageSchema)
  const entries = props.rows.map((row, index) => ({ row, index }))
  const firstRow = props.rows[0]
  const allRowsFree = props.rows.every(
    (row) =>
      row.constant === 0 &&
      numberFields.every(([field]) => !(row.unitPrices[field] > 0))
  )
  const firstEnumField = enumFields[0]
  const shouldGroup =
    props.rows.length > TASK_MATRIX_GROUP_THRESHOLD && Boolean(firstEnumField)
  const [openGroups, setOpenGroups] = useState<string[]>(() => {
    const firstGroupValue = firstEnumField?.[1].enum?.[0]
    return firstGroupValue ? [firstGroupValue] : []
  })

  const handlePriceKeyDown = (
    event: KeyboardEvent<HTMLInputElement>,
    rowIndex: number,
    priceKey: string
  ) => {
    if (event.key !== 'Enter' || rowIndex >= props.rows.length - 1) return
    event.preventDefault()
    const selector = `input[data-matrix-col="${CSS.escape(priceKey)}"][data-matrix-row="${rowIndex + 1}"]`
    const nextInput =
      containerRef.current?.querySelector<HTMLInputElement>(selector)
    if (nextInput) {
      nextInput.focus()
      return
    }

    const nextGroupValue = firstEnumField
      ? props.rows[rowIndex + 1]?.combination[firstEnumField[0]]
      : undefined
    if (!shouldGroup || !nextGroupValue) return
    setOpenGroups((current) =>
      current.includes(nextGroupValue) ? current : [...current, nextGroupValue]
    )
    window.requestAnimationFrame(() => {
      containerRef.current?.querySelector<HTMLInputElement>(selector)?.focus()
    })
  }

  if (!firstRow) return null

  return (
    <TooltipProvider>
      <div ref={containerRef} className='flex flex-col gap-3'>
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
        {shouldGroup && firstEnumField ? (
          <div className='flex flex-col gap-2'>
            {(firstEnumField[1].enum ?? []).map((groupValue) => (
              <TaskMatrixGroup
                currency={props.currency}
                key={groupValue}
                entries={entries.filter(
                  (entry) =>
                    entry.row.combination[firstEnumField[0]] === groupValue
                )}
                enumFields={enumFields}
                numberFields={numberFields}
                groupField={firstEnumField[0]}
                groupValue={groupValue}
                open={openGroups.includes(groupValue)}
                onOpenChange={(nextOpen) =>
                  setOpenGroups((current) => {
                    if (nextOpen) {
                      return current.includes(groupValue)
                        ? current
                        : [...current, groupValue]
                    }
                    return current.filter((value) => value !== groupValue)
                  })
                }
                firstRow={firstRow}
                allRowsFree={allRowsFree}
                matchedRowIndex={props.matchedRowIndex}
                onRowChange={props.onRowChange}
                onFillColumn={props.onFillColumn}
                onPriceKeyDown={handlePriceKeyDown}
              />
            ))}
          </div>
        ) : (
          <TaskMatrixTable
            currency={props.currency}
            entries={entries}
            enumFields={enumFields}
            numberFields={numberFields}
            firstRow={firstRow}
            allRowsFree={allRowsFree}
            matchedRowIndex={props.matchedRowIndex}
            onRowChange={props.onRowChange}
            onFillColumn={props.onFillColumn}
            onPriceKeyDown={handlePriceKeyDown}
          />
        )}
      </div>
    </TooltipProvider>
  )
}
