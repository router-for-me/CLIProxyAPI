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
import { t } from 'i18next'

import { combineBillingExpr } from '@/features/pricing/lib/billing-expr'
import { splitPluginBillingExprKey } from '@/features/pricing/lib/plugin-pricing'
import type { PricingModel } from '@/features/pricing/types'
import type { ModelRatioData } from '@/features/system-settings/models/model-pricing-core'
import {
  buildModelSnapshots,
  type ModelPricingSnapshot,
} from '@/features/system-settings/models/model-pricing-snapshots'

import type { ModelPricingEntry } from './api'

export const PRICING_KEYS = [
  'ModelPrice',
  'ModelRatio',
  'CompletionRatio',
  'CacheRatio',
  'CreateCacheRatio',
  'ImageRatio',
  'AudioRatio',
  'AudioCompletionRatio',
  'billing_setting.billing_mode',
  'billing_setting.billing_expr',
  'billing_setting.plugin_billing_expr',
] as const
export type PricingKey = (typeof PRICING_KEYS)[number]
export type PricingValues = Partial<
  Record<
    Exclude<PricingKey, 'billing_setting.plugin_billing_expr'>,
    number | string
  >
> & {
  'billing_setting.plugin_billing_expr'?: Record<string, string>
}
export type PricingOptions = Record<PricingKey, string>

export type CacheWriteMode = 'none' | 'standard' | 'claude_ttl'

export type LegacyBillingDetails = {
  audio_input_price?: number
  audio_output_price?: number
  audio_text_branches?: boolean
  image_count?: boolean
  request_rules?: { condition: string; multiplier: number }[]
}

export function modelPricingDisplay(
  entry: Pick<ModelPricingEntry, 'model_name' | 'effective' | 'usage_schema'> &
    Partial<Pick<ModelPricingEntry, 'configured' | 'cache_write_mode'>>
): PricingModel {
  const values = entry.effective
  return {
    id: 0,
    model_name: entry.model_name,
    enable_groups: [],
    quota_type:
      values.ModelPrice !== undefined &&
      values['billing_setting.billing_mode'] !== 'tiered_expr'
        ? 1
        : 0,
    model_ratio: Number(values.ModelRatio ?? Number.NaN),
    completion_ratio: Number(values.CompletionRatio ?? Number.NaN),
    model_price:
      values.ModelPrice === undefined ? undefined : Number(values.ModelPrice),
    cache_ratio:
      values.CacheRatio === undefined || Number(values.CacheRatio) === 1
        ? undefined
        : Number(values.CacheRatio),
    create_cache_ratio:
      values.CreateCacheRatio === undefined ||
      !(
        entry.cache_write_mode === 'standard' ||
        entry.cache_write_mode === 'claude_ttl' ||
        entry.configured?.CreateCacheRatio !== undefined
      )
        ? undefined
        : Number(values.CreateCacheRatio),
    image_ratio:
      values.ImageRatio === undefined || Number(values.ImageRatio) === 1
        ? undefined
        : Number(values.ImageRatio),
    audio_ratio:
      values.AudioRatio === undefined ? undefined : Number(values.AudioRatio),
    audio_completion_ratio:
      values.AudioCompletionRatio === undefined
        ? undefined
        : Number(values.AudioCompletionRatio),
    billing_mode:
      typeof values['billing_setting.billing_mode'] === 'string'
        ? values['billing_setting.billing_mode']
        : undefined,
    billing_expr:
      typeof values['billing_setting.billing_expr'] === 'string'
        ? values['billing_setting.billing_expr']
        : undefined,
    billing_usage_schema: entry.usage_schema,
  }
}

export const pricingFieldMap = {
  price: 'ModelPrice',
  ratio: 'ModelRatio',
  completionRatio: 'CompletionRatio',
  cacheRatio: 'CacheRatio',
  createCacheRatio: 'CreateCacheRatio',
  imageRatio: 'ImageRatio',
  audioRatio: 'AudioRatio',
  audioCompletionRatio: 'AudioCompletionRatio',
} as const

