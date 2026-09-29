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
import { api } from '@/lib/api'

import type {
  GetModelsParams,
  GetModelsResponse,
  GetModelResponse,
  GetVendorsResponse,
  GetVendorResponse,
  Model,
  Vendor,
  SearchModelsParams,
  SyncUpstreamResponse,
  PreviewUpstreamDiffResponse,
  MissingModelsResponse,
  PrefillGroupsResponse,
  SyncLocale,
  SyncSource,
  MetadataSyncRequest,
  DeploymentSettingsResponse,
  ListDeploymentsResponse,
} from './types'

// ============================================================================
// Model CRUD Operations
// ============================================================================

/**
 * Get paginated list of models
 */
export async function getModels(
  params: GetModelsParams = {}
): Promise<GetModelsResponse> {
  const res = await api.get('/api/models/', { params })
  return res.data
}

/**
 * Search models with filters
 */
export async function searchModels(
  params: SearchModelsParams
): Promise<GetModelsResponse> {
  const res = await api.get('/api/models/search', { params })
  return res.data
}

/**
 * Get single model by ID
 */
export async function getModel(id: number): Promise<GetModelResponse> {
  const res = await api.get(`/api/models/${id}`)
  return res.data
}

/**
 * Create new model
 */
export async function createModel(
  data: Partial<Model>
): Promise<{ success: boolean; message?: string; data?: Model }> {
  const res = await api.post('/api/models/', data, {
    skipBusinessError: true,
    skipErrorHandler: true,
  })
  return res.data
}

/**
 * Update existing model
 */
export async function updateModel(
  data: Partial<Model> & { id: number }
): Promise<{ success: boolean; message?: string; data?: Model }> {
  const res = await api.put('/api/models/', data, {
    skipBusinessError: true,
    skipErrorHandler: true,
  })
  return res.data
}

/**
 * Update model status only
 */
export async function updateModelStatus(
  id: number,
  status: number
): Promise<{ success: boolean; message?: string }> {
  const res = await api.put('/api/models/?status_only=true', { id, status })
  return res.data
}

/**
 * Delete model
 */
export async function deleteModel(
  id: number,
  removeFromChannels = false,
  removePricing = false
): Promise<{ success: boolean; message?: string; data: ModelDeleteResult }> {
  const res = await api.delete(`/api/models/${id}`, {
    params: {
      remove_from_channels: removeFromChannels,
      remove_pricing: removePricing,
    },
  })
  return res.data
}

// ============================================================================
// Vendor Management
// ============================================================================

/**
 * Get paginated list of vendors
 */
export async function getVendors(params?: {
  p?: number
  page_size?: number
}): Promise<GetVendorsResponse> {
  const res = await api.get('/api/vendors/', {
    params: params || { page_size: 1000 },
  })
  return res.data
}

/**
 * Search vendors
 */
export async function searchVendors(params: {
  association?: string
  keyword?: string
  p?: number
  page_size?: number
}): Promise<GetVendorsResponse> {
  const res = await api.get('/api/vendors/search', { params })
  return res.data
}

/**
 * Get single vendor by ID
 */
export async function getVendor(id: number): Promise<GetVendorResponse> {
  const res = await api.get(`/api/vendors/${id}`)
  return res.data
}

/**
 * Create new vendor
 */
export async function createVendor(
  data: Partial<Vendor>
): Promise<{ success: boolean; message?: string; data?: Vendor }> {
  const res = await api.post('/api/vendors/', data)
  return res.data
}

/**
 * Update existing vendor
 */
export async function updateVendor(
  data: Partial<Vendor> & { id: number }
): Promise<{ success: boolean; message?: string; data?: Vendor }> {
  const res = await api.put('/api/vendors/', data)
  return res.data
}

/**
 * Delete vendor
 */
export async function deleteVendor(
  id: number
): Promise<{ success: boolean; message?: string }> {
  const res = await api.delete(`/api/vendors/${id}`)
  return res.data
}

// ============================================================================
// Sync Operations
// ============================================================================

/**
 * Sync upstream models (missing only or with overwrite)
 */
export async function syncUpstream(
  params: MetadataSyncRequest
): Promise<SyncUpstreamResponse> {
  const res = await api.post('/api/models/sync_upstream', params)
  return res.data
}

/**
 * Preview upstream diff
 */
export async function previewUpstreamDiff(params?: {
  locale?: SyncLocale
  source?: SyncSource
}): Promise<PreviewUpstreamDiffResponse> {
  const searchParams = new URLSearchParams()
  if (params?.locale) {
    searchParams.set('locale', params.locale)
  }
  if (params?.source) {
    searchParams.set('source', params.source)
  }
  const queryString = searchParams.toString()
  const url = queryString
    ? `/api/models/sync_upstream/preview?${queryString}`
    : '/api/models/sync_upstream/preview'
  const res = await api.get(url)
  return res.data
}

