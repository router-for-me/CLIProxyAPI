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
import type { QueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { t } from 'i18next'

import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type { Model, Vendor } from './types'

export type VendorOperation = {
  action: 'assign' | 'merge' | 'delete'
  vendor_ids?: number[]
  model_ids?: number[]
  target_vendor_id?: number
  expected_version?: string
}
export type VendorOperationPreview = {
  action: VendorOperation['action']
  sources: Vendor[]
  target: Vendor | null
  models: Array<
    Pick<
      Model,
      'id' | 'model_name' | 'name_rule' | 'vendor_id' | 'updated_time'
    > & { vendor_name: string }
  >
  version: string
}
export type VendorOperationResult = {
  updated_models: number[]
  deleted_vendors: number[]
}

type VendorErrorPayload = {
  code?: string
  message?: string
  reference_counts?: Record<string, number>
}
export function vendorErrorMessage(error: unknown): string {
  const payload = isAxiosError<VendorErrorPayload>(error)
    ? error.response?.data
    : undefined
  if (payload?.code === 'VENDOR_CONFLICT') {
    return t('Vendor data changed. Preview again before applying.')
  }
  if (payload?.code === 'VENDOR_REFERENCED') {
    const count = Object.values(payload.reference_counts ?? {}).reduce(
      (sum, value) => sum + value,
      0
    )
    return t(
      'Vendors still have {{count}} linked model records. Transfer or clear their assignments first.',
      { count }
    )
  }
  const message =
    payload?.message || (error instanceof Error ? error.message : '')
  switch (message) {
    case 'vendor name is required':
      return t('Vendor name is required')
    case 'vendor name already exists':
      return t('A vendor with this name already exists.')
    case 'vendor name and icon must not exceed 128 characters':
      return t('Vendor name and icon must not exceed 128 characters.')
    case 'select a saved vendor':
    case 'select a saved target vendor':
      return t('Select a saved vendor.')
    case 'vendor does not exist':
    case 'target vendor does not exist':
    case 'source vendor does not exist':
      return t('The vendor no longer exists. Reload the vendor list.')
    case 'a selected model no longer exists':
      return t('A selected model no longer exists. Reload the model list.')
    default:
      return message || t('Operation failed')
  }
}

export async function invalidateVendorData(client: QueryClient) {
  await Promise.all(
    ['vendors', 'models', 'pricing'].map((key) =>
      client.invalidateQueries({ queryKey: [key] })
    )
  )
}

export async function previewVendorOperation(
  operation: VendorOperation
): Promise<VendorOperationPreview> {
  const response = await api.post('/api/vendors/operations/preview', operation)
  if (!response.data.success) {
    throw createServerError(
      response.data,
      t('Failed to preview vendor changes')
    )
  }
  return response.data.data
}
export async function applyVendorOperation(
  operation: VendorOperation
): Promise<VendorOperationResult> {
  const response = await api.post('/api/vendors/operations', operation)
  if (!response.data.success) {
    throw createServerError(response.data, t('Failed to apply vendor changes'))
  }
  return response.data.data
}