export function pricingOptions(
  values: Record<string, string | boolean>
): PricingOptions {
  return Object.fromEntries(
    PRICING_KEYS.map((key) => {
      let value = values[key]
      if (key === 'billing_setting.billing_mode') value ??= values.BillingMode
      if (key === 'billing_setting.billing_expr') value ??= values.BillingExpr
      if (key === 'billing_setting.plugin_billing_expr') {
        value ??= values.PluginBillingExpr
      }
      return [key, typeof value === 'string' ? value : '{}']
    })
  ) as PricingOptions
}

export function pricingRows(options: PricingOptions): ModelPricingSnapshot[] {
  return buildModelSnapshots({
    modelPrice: options.ModelPrice,
    modelRatio: options.ModelRatio,
    completionRatio: options.CompletionRatio,
    cacheRatio: options.CacheRatio,
    createCacheRatio: options.CreateCacheRatio,
    imageRatio: options.ImageRatio,
    audioRatio: options.AudioRatio,
    audioCompletionRatio: options.AudioCompletionRatio,
    billingMode: options['billing_setting.billing_mode'],
    billingExpr: options['billing_setting.billing_expr'],
    pluginBillingExpr: options['billing_setting.plugin_billing_expr'],
  })
}

export function pricingRow(
  name: string,
  values: PricingValues
): ModelRatioData {
  const options = Object.fromEntries(
    PRICING_KEYS.map((key) => {
      if (key === 'billing_setting.plugin_billing_expr') {
        return [
          key,
          JSON.stringify(
            Object.fromEntries(
              Object.entries(values[key] ?? {}).map(([plugin, expression]) => [
                `${plugin}::${name}`,
                expression,
              ])
            )
          ),
        ]
      }
      return [
        key,
        JSON.stringify(
          values[key] === undefined ? {} : { [name]: values[key] }
        ),
      ]
    })
  ) as PricingOptions
  const row = pricingRows(options).find((entry) => entry.name === name)
  let billingMode: ModelRatioData['billingMode'] = 'per-token'
  if (row?.billingMode === 'tiered_expr') billingMode = 'tiered_expr'
  else if (row?.price) billingMode = 'per-request'
  return {
    ...row,
    name,
    billingMode,
    pluginBillingExpr: values['billing_setting.plugin_billing_expr'],
  }
}

export function pricingFromDraft(data: ModelRatioData): PricingValues {
  const values: PricingValues = {
    ...(data.pluginBillingExpr === undefined
      ? {}
      : { 'billing_setting.plugin_billing_expr': data.pluginBillingExpr }),
    'billing_setting.billing_mode':
      data.billingMode === 'tiered_expr' ? 'tiered_expr' : 'ratio',
  }
  for (const [field, key] of Object.entries(pricingFieldMap)) {
    const value = data[field as keyof typeof pricingFieldMap]
    if (value === undefined || value === '') continue
    const number = Number(value)
    if (!Number.isFinite(number) || number < 0) {
      throw new Error(t('Enter a finite, non-negative price'))
    }
    if (
      data.billingMode === 'tiered_expr' ||
      (data.billingMode === 'per-request'
        ? key === 'ModelPrice'
        : key !== 'ModelPrice')
    ) {
      values[key] = number
    }
  }
  if (data.billingMode === 'tiered_expr') {
    values['billing_setting.billing_expr'] = combineBillingExpr(
      data.billingExpr || '',
      data.requestRuleExpr || ''
    )
  }
  return values
}

export function applyPricingDraft(
  options: PricingOptions,
  data: ModelRatioData,
  names: string[] = [data.name]
): PricingOptions {
  const values = pricingFromDraft(data)
  if (names.length === 1 && names[0] === data.name) {
    return applyPricingValues(options, values, names)
  }
  // Provider bindings belong to each model. Copy only the shared model price
  // to other models, while still committing the source provider draft.
  const copied = { ...values }
  delete copied['billing_setting.plugin_billing_expr']
  const next = applyPricingValues(options, copied, names)
  return names.includes(data.name)
    ? applyPricingValues(next, values, [data.name])
    : next
}

