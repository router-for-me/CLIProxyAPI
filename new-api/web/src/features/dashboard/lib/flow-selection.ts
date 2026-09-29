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
import type {
  FlowQuotaDataItem,
  FlowUserFilterOption,
} from '@/features/dashboard/types'
import { createServerError } from '@/lib/server-error-message'

export type FlowDisplayState = 'loading' | 'error' | 'empty' | 'chart'

export interface FlowResponse {
  success: boolean
  data?: FlowQuotaDataItem[]
  message?: string
}

export function requireSuccessfulFlowRows(
  response: FlowResponse,
  fallbackMessage: string
): FlowQuotaDataItem[] {
  if (!response.success) {
    throw createServerError(response, response.message || fallbackMessage)
  }
  return response.data ?? []
}

export function flowDisplayState(options: {
  isLoading: boolean
  isError: boolean
  linkCount: number
  themeReady: boolean
}): FlowDisplayState {
  if (options.isLoading) return 'loading'
  if (options.isError) return 'error'
  if (options.linkCount === 0) return 'empty'
  if (!options.themeReady) return 'loading'
  return 'chart'
}

export function compactFlowSelectionLabel(count: number): string {
  return count > 0 ? String(count) : '*'
}

export function visibleFlowUsers(
  users: FlowUserFilterOption[],
  selectedUsers: string[]
): FlowUserFilterOption[] {
  if (selectedUsers.length === 0) return users
  const selected = new Set(selectedUsers)
  return users.filter((user) => selected.has(user.value))
}
