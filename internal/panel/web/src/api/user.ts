/* ============================================================
 * 用户 API — 个人信息 / 额度 / 统计 / API Key / 凭证 / 兑换
 * 与后端 handler_user.go 路由对齐
 * ============================================================ */

import { get, put, post, del, type ApiResponse } from './client'
import type { User } from './auth'

/* ----------------------------------------------------------
 * 类型定义
 * ---------------------------------------------------------- */

export interface UpdateProfileData {
  email?: string
  password?: string
  current_password?: string
}

export interface QuotaInfo {
  total: number
  used: number
  remaining: number
  reset_at: string
  models: ModelQuota[]
}

export interface ModelQuota {
  model: string
  total: number
  used: number
  remaining: number
}

export interface UserStats {
  total_requests: number
  today_requests: number
  week_requests: number
  month_requests: number
  recent_usage: UsageRecord[]
}

export interface UsageRecord {
  date: string
  model: string
  count: number
  tokens: number
}

export interface CredentialItem {
  id: string
  provider: string
  health: 'healthy' | 'unhealthy' | 'unknown'
  created_at: string
  last_checked: string | null
}

export interface UploadCredentialData {
  provider: string
  api_key: string
  endpoint?: string
  notes?: string
}

export interface RedemptionTemplate {
  id: string
  name: string
  description: string
  bonus_requests: number
  bonus_tokens: number
  model_pattern: string
  available: boolean
}

export interface ReferralInfo {
  code: string
  total_referrals: number
  total_rewards: number
}

/* ----------------------------------------------------------
 * 个人信息
 * ---------------------------------------------------------- */

/** GET /api/v1/user/profile */
export function getProfile(): Promise<ApiResponse<User>> {
  return get<User>('/user/profile')
}

/** PUT /api/v1/user/profile */
export function updateProfile(
  data: UpdateProfileData,
): Promise<ApiResponse<{ message: string }>> {
  return put<{ message: string }>('/user/profile', data)
}

/** POST /api/v1/user/reset-api-key */
export function resetApiKey(): Promise<ApiResponse<{ api_key: string }>> {
  return post<{ api_key: string }>('/user/reset-api-key')
}

/* ----------------------------------------------------------
 * 额度 / 统计
 * ---------------------------------------------------------- */

/** GET /api/v1/user/quota */
export function getQuota(): Promise<ApiResponse<QuotaInfo>> {
  return get<QuotaInfo>('/user/quota')
}

/** GET /api/v1/user/stats */
export function getStats(): Promise<ApiResponse<UserStats>> {
  return get<UserStats>('/user/stats')
}

/** GET /api/v1/user/stats/recent */
export function getRecentStats(): Promise<ApiResponse<{ stats: UsageRecord[] }>> {
  return get<{ stats: UsageRecord[] }>('/user/stats/recent')
}

/* ----------------------------------------------------------
 * 凭证管理
 * ---------------------------------------------------------- */

/** GET /api/v1/user/credentials */
export function getCredentials(): Promise<ApiResponse<{ credentials: CredentialItem[] }>> {
  return get<{ credentials: CredentialItem[] }>('/user/credentials')
}

/** POST /api/v1/user/credentials */
export function uploadCredential(
  data: UploadCredentialData,
): Promise<ApiResponse<{ message: string; credential: CredentialItem }>> {
  return post<{ message: string; credential: CredentialItem }>('/user/credentials', data)
}

/** DELETE /api/v1/user/credentials/:id */
export function deleteCredential(
  id: string,
): Promise<ApiResponse<{ message: string }>> {
  return del<{ message: string }>(`/user/credentials/${id}`)
}

/* ----------------------------------------------------------
 * 兑换 / 邀请
 * ---------------------------------------------------------- */

/** POST /api/v1/user/redeem */
export function redeemCode(
  code: string,
): Promise<ApiResponse<{ message: string }>> {
  return post<{ message: string }>('/user/redeem', { code })
}

/** GET /api/v1/user/redemption/templates */
export function getRedemptionTemplates(): Promise<ApiResponse<{ templates: RedemptionTemplate[] }>> {
  return get<{ templates: RedemptionTemplate[] }>('/user/redemption/templates')
}

/** POST /api/v1/user/redemption/templates/:id/claim */
export function claimTemplate(
  templateId: string,
): Promise<ApiResponse<{ message: string }>> {
  return post<{ message: string }>(`/user/redemption/templates/${templateId}/claim`)
}

/** GET /api/v1/user/referral */
export function getReferralInfo(): Promise<ApiResponse<{ referral: ReferralInfo }>> {
  return get<{ referral: ReferralInfo }>('/user/referral')
}

/* ----------------------------------------------------------
 * 设置
 * ---------------------------------------------------------- */

/** POST /api/v1/user/settings/password */
export function changePassword(
  data: {
    current_password?: string
    new_password?: string
    currentPassword?: string
    newPassword?: string
  },
): Promise<ApiResponse<{ message: string }>> {
  // 兼容 camelCase 和 snake_case 两种命名
  const payload = {
    current_password: data.current_password ?? data.currentPassword ?? '',
    new_password: data.new_password ?? data.newPassword ?? '',
  }
  return post<{ message: string }>('/user/settings/password', payload)
}

/** POST /api/v1/user/settings/api-key/regenerate */
export async function regenerateApiKey(): Promise<{ api_key: string; apiKey: string }> {
  const res = await post<{ api_key: string }>('/user/settings/api-key/regenerate')
  // 兼容：同时提供 snake_case 和 camelCase 字段
  return { api_key: res.data.api_key, apiKey: res.data.api_key }
}