function applyPricingValues(
  options: PricingOptions,
  values: PricingValues,
  names: string[]
): PricingOptions {
  return Object.fromEntries(
    PRICING_KEYS.map((key) => {
      const map = JSON.parse(options[key] ?? '{}') as Record<
        string,
        number | string
      >
      if (key === 'billing_setting.plugin_billing_expr') {
        // Model-only imports and batch copies retain each target's provider prices.
        if (values[key] === undefined) return [key, JSON.stringify(map)]
        for (const name of names) {
          for (const variant of Object.keys(map)) {
            if (splitPluginBillingExprKey(variant)?.[1] === name) {
              delete map[variant]
            }
          }
          for (const [plugin, expression] of Object.entries(values[key])) {
            map[`${plugin}::${name}`] = expression
          }
        }
        return [key, JSON.stringify(map)]
      }
      for (const name of names) {
        delete map[name]
        if (values[key] !== undefined) {
          Object.defineProperty(map, name, {
            value: values[key],
            enumerable: true,
            writable: true,
            configurable: true,
          })
        }
      }
      return [key, JSON.stringify(map)]
    })
  ) as PricingOptions
}

export function pricingValuesByModel(
  options: PricingOptions
): Map<string, PricingValues> {
  const models = new Map<string, PricingValues>()
  for (const key of PRICING_KEYS) {
    const map: unknown = JSON.parse(options[key] ?? '{}')
    if (map === null || typeof map !== 'object' || Array.isArray(map)) {
      throw new Error(t('Pricing must be a JSON object'))
    }
    for (const [name, value] of Object.entries(map)) {
      if (typeof value !== 'number' && typeof value !== 'string') {
        throw new Error(t('Invalid pricing value'))
      }
      if (key === 'billing_setting.plugin_billing_expr') {
        const parts = splitPluginBillingExprKey(name)
        if (!parts || typeof value !== 'string') {
          throw new Error(t('Invalid pricing value'))
        }
        const [plugin, modelName] = parts
        const model = models.get(modelName) ?? {}
        model[key] = { ...model[key], [plugin]: value }
        models.set(modelName, model)
        continue
      }
      const model = models.get(name) ?? {}
      model[key] = value
      models.set(name, model)
    }
  }
  return models
}

export function applyPriceSyncSelections(
  options: PricingOptions,
  selections: Record<string, Record<string, number | string>>
): PricingOptions {
  let result = options
  for (const [name, fields] of Object.entries(selections)) {
    const next: PricingValues = {}
    const expression =
      typeof fields.billing_expr === 'string' &&
      fields.billing_expr.trim() !== ''
    if (expression) {
      next['billing_setting.billing_mode'] = 'tiered_expr'
      next['billing_setting.billing_expr'] = fields.billing_expr
    } else {
      next['billing_setting.billing_mode'] = 'ratio'
      const fixed = fields.model_price !== undefined
      for (const [field, value] of Object.entries(fields)) {
        if (field === 'billing_mode' || field === 'billing_expr') continue
        if (fixed && field !== 'model_price') continue
        const key = field
          .split('_')
          .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
          .join('') as PricingKey
        if (
          PRICING_KEYS.includes(key) &&
          key !== 'billing_setting.plugin_billing_expr'
        ) {
          next[key] = value
        }
      }
    }
    const validated = pricingFromDraft(pricingRow(name, next))
    if (expression) {
      // Sync is a literal import. The editor may normalize request-rule
      // parentheses, which would otherwise produce another upstream diff.
      validated['billing_setting.billing_expr'] = fields.billing_expr
    }
    result = applyPricingValues(result, validated, [name])
  }
  return result
}
