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
import { api, type ApiRequestConfig } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type {
  ApiResponse,
  MarketplaceSource,
  TaskPluginDetail,
  TaskPluginListItem,
  TaskPluginRecord,
  TaskPluginUsage,
  TaskPluginDryRunRequest,
} from './types'

const mutationConfig: ApiRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
}

export async function dryRunTaskPlugin(
  key: string,
  request: TaskPluginDryRunRequest
) {
  const response = await api.post<ApiResponse<unknown>>(
    `/api/plugin/task/${encodeURIComponent(key)}/dryrun`,
    request,
    mutationConfig
  )
  return requireSuccess(response.data)
}

export class TaskPluginUsageError extends Error {
  constructor(
    message: string,
    public usage: TaskPluginUsage
  ) {
    super(message)
  }
}

function requireSuccess<T>(response: ApiResponse<T>): T {
  if (!response.success) {
    const data = response.data as TaskPluginUsage | undefined
    if (data?.channels && typeof data.in_flight_count === 'number') {
      throw new TaskPluginUsageError(response.message, data)
    }
    throw createServerError(response)
  }
  return response.data
}

export async function listTaskPlugins() {
  const response =
    await api.get<ApiResponse<TaskPluginListItem[]>>('/api/plugin/task')
  return requireSuccess(response.data)
}

export async function getTaskPlugin(key: string, version?: string) {
  const response = await api.get<ApiResponse<TaskPluginDetail>>(
    `/api/plugin/task/${encodeURIComponent(key)}`,
    { params: version ? { version } : undefined }
  )
  return requireSuccess(response.data)
}

export async function getTaskPluginVersions(key: string) {
  const response = await api.get<ApiResponse<TaskPluginRecord[]>>(
    `/api/plugin/task/${encodeURIComponent(key)}/versions`
  )
  return requireSuccess(response.data)
}

/**
 * `icon` is the sidecar icon.svg / icon.png as a data URI. It is stored apart
 * from the source, so the JavaScript stays readable in diffs and reviews.
 */
export async function uploadTaskPlugin(
  source: string,
  remark: string,
  icon?: string
) {
  const response = await api.post<ApiResponse<TaskPluginDetail>>(
    '/api/plugin/task',
    { source, remark, icon: icon || undefined },
    mutationConfig
  )
  return requireSuccess(response.data)
}

/**
 * Installs a plugin fetched from a marketplace source. `sourceSha256` lets the
 * server re-verify the bytes it received against the index hash, and `force` is
 * deliberately never sent: a routing conflict must reject so the administrator
 * resolves it on the task plugins page instead of being silently overridden.
 */
export async function installMarketplacePlugin(request: {
  source: string
  sourceSha256?: string
  remark: string
  icon?: string
}) {
  const response = await api.post<ApiResponse<TaskPluginDetail>>(
    '/api/plugin/task',
    {
      source: request.source,
      sourceSha256: request.sourceSha256,
      enabled: true,
      remark: request.remark,
      icon: request.icon || undefined,
    },
    mutationConfig
  )
  return requireSuccess(response.data)
}

export async function listMarketplaceSources() {
  const response = await api.get<ApiResponse<MarketplaceSource[]>>(
    '/api/plugin/task/marketplace/sources'
  )
  return requireSuccess(response.data) ?? []
}

export async function updateMarketplaceSources(sources: MarketplaceSource[]) {
  const response = await api.put<ApiResponse<MarketplaceSource[]>>(
    '/api/plugin/task/marketplace/sources',
    sources,
    mutationConfig
  )
  return requireSuccess(response.data) ?? []
}

export async function activateTaskPlugin(key: string, version: string) {
  const response = await api.post<ApiResponse<null>>(
    `/api/plugin/task/${encodeURIComponent(key)}/activate`,
    { version },
    mutationConfig
  )
  requireSuccess(response.data)
}

export async function setTaskPluginStatus(
  key: string,
  enabled: boolean,
  options?: { cascade?: boolean; force?: boolean }
) {
  const response = await api.post<ApiResponse<null>>(
    `/api/plugin/task/${encodeURIComponent(key)}/status`,
    { enabled },
    { ...mutationConfig, params: options }
  )
  requireSuccess(response.data)
}

export async function deleteTaskPluginVersion(
  key: string,
  version: string,
  force = false
) {
  const response = await api.delete<ApiResponse<null>>(
    `/api/plugin/task/${encodeURIComponent(key)}/versions/${encodeURIComponent(version)}`,
    { ...mutationConfig, params: force ? { force: true } : undefined }
  )
  requireSuccess(response.data)
}

export async function getTaskPluginEnabledOption() {
  const response =
    await api.get<ApiResponse<Array<{ key: string; value: string }>>>(
      '/api/option/'
    )
  const options = requireSuccess(response.data)
  return (
    options.find((option) => option.key === 'TaskPluginEnabled')?.value ===
    'true'
  )
}

export async function setTaskPluginEnabledOption(enabled: boolean) {
  const response = await api.put<ApiResponse<null>>(
    '/api/option/',
    { key: 'TaskPluginEnabled', value: String(enabled) },
    mutationConfig
  )
  requireSuccess(response.data)
}
