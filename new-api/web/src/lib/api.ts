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
import { api } from '@/lib/http-client'
import { authRequestOptions, authResult } from '@/lib/secure-verification'
import { requireServerSuccess } from '@/lib/server-error-message'

export {
  applyAuthBundle,
  applyAuthRotation,
  bootstrapAuthentication,
  clearAuthenticatedClientState,
  clearAuthentication,
  getCommonHeaders,
  getFreshAuthHeaders,
  isAuthBundle,
  refreshAuthentication,
  resolveAuthentication,
  AuthRotationError,
} from '@/lib/auth-session'
export type { AuthTokenRotation, RefreshOutcome } from '@/lib/auth-session'
export { api }
export type { ApiRequestConfig } from '@/lib/http-client'

// ============================================================================
// User APIs
// ============================================================================

export async function getSelf() {
  const res = await api.get('/api/user/self', {
    skipErrorHandler: true,
  })
  return res.data
}

export async function getUserModels(): Promise<{
  success: boolean
  message?: string
  data?: string[]
}> {
  const res = await api.get('/api/user/models')
  return res.data
}

export async function getUserGroups(): Promise<{
  success: boolean
  message?: string
  data?: Record<string, { desc: string; ratio: number | string }>
}> {
  const res = await api.get('/api/user/self/groups')
  return res.data
}

// ============================================================================
// System APIs
// ============================================================================

export async function getStatus() {
  const res = await api.get('/api/status')
  return requireServerSuccess(res.data)?.data as Record<string, unknown>
}

export async function getNotice(): Promise<{
  success: boolean
  message?: string
  data?: string
}> {
  // Drop the client's global `Cache-Control: no-store` for this public,
  // non-user-specific payload. `no-store` forbids the browser from keeping a
  // copy at all, so it would never hold an ETag to revalidate with and the
  // server could never answer 304. The server sends `no-cache`, so the browser
  // still revalidates on every request and an admin edit shows up immediately.
  const res = await api.get('/api/notice', {
    headers: { 'Cache-Control': null },
  })
  return res.data
}

// ============================================================================
// 2FA Management APIs
// ============================================================================

export function disable2FA(
  proofToken: string,
  signal?: AbortSignal
): Promise<{ notification_warning?: boolean }> {
  return authResult(
    api.post(
      '/api/user/2fa/disable',
      {},
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proofToken },
        acceptAuthRotation: true,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function regenerate2FABackupCodes(
  proofToken: string,
  signal?: AbortSignal
): Promise<{ backup_codes: string[]; notification_warning?: boolean }> {
  return authResult(
    api.post(
      '/api/user/2fa/backup_codes',
      {},
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proofToken },
        acceptAuthRotation: true,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}
