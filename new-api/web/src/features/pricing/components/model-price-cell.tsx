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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { getCurrencyLabel } from '@/lib/currency'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { DEFAULT_TOKEN_UNIT } from '../constants'
import { useBillingTime } from '../hooks/use-billing-time'
import {
  getDynamicDisplayGroupRatio,
  getDynamicPriceUnitLabelKey,
  getDynamicPricingSummary,
  isUnconfiguredTaskUsageModel,
} from '../lib/dynamic-price'
import { isTokenBasedModel } from '../lib/model-helpers'
import { formatPrice, formatRequestPrice } from '../lib/price'
import { taskUsageUnitLabel } from '../lib/task-price-display'
import type { PricingModel, TokenUnit } from '../types'

export type ModelPriceCellOptions = {
  tokenUnit?: TokenUnit
  priceRate?: number
  usdExchangeRate?: number
  showRechargePrice?: boolean
  selectedGroup?: string
}

export function ModelPriceCell(props: {
  model: PricingModel
  options?: ModelPriceCellOptions
  showExpression?: boolean
}) {
  const { t, i18n } = useTranslation()
  const currency = useSystemConfigStore((state) => state.config.currency)
  const currencyLabel =
    currency.quotaDisplayType === 'TOKENS' ? 'USD' : getCurrencyLabel()
  const options = props.options ?? {}
  const tokenUnit = options.tokenUnit ?? DEFAULT_TOKEN_UNIT
  const tokenUnitLabel = tokenUnit === 'K' ? '1K' : '1M'
  const billingTime = useBillingTime(props.model.billing_expr)
  const dynamic = useMemo(
    () =>
      getDynamicPricingSummary(props.model, {
        priceRate: options.priceRate,
        usdExchangeRate: options.usdExchangeRate,
        showRechargePrice: options.showRechargePrice,
        now: billingTime === undefined ? undefined : new Date(billingTime),
        tokenUnit,
        showCurrencySymbol: false,
        groupRatioMultiplier: getDynamicDisplayGroupRatio(
          props.model,
          options.selectedGroup
        ),
      }),
    // Currency is read indirectly by the price formatter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      props.model,
      tokenUnit,
      options.priceRate,
      options.usdExchangeRate,
      options.showRechargePrice,
      options.selectedGroup,
      billingTime,
      currency,
    ]
  )
  let metrics: Array<{ label: string; value: string }>
  const providerCaption = dynamic?.providerCount
    ? t('{{count}} providers', { count: dynamic.providerCount })
    : ''
  const unconfiguredCaption = dynamic?.hasUnconfiguredProviders
    ? t('Not configured for some providers')
    : ''
  let caption = t('{{currency}} / {{unit}} tokens', {
    currency: currencyLabel,
    unit: tokenUnitLabel,
  })

  if (dynamic) {
    if (dynamic.isSpecialExpression) {
      return (
        <span className='block max-w-full min-w-0'>
          <span className='text-muted-foreground block truncate text-sm'>
            {t('Special billing expression')}
          </span>
          {providerCaption && (
            <span className='text-muted-foreground block text-xs'>
              {[providerCaption, unconfiguredCaption]
                .filter(Boolean)
                .join(' · ')}
            </span>
          )}
          {props.showExpression !== false && (
            <code className='text-muted-foreground mt-1 line-clamp-2 block text-xs break-all whitespace-normal'>
              {dynamic.rawExpression}
            </code>
          )}
        </span>
      )
    }
    const hasRequestPrice = dynamic.primaryEntries.some(
      (entry) => entry.unit === 'request' || entry.unit === 'image'
    )
    metrics = dynamic.primaryEntries
      .slice(0, hasRequestPrice ? 3 : 2)
      .map((entry) => {
        const unit = getDynamicPriceUnitLabelKey(entry)
        const unitLabel = taskUsageUnitLabel(
          entry,
          i18n.language,
          unit ? t(unit) : ''
        )
        let suffix = unitLabel ? `/${unitLabel}` : ''
        if (hasRequestPrice && entry.unit === 'token') {
          suffix = `/${t('{{unit}} tokens', { unit: tokenUnitLabel })}`
        }
        return {
          label:
            entry.labelKind === 'schema'
              ? entry.shortLabel
              : t(entry.shortLabel),
          value: `${entry.formattedRange ?? entry.formatted}${suffix}`,
        }
      })
    if (metrics.length === 0) {
      return (
        <span className='text-muted-foreground text-sm'>
          {dynamic.hasUnconfiguredProviders
            ? t('Not configured')
            : t('Dynamic Pricing')}
          {providerCaption && (
            <span className='block text-xs'>
              {[providerCaption, unconfiguredCaption]
                .filter(Boolean)
                .join(' · ')}
            </span>
          )}
        </span>
      )
    }
    if (dynamic.isTaskUsage || hasRequestPrice) caption = currencyLabel
    if (dynamic.isTimePricing) caption += ` · ${t('Current period price')}`
    if (dynamic.isMixedBilling) {
      caption += ` · ${t('Token or per-call pricing')}`
    }
    if (providerCaption) caption += ` · ${providerCaption}`
    if (unconfiguredCaption) caption += ` · ${unconfiguredCaption}`
    if (dynamic.tierCount > 1) {
      caption += ` · ${t('{{count}} tiers', { count: dynamic.tierCount })}`
    }
  } else {
    if (isUnconfiguredTaskUsageModel(props.model)) {
      return (
        <span className='text-muted-foreground text-sm'>
          {t('Not configured')}
        </span>
      )
    }
    const tokenBased = isTokenBasedModel(props.model)
    if (
      !Number.isFinite(
        tokenBased ? props.model.model_ratio : props.model.model_price
      )
    ) {
      return (
        <span className='text-muted-foreground text-sm'>
          {t('Unset price')}
        </span>
      )
    }
    if (tokenBased) {
      metrics = [
        {
          label: t('Input'),
          value: formatPrice(
            props.model,
            'input',
            tokenUnit,
            options.showRechargePrice,
            options.priceRate,
            options.usdExchangeRate,
            options.selectedGroup,
            false
          ),
        },
        {
          label: t('Output'),
          value: formatPrice(
            props.model,
            'output',
            tokenUnit,
            options.showRechargePrice,
            options.priceRate,
            options.usdExchangeRate,
            options.selectedGroup,
            false
          ),
        },
      ]
    } else {
      metrics = [
        {
          label: t('Per-request'),
          value: formatRequestPrice(
            props.model,
            options.showRechargePrice,
            options.priceRate,
            options.usdExchangeRate,
            options.selectedGroup,
            false
          ),
        },
      ]
      caption = `${currencyLabel} / ${t('request')}`
    }
  }
  return (
    <span className='block w-full max-w-full min-w-0 space-y-1.5'>
      <span
        className={metrics.length > 1 ? 'grid grid-cols-2 gap-x-4' : 'grid'}
      >
        {metrics.map((metric) => (
          <span
            key={metric.label}
            className='flex min-w-0 flex-col items-start gap-y-0.5 sm:flex-row sm:flex-wrap sm:items-baseline sm:gap-x-1.5'
          >
            <span
              className='text-muted-foreground min-w-0 truncate text-xs font-normal'
              title={metric.label}
            >
              {metric.label}
            </span>
            <span
              className='min-w-0 font-mono text-sm break-words whitespace-normal tabular-nums'
              title={metric.value}
            >
              {metric.value}
            </span>
          </span>
        ))}
      </span>
      <span
        className='text-muted-foreground block text-xs font-normal break-words whitespace-normal sm:truncate'
        title={caption}
      >
        {caption}
      </span>
    </span>
  )
}
