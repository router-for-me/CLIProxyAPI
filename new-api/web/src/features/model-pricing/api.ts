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
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { t } from 'i18next'

import { pluginExpressionsEqual } from '@/features/pricing/lib/plugin-pricing'
import type {
  BillingUsageSchema,
  BillingUsageExample,
} from '@/features/pricing/types'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { createServerError } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  PRICING_KEYS,
  pricingValuesByModel,
  type PricingOptions,
  type PricingValues,
  type CacheWriteMode,
  type LegacyBillingDetails,
} from './pricing'

export type ModelPricingDescription = {
  billing_details?: LegacyBillingDetails
  effective: PricingValues
  cache_write_mode?: CacheWriteMode
}

export type ModelPricingPluginVariant = {
  plugin_key: string
  plugin_name: string
  icon?: string
  usage_schema: BillingUsageSchema
  usage_examples?: BillingUsageExample[]
  configured: string
  effective: string
  compatible: boolean
  stale?: boolean
}

export type ModelPricingEntry = ModelPricingDescription & {
  plugin_variants?: ModelPricingPluginVariant[]
  model_name: string
  version: string
  configured: PricingValues
  usage_schema?: BillingUsageSchema
}

export type ModelPricingConfig = {
  entries: ModelPricingEntry[]
  options: PricingOptions
  empty_version: string
}
export type ModelPricingChange = {
  model_name: string
  expected_version: string
  pricing: PricingValues
  reset?: boolean
}

export type ModelPricingConversion = Partial<ModelPricingDescription> & {
  expression?: string
  unsupported_reason?: string
}

export async function previewModelPricingConversion(request: {
  model_name: string
  pricing: PricingValues
}): Promise<ModelPricingConversion> {
  const response = await api.post('/api/option/model_pricing/convert', request)
  if (!response.data.success) {
    throw createServerError(
      response.data,
      t('Failed to prepare pricing conversion')
    )
  }
  return response.data.data
}

export async function previewModelPricing(request: {
  model_name: string
  pricing: PricingValues
}): Promise<{
  effective: PricingValues
  cacheWriteMode?: CacheWriteMode
  billingDetails?: LegacyBillingDetails
}> {
  const response = await api.post('/api/option/model_pricing/preview', request)
  if (!response.data.success) {
    throw createServerError(response.data, t('Failed to load model pricing'))
  }
  return {
    effective: response.data.data.effective,
    cacheWriteMode: response.data.data.cache_write_mode,
    billingDetails: response.data.data.billing_details,
  }
}

export function useCanEditModelPricing() {
  return useAuthStore((state) => state.auth.user?.role === ROLE.SUPER_ADMIN)
}

export async function getModelPricing(
  names: string[] = []
): Promise<ModelPricingConfig> {
  const params = new URLSearchParams()
  for (const name of names) params.append('model', name)
  const res = await api.get('/api/option/model_pricing', { params })
  if (!res.data.success) {
    throw createServerError(res.data, t('Failed to load model pricing'))
  }
  return res.data.data
}

export function useModelPricing(names: string[] = [], enabled = true) {
  const canEdit = useCanEditModelPricing()
  return useQuery({
    queryKey: ['model-pricing-config', ...names],
    queryFn: () => getModelPricing(names),
    enabled: enabled && canEdit,
    refetchOnWindowFocus: false,
  })
}

export async function invalidateModelPricing(client: QueryClient) {
  await Promise.all([
    client.invalidateQueries({ queryKey: ['model-pricing-config'] }),
    client.invalidateQueries({ queryKey: ['model-pricing-preview'] }),
    client.invalidateQueries({ queryKey: ['system-options'] }),
    client.invalidateQueries({ queryKey: ['pricing'] }),
    client.invalidateQueries({ queryKey: ['models'] }),
  ])
}

export async function saveModelPricing(changes: ModelPricingChange[]) {
  if (!changes.length) return
  const res = await api.patch('/api/option/model_pricing', { changes })
  if (!res.data.success) {
    throw createServerError(res.data, t('Failed to save model pricing'))
  }
}

export function useSaveModelPricing() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: saveModelPricing,
    onSuccess: () => invalidateModelPricing(client),
  })
}

// Only dirty model fields are applied to stored configuration. Display-only
// built-in expressions for other models never become administrator overrides.
export function buildPricingChanges(
  snapshot: ModelPricingConfig,
  before: PricingOptions,
  after: PricingOptions
): ModelPricingChange[] {
  const previous = pricingValuesByModel(before)
  const next = pricingValuesByModel(after)
  const entries = new Map(
    snapshot.entries.map((entry) => [entry.model_name, entry])
  )
  const changes: ModelPricingChange[] = []
  for (const name of new Set([...previous.keys(), ...next.keys()])) {
    const oldValues = previous.get(name) ?? {}
    const newValues = next.get(name) ?? {}
    const dirty = PRICING_KEYS.filter((key) =>
      key === 'billing_setting.plugin_billing_expr'
        ? !pluginExpressionsEqual(oldValues[key], newValues[key])
        : oldValues[key] !== newValues[key]
    )
    if (!dirty.length) continue
    const entry = entries.get(name)
    const pricing = { ...entry?.configured }
    for (const key of dirty) {
      delete pricing[key]
      if (newValues[key] !== undefined) {
        Object.assign(pricing, { [key]: newValues[key] })
      }
    }
    if (newValues['billing_setting.billing_mode'] === 'tiered_expr') {
      pricing['billing_setting.billing_mode'] = 'tiered_expr'
      pricing['billing_setting.billing_expr'] =
        newValues['billing_setting.billing_expr']
    }
    changes.push({
      model_name: name,
      expected_version: entry?.version ?? snapshot.empty_version,
      pricing,
    })
  }
  return changes
}