// ============================================================================
// Utility Operations
// ============================================================================

/**
 * Get missing models (used but not configured)
 */
export async function getMissingModels(): Promise<MissingModelsResponse> {
  const res = await api.get('/api/models/missing')
  return res.data
}

/**
 * Get prefill groups
 */
export async function getPrefillGroups(
  type?: 'model' | 'tag' | 'endpoint'
): Promise<PrefillGroupsResponse> {
  const res = await api.get('/api/prefill_group', {
    params: type ? { type } : undefined,
  })
  return res.data
}

/**
 * Create prefill group
 */
export async function createPrefillGroup(data: {
  name: string
  type: 'model' | 'tag' | 'endpoint'
  items: string | string[]
  description?: string
}): Promise<{ success: boolean; message?: string }> {
  const res = await api.post('/api/prefill_group', data)
  return res.data
}

/**
 * Update prefill group
 */
export async function updatePrefillGroup(data: {
  id: number
  type?: 'model' | 'tag' | 'endpoint'
  name?: string
  items?: string | string[]
  description?: string
}): Promise<{ success: boolean; message?: string }> {
  const res = await api.put('/api/prefill_group', data)
  return res.data
}

/**
 * Delete prefill group
 */
export async function deletePrefillGroup(
  id: number
): Promise<{ success: boolean; message?: string }> {
  const res = await api.delete(`/api/prefill_group/${id}`)
  return res.data
}

// ============================================================================
// Deployment Operations
// ============================================================================

/**
 * Get deployment settings (io.net config)
 */
export async function getDeploymentSettings(): Promise<DeploymentSettingsResponse> {
  const res = await api.get('/api/deployments/settings')
  return res.data
}

/**
 * Test deployment connection
 */
export async function testDeploymentConnection(): Promise<{
  success: boolean
  message?: string
}> {
  const config = { skipErrorHandler: true } as unknown as Parameters<
    typeof api.post
  >[2]
  const res = await api.post(
    '/api/deployments/settings/test-connection',
    {},
    config
  )
  return res.data
}

/**
 * Test deployment connection with optional api_key (allow test before saving)
 */
export async function testDeploymentConnectionWithKey(
  apiKey?: string
): Promise<{
  success: boolean
  message?: string
}> {
  const payload =
    typeof apiKey === 'string' && apiKey.trim()
      ? { api_key: apiKey.trim() }
      : {}
  const config = { skipErrorHandler: true } as unknown as Parameters<
    typeof api.post
  >[2]
  const res = await api.post(
    '/api/deployments/settings/test-connection',
    payload,
    config
  )
  return res.data
}

/**
 * List deployments
 */
export async function listDeployments(params: {
  p?: number
  page_size?: number
  status?: string
}): Promise<ListDeploymentsResponse> {
  const res = await api.get('/api/deployments/', { params })
  return res.data
}

/**
 * Search deployments (keyword + status)
 *
 * Backend exposes a dedicated `/api/deployments/search` route that supports
 * filtering by keyword (and status). Use this when keyword is provided.
 */
export async function searchDeployments(params: {
  p?: number
  page_size?: number
  status?: string
  keyword?: string
}): Promise<ListDeploymentsResponse> {
  const res = await api.get('/api/deployments/search', { params })
  return res.data
}

/**
 * Get deployment details
 */
export async function getDeployment(id: string | number): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const res = await api.get(`/api/deployments/${id}`)
  return res.data
}

/**
 * List deployment containers
 */
export async function listDeploymentContainers(
  deploymentId: string | number
): Promise<{
  success: boolean
  message?: string
  data?: {
    total?: number
    containers?: Array<Record<string, unknown>>
  }
}> {
  const res = await api.get(`/api/deployments/${deploymentId}/containers`)
  return res.data
}

/**
 * Get single container details
 */
export async function getDeploymentContainerDetails(
  deploymentId: string | number,
  containerId: string
): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const res = await api.get(
    `/api/deployments/${deploymentId}/containers/${encodeURIComponent(containerId)}`
  )
  return res.data
}

/**
 * Delete deployment
 */
export async function deleteDeployment(
  id: string | number
): Promise<{ success: boolean; message?: string }> {
  const res = await api.delete(`/api/deployments/${id}`)
  return res.data
}

/**
 * Get deployment logs (raw)
 */
