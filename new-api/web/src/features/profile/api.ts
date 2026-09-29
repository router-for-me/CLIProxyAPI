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
import type { CustomOAuthBinding } from '@/lib/oauth'
import { authRequestOptions, authResult } from '@/lib/secure-verification'
import type { LoginSession } from '@/stores/auth-store'

import { normalizeUserSettings } from './lib/user-settings'
import type {
  ApiResponse,
  UserProfile,
  UpdateUserRequest,
  UpdateUserSettingsRequest,
  CheckinStatusResponse,
  CheckinResponse,
  AccountSecurityResult,
  EmailBindingFlow,
} from './types'

// ============================================================================
// User Profile APIs
// ============================================================================

/**
 * Get current user profile
 */
export async function getUserProfile(): Promise<ApiResponse<UserProfile>> {
  const res = await api.get('/api/user/self')
  return res.data
}

/**
 * Update user profile
 */
export async function updateUserProfile(
  data: UpdateUserRequest
): Promise<ApiResponse> {
  const res = await api.put('/api/user/self', data, {
    acceptAuthRotation: Boolean(data.password),
  })
  return res.data
}

export function changeAccountPassword(
  data: UpdateUserRequest,
  proofToken: string,
  signal: AbortSignal
): Promise<AccountSecurityResult & { has_password: boolean }> {
  return authResult(
    api.put('/api/user/self', data, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      acceptAuthRotation: true,
      singleUseAuthorization: true,
      signal,
    })
  )
}

/**
 * Update user settings
 */
export async function updateUserSettings(
  data: UpdateUserSettingsRequest
): Promise<ApiResponse> {
  const profile = await getUserProfile()
  if (!profile.success || !profile.data) {
    return { success: false, message: profile.message }
  }
  const settings = normalizeUserSettings(profile.data.setting)
  const res = await api.put('/api/user/setting', { ...settings, ...data })
  return res.data
}

/**
 * Update interface language preference
 */
export async function updateUserLanguage(
  language: string
): Promise<ApiResponse> {
  const res = await api.put('/api/user/self', { language })
  return res.data
}

/**
 * Delete user account
 */
export function deleteUserAccount(
  proof: string,
  signal: AbortSignal
): Promise<AccountSecurityResult> {
  return authResult(
    api.delete('/api/user/self', {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proof },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to delete account'
  )
}

// ============================================================================
// Account Binding APIs
// ============================================================================

/**
 * Send email verification code
 */
export async function sendEmailVerification(
  email: string,
  turnstileToken?: string
): Promise<ApiResponse> {
  const params = new URLSearchParams({ email })
  if (turnstileToken) {
    params.append('turnstile', turnstileToken)
  }
  const res = await api.get(`/api/verification?${params}`)
  return res.data
}

/**
 * Bind email account
 */
export function startEmailBinding(
  email: string,
  proofToken: string,
  signal: AbortSignal
): Promise<EmailBindingFlow> {
  return authResult(
    api.post(
      '/api/oauth/email/bind/start',
      { email },
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proofToken },
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function resendEmailBinding(
  flowToken: string,
  signal: AbortSignal
): Promise<EmailBindingFlow> {
  return authResult(
    api.post(
      '/api/oauth/email/bind/resend',
      { flow_token: flowToken },
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function bindEmail(
  flowToken: string,
  newCode: string,
  oldCode: string,
  signal: AbortSignal
): Promise<AccountSecurityResult> {
  return authResult(
    api.post(
      '/api/oauth/email/bind',
      { flow_token: flowToken, new_code: newCode, old_code: oldCode },
      {
        ...authRequestOptions,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

/**
 * Bind WeChat account
 */
export function bindWeChat(
  code: string,
  proofToken: string,
  signal: AbortSignal
): Promise<AccountSecurityResult> {
  return authResult(
    api.post(
      '/api/oauth/wechat/bind',
      { code },
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proofToken },
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export interface TelegramBindFlow {
  flow_token: string
  callback_url: string
  expires_at: number
}

export async function startTelegramBind(): Promise<
  ApiResponse<TelegramBindFlow>
> {
  const res = await api.post('/api/oauth/telegram/bind/start')
  return res.data
}

// ============================================================================
// Login Session APIs
// ============================================================================

export async function getLoginSessions(): Promise<ApiResponse<LoginSession[]>> {
  const res = await api.get('/api/user/sessions')
  return res.data
}

export async function revokeLoginSession(sid: string): Promise<ApiResponse> {
  const res = await api.delete(`/api/user/sessions/${encodeURIComponent(sid)}`)
  return res.data
}

export async function revokeOtherLoginSessions(): Promise<ApiResponse> {
  const res = await api.post('/api/user/sessions/revoke-others')
  return res.data
}

// ============================================================================
// Custom OAuth Binding APIs
// ============================================================================

/**
 * Get current user's custom OAuth bindings
 */
export async function getSelfOAuthBindings(): Promise<
  ApiResponse<CustomOAuthBinding[]>
> {
  const res = await api.get('/api/user/oauth/bindings')
  return res.data
}

/**
 * Unbind a custom OAuth provider for current user
 */
export function unbindCustomOAuth(
  providerId: number,
  proofToken: string,
  signal: AbortSignal
): Promise<AccountSecurityResult> {
  return authResult(
    api.delete(`/api/user/oauth/bindings/${providerId}`, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    })
  )
}

// ============================================================================
// Checkin APIs
// ============================================================================

/**
 * Get checkin status for a specific month
 */
export async function getCheckinStatus(
  month: string
): Promise<ApiResponse<CheckinStatusResponse>> {
  const res = await api.get(`/api/user/checkin?month=${month}`)
  return res.data
}

/**
 * Perform daily checkin
 */
export async function performCheckin(
  turnstileToken?: string
): Promise<ApiResponse<CheckinResponse>> {
  const url = turnstileToken
    ? `/api/user/checkin?turnstile=${encodeURIComponent(turnstileToken)}`
    : '/api/user/checkin'
  const res = await api.post(url)
  return res.data
}
