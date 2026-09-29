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
import { createContext, useContext } from 'react'

import { BILLING_PRICING_VARS, splitBillingExprAndRequestRules } from '@/features/pricing/lib/billing-expr'
import { tryParseVisualConfig } from '@/features/pricing/lib/tier-expr'

import type { PricingSyncValues } from '../types'
import {
  OFFICIAL_CHANNEL_ID,
  OFFICIAL_CHANNEL_NAME,
  MODELS_DEV_PRESET_ID,
  MODELS_DEV_PRESET_NAME,
} from './constants'
import { formatPricingNumber } from './pricing-format'

export type PricingSourceSelection = { model: string; source: string }
export type PricingSourceSelections = Record<string, string>

export function getUpstreamDisplayName(
  sourceName: string,
  t: (key: string) => string
): string {
  if (
    sourceName === OFFICIAL_CHANNEL_NAME ||
    sourceName === `${OFFICIAL_CHANNEL_NAME}(${OFFICIAL_CHANNEL_ID})`
  ) {
    return t('Official pricing preset')
  }
  if (
    sourceName === MODELS_DEV_PRESET_NAME ||
    sourceName === `${MODELS_DEV_PRESET_NAME}(${MODELS_DEV_PRESET_ID})`
  ) {
    return t('models.dev pricing preset')
  }
  return sourceName
}

export function getSyncPriceKind(
  values?: PricingSyncValues
): 'expression' | 'request' | 'token' | 'unset' {
  if (
    values?.billing_mode === 'tiered_expr' &&
    typeof values.billing_expr === 'string' &&
    values.billing_expr.trim()
  ) {
    return 'expression'
  }
  if (typeof values?.model_price === 'number') return 'request'
  if (typeof values?.model_ratio === 'number') return 'token'
  return 'unset'
}

export function sameSyncPrice(
  left: PricingSyncValues,
  right: PricingSyncValues
): boolean {
  const fields = new Set([...Object.keys(left), ...Object.keys(right)])
  for (const field of fields) {
    const a = left[field as keyof PricingSyncValues]
    const b = right[field as keyof PricingSyncValues]
    if (typeof a === 'number' && typeof b === 'number') {
      if (Math.abs(a - b) >= 1e-9) return false
    } else if (a !== b) return false
  }
  return true
}

// Prices are USD before group or recharge adjustments. Audio output uses
// the audio input price as its base, matching realtime quota calculation.
export function getSyncPriceLines(
  values: PricingSyncValues,
  t: (key: string) => string
): Array<{ label: string; value: string }> {
  if (getSyncPriceKind(values) === 'request') {
    return [
      {
        label: t('Per-request'),
        value: `$${formatPricingNumber(values.model_price)}`,
      },
    ]
  }
  if (getSyncPriceKind(values) !== 'token') return []
  const input = Number(values.model_ratio) * 2
  const formattedInput = formatPricingNumber(input)
  const lines = [
    { label: t('Input'), value: formattedInput ? `$${formattedInput}` : '—' },
  ]
  const multipliers = [
    ['completion_ratio', t('Output')],
    ['cache_ratio', t('Cache Read')],
    ['create_cache_ratio', t('Cache write')],
    ['image_ratio', t('Image input')],
    ['audio_ratio', t('Audio input')],
    ['audio_completion_ratio', t('Audio output')],
  ] as const
  for (const [field, label] of multipliers) {
    const ratio = values[field]
    if (typeof ratio !== 'number') continue
    let price = input * ratio
    if (field === 'audio_completion_ratio') {
      if (typeof values.audio_ratio !== 'number') {
        lines.push({ label, value: '—' })
        continue
      }
      price *= values.audio_ratio
    }
    const formatted = formatPricingNumber(price)
    lines.push({ label, value: formatted ? `$${formatted}` : '—' })
  }
  return lines
}

export function getSyncExpressionPricing(expression: string, t: (key: string) => string) {
  const { billingExpr, requestRuleExpr } = splitBillingExprAndRequestRules(expression)
  const config = tryParseVisualConfig(billingExpr)
  if (!config) return null
  // Do not turn malformed or overflowing upstream numbers into free prices.
  const body = billingExpr.replace(/"(?:\\.|[^"\\])*"/g, '')
  for (const match of body.matchAll(/\*\s*([+\-\d.eE]+)/g)) {
    if (!Number.isFinite(Number(match[1])) || Number(match[1]) < 0) return null
  }
  const fields = BILLING_PRICING_VARS.filter((field) =>
    field.tierField && new RegExp(`\\b${field.key}\\s*\\*`).test(body)
  )
  const conditionLabels = { p: t('Input tokens'), c: t('Output tokens'), len: t('Length') }
  return {
    requestRuleExpr,
    tiers: config.tiers.map((tier) => ({
      label: tier.label,
      condition: tier.conditions.map((condition) =>
        `${conditionLabels[condition.var]} ${condition.op} ${Number(condition.value).toLocaleString()}`
      ).join(' ∧ '),
      lines: fields.map((field) => ({
        label: t(field.shortLabel),
        value: `$${formatPricingNumber(Number(tier[field.tierField!]))}`,
      })),
    })),
  }
}

export function describeSyncPrice(
  values: PricingSyncValues,
  t: (key: string) => string
): string {
  const kind = getSyncPriceKind(values)
  if (kind === 'expression') {
    const parsed = getSyncExpressionPricing(String(values.billing_expr), t)
    if (parsed) {
      return [
        t('Expression pricing'),
        `USD / ${t('1M token')}`,
        ...parsed.tiers.flatMap((tier) => [
          ...(parsed.tiers.length > 1 ? [tier.condition || tier.label || t('Default')] : []),
          ...tier.lines.map((line) => `${line.label}: ${line.value}`),
        ]),
        ...(parsed.requestRuleExpr ? [`${t('Includes request rules')}: ${parsed.requestRuleExpr}`] : []),
      ].join('\n')
    }
    return `${t('Expression pricing')}\n${values.billing_expr}`
  }
  if (kind === 'unset') return t('Unset price')
  const unit =
    kind === 'request' ? `USD / ${t('request')}` : `USD / ${t('1M token')}`
  return [
    unit,
    ...getSyncPriceLines(values, t).map(
      (line) => `${line.label}: ${line.value}`
    ),
  ].join('\n')
}

export type SyncPriceSelectionContext = {
  bulkStates: Record<
    string,
    { selections: PricingSourceSelection[]; selectedModels: string[] }
  >
  selectedSources: PricingSourceSelections
  isDisabled: boolean
  onSelectPrices: (selections: PricingSourceSelection[]) => void
  onUnselectPrices: (models: string[]) => void
}
export const SyncPriceContext = createContext<SyncPriceSelectionContext | null>(
  null
)
export function useSyncPriceSelection() {
  const context = useContext(SyncPriceContext)
  if (!context) throw new Error('Sync price selection provider is required')
  return context
}
