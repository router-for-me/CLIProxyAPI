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

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  parseTiersFromExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { getDynamicPriceEntries } from '@/features/pricing/lib/dynamic-price'
import {
  buildPreviewRows,
  createInitialLaneState,
  pricingAdjustmentRows,
} from '@/features/system-settings/models/model-pricing-core'

import { formatPricingAmount, type PricingCurrency } from './currency'
import {
  pricingRow,
  type CacheWriteMode,
  type PricingValues,
  type LegacyBillingDetails,
} from './pricing'

export type PricingConversionPreview = {
  modelName: string
  effective: PricingValues
  expression: string
  draftFingerprint: string
  cacheWriteMode?: CacheWriteMode
  billingDetails?: LegacyBillingDetails
}

export function PricingConversionDialog(props: {
  preview: PricingConversionPreview
  currency: PricingCurrency
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  const before = pricingRow(props.preview.modelName, props.preview.effective)
  const lanes = createInitialLaneState(before)
  const beforeRows = buildPreviewRows(
    before,
    before.billingMode ?? 'per-token',
    '',
    '',
    lanes.promptPrice,
    lanes.prices,
    lanes.enabled,
    t,
    props.currency,
    props.preview.cacheWriteMode,
    props.preview.billingDetails
  ).filter((row) => row.value !== t('Empty'))
  const tiers = parseTiersFromExpr(
    splitBillingExprAndRequestRules(props.preview.expression).billingExpr
  )
  const afterRows = tiers.flatMap((tier, index) =>
    getDynamicPriceEntries(tier, { tokenUnit: 'M' }).map((row) => ({
      ...row,
      key: `${index}-${row.key}`,
      tierLabel: tiers.length > 1 ? t(tier.conditionText || tier.label) : '',
      imageCount: Boolean(tier.imageCount),
    }))
  )
  const compiled = compileBillingExpression(props.preview.expression)
  const afterAdjustments = pricingAdjustmentRows(
    compiled.status === 'ready'
      ? {
          image_count: compiled.variables.has('image_count'),
          request_rules: compiled.requestRules.map((rule) => ({
            condition: props.preview.expression.slice(
              rule.condition.start,
              rule.condition.end
            ),
            multiplier: rule.multiplier,
          })),
        }
      : undefined,
    t
  )

  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) props.onCancel()
      }}
      title={t('Preview pricing conversion')}
      desc={t(
        'Review the prices and billing expression. Confirming updates the draft; save model pricing to apply it.'
      )}
      confirmText={t('Apply to draft')}
      handleConfirm={props.onConfirm}
      className='max-h-[min(90dvh,var(--dialog-available-height))] overflow-y-auto data-[size=default]:max-w-[calc(100%-2rem)] data-[size=default]:sm:max-w-5xl'
    >
      <p className='font-mono text-sm break-all'>{props.preview.modelName}</p>
      <div className='grid min-w-0 gap-4 md:grid-cols-2'>
        <section
          aria-label={t('Before conversion')}
          className='min-w-0 space-y-4 rounded-lg border p-4'
        >
          <h3 className='font-semibold'>{t('Before conversion')}</h3>
          <dl className='space-y-3'>
            {beforeRows.map((row) => {
              let unitLabel =
                before.billingMode === 'per-request'
                  ? t('request')
                  : t('1M token')
              if (row.unit === 'image') unitLabel = t('image')
              return (
                <div
                  key={row.key}
                  className='flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1'
                >
                  <dt className='text-muted-foreground text-sm'>{row.label}</dt>
                  <dd className='font-mono text-sm tabular-nums'>
                    {row.value}
                    {row.unit !== 'none' && <> / {unitLabel}</>}
                  </dd>
                </div>
              )
            })}
          </dl>
        </section>
        <section
          aria-label={t('After conversion')}
          className='border-primary/30 bg-primary/5 min-w-0 space-y-4 rounded-lg border p-4'
        >
          <h3 className='font-semibold'>{t('After conversion')}</h3>
          <dl className='space-y-3'>
            {afterRows.map((row) => {
              let unitLabel =
                row.unit === 'request' ? t('request') : t('1M token')
              if (row.imageCount) unitLabel = t('image')
              return (
                <div
                  key={row.key}
                  className='flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1'
                >
                  <dt className='text-muted-foreground text-sm'>
                    {row.tierLabel && `${row.tierLabel} · `}
                    {t(row.label)}
                  </dt>
                  <dd className='font-mono text-sm tabular-nums'>
                    {formatPricingAmount(row.value, props.currency)} /{' '}
                    {unitLabel}
                  </dd>
                </div>
              )
            })}
            {afterAdjustments.map((row) => (
              <div
                key={row.key}
                className='flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1'
              >
                <dt className='text-muted-foreground text-sm'>{row.label}</dt>
                <dd className='font-mono text-sm tabular-nums'>{row.value}</dd>
              </div>
            ))}
          </dl>
          <div className='space-y-2 border-t pt-3'>
            <div className='flex items-center justify-between gap-2'>
              <h4 className='text-sm font-medium'>
                {t('Billing expression')} (USD)
              </h4>
              <CopyButton value={props.preview.expression} />
            </div>
            <pre className='bg-background rounded-md border p-3 text-xs break-words whitespace-pre-wrap'>
              <code>{props.preview.expression}</code>
            </pre>
          </div>
        </section>
      </div>
      <Alert>
        <AlertDescription className='text-xs'>
          {t(
            'After conversion, expression reservation and rounding rules apply. Effective unit prices are preserved; individual rounded charges may differ.'
          )}
        </AlertDescription>
      </Alert>
    </ConfirmDialog>
  )
}
