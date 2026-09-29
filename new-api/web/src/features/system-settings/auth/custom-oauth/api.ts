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

import type { CustomOAuthProvider, DiscoveryResponse } from './types'

// ============================================================================
// Response Types
// ============================================================================

interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

// ============================================================================
// Custom OAuth Provider APIs
// ============================================================================

export async function getCustomOAuthProviders(): Promise<
  ApiResponse<CustomOAuthProvider[]>
> {
  const res = await api.get('/api/custom-oauth-provider/')
  return res.data
}

export async function getCustomOAuthProvider(
  id: number
): Promise<ApiResponse<CustomOAuthProvider>> {
  const res = await api.get(`/api/custom-oauth-provider/${id}`)
  return res.data
}

export async function createCustomOAuthProvider(
  data: Omit<CustomOAuthProvider, 'id'>
): Promise<ApiResponse<CustomOAuthProvider>> {
  const res = await api.post('/api/custom-oauth-provider/', data)
  return res.data
}

export async function updateCustomOAuthProvider(
  id: number,
  data: Partial<CustomOAuthProvider>
): Promise<ApiResponse<CustomOAuthProvider>> {
  const res = await api.put(`/api/custom-oauth-provider/${id}`, data)
  return res.data
}

export async function deleteCustomOAuthProvider(
  id: number
): Promise<ApiResponse> {
  const res = await api.delete(`/api/custom-oauth-provider/${id}`)
  return res.data
}

export async function discoverOIDCEndpoints(
  wellKnownUrl: string
): Promise<DiscoveryResponse> {
  const res = await api.post('/api/custom-oauth-provider/discovery', {
    well_known_url: wellKnownUrl,
  })
  return res.data
}