export async function getDeploymentLogs(
  deploymentId: string | number,
  params: {
    container_id: string
    stream?: 'stdout' | 'stderr' | 'all' | string
    level?: string
    cursor?: string
    limit?: number
    follow?: boolean
    start_time?: string
    end_time?: string
  }
): Promise<{ success: boolean; message?: string; data?: string }> {
  const res = await api.get(`/api/deployments/${deploymentId}/logs`, { params })
  return res.data
}

/**
 * Get hardware types for deployment
 */
export async function getHardwareTypes(): Promise<{
  success: boolean
  data?: { hardware_types?: Array<Record<string, unknown>> }
}> {
  const res = await api.get('/api/deployments/hardware-types')
  return res.data
}

/**
 * Get locations for deployment
 */
export async function getDeploymentLocations(): Promise<{
  success: boolean
  message?: string
  data?: { locations?: Array<Record<string, unknown>>; total?: number }
}> {
  const res = await api.get('/api/deployments/locations')
  return res.data
}

/**
 * Get available replicas
 */
export async function getAvailableReplicas(params: {
  hardware_id: string
  gpu_count: number
}): Promise<{
  success: boolean
  data?: { replicas?: Array<Record<string, unknown>> }
}> {
  const res = await api.get('/api/deployments/available-replicas', { params })
  return res.data
}

/**
 * Estimate deployment price
 */
export async function estimatePrice(params: {
  location_ids: Array<string | number>
  hardware_id: string | number
  gpus_per_container: number
  duration_hours: number
  replica_count: number
  currency?: string
}): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const locationIds = (params.location_ids || [])
    .map((x) => Number(x))
    .filter((n) => Number.isInteger(n) && n > 0)
  const hardwareId = Number(params.hardware_id)
  const duration = Number(params.duration_hours)
  const gpus = Number(params.gpus_per_container)
  const replicaCount = Number(params.replica_count)
  const currency =
    typeof params.currency === 'string' && params.currency.trim()
      ? params.currency.trim().toLowerCase()
      : 'usdc'

  const payload = {
    location_ids: locationIds,
    hardware_id: hardwareId,
    gpus_per_container: gpus,
    duration_hours: duration,
    replica_count: replicaCount,
    currency,
    duration_type: 'hour',
    duration_qty: duration,
    hardware_qty: gpus,
  }

  const res = await api.post('/api/deployments/price-estimation', payload)
  return res.data
}

/**
 * Create deployment
 */
export async function createDeployment(data: {
  resource_private_name: string
  duration_hours: number
  gpus_per_container: number
  hardware_id: number
  location_ids: number[]
  container_config: {
    replica_count: number
    env_variables?: Record<string, string>
    secret_env_variables?: Record<string, string>
    entrypoint?: string[]
    traffic_port?: number
    args?: string[]
  }
  registry_config: {
    image_url: string
    registry_username?: string
    registry_secret?: string
  }
}): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const res = await api.post('/api/deployments/', data)
  return res.data
}

/**
 * Update deployment configuration
 */
export async function updateDeployment(
  id: string | number,
  data: {
    env_variables?: Record<string, string>
    secret_env_variables?: Record<string, string>
    entrypoint?: string[]
    traffic_port?: number | null
    image_url?: string
    registry_username?: string
    registry_secret?: string
    args?: string[]
    command?: string
  }
): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const payload: Record<string, unknown> = { ...data }
  if (data.traffic_port === null) {
    delete payload.traffic_port
  }
  const res = await api.put(`/api/deployments/${id}`, payload)
  return res.data
}

/**
 * Update deployment name
 */
export async function updateDeploymentName(
  id: string | number,
  name: string
): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const res = await api.put(`/api/deployments/${id}/name`, { name })
  return res.data
}

/**
 * Extend deployment duration
 */
export async function extendDeployment(
  id: string | number,
  durationHours: number
): Promise<{
  success: boolean
  message?: string
  data?: Record<string, unknown>
}> {
  const res = await api.post(`/api/deployments/${id}/extend`, {
    duration_hours: durationHours,
  })
  return res.data
}

/**
 * Check cluster name availability
 */
export async function checkClusterNameAvailability(name: string): Promise<{
  success: boolean
  message?: string
  data?: { available?: boolean; name?: string }
}> {
  const res = await api.get('/api/deployments/check-name', {
    params: { name },
  })
  return res.data
}

export interface ModelDeleteResult {
  deleted_count: number
  updated_channels: number
}

export async function deleteModels(
  modelIds: number[],
  removeFromChannels = false,
  removePricing = false
): Promise<{ success: boolean; message?: string; data: ModelDeleteResult }> {
  const res = await api.post('/api/models/delete', {
    model_ids: modelIds,
    remove_from_channels: removeFromChannels,
    remove_pricing: removePricing,
  })
  return res.data
}
