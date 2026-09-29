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
import type { UserPermissions } from '@/stores/auth-store'

// ============================================================================
// Profile Type Definitions
// ============================================================================

/**
 * Generic API response
 */
export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

/**
 * User profile data
 */
export interface UserProfile {
  has_password?: boolean
  permissions?: UserPermissions
  /** User ID */
  id: number
  /** Username */
  username: string
  /** Display name */
  display_name: string
  /** User role (1=普通用户, 10=管理员, 100=超级管理员) */
  role: number
  /** Email address */
  email?: string
  /** User group */
  group: string
  /** Current quota balance */
  quota: number
  /** Total used quota */
  used_quota: number
  /** Total request count */
  request_count: number
  /** Account status (1=启用, 2=禁用, 3=待审核, 4=已删除) */
  status: number
  /** Access token (system token) */
  access_token?: string
  /** Affiliate code */
  aff_code?: string
  /** Number of successful affiliate invites */
  aff_count: number
  /** Affiliate quota (pending rewards) */
  aff_quota: number
  /** Total affiliate quota earned (historical) */
  aff_history_quota: number
  /** Invite user ID */
  invite_user_id?: number
  /** Account creation timestamp */
  created_time: number
  /** User settings (JSON string) */
  setting?: string
  /** WeChat ID (OAuth) */
  wechat_id?: string
  /** GitHub ID (OAuth) */
  github_id?: string
  /** Discord ID (OAuth) */
  discord_id?: string
  /** OIDC ID (OAuth) */
  oidc_id?: string
  /** Telegram ID (OAuth) */
  telegram_id?: string
  /** LinuxDO ID (OAuth) */
  linux_do_id?: string
}

/**
 * Notification type
 */
export type NotifyType = 'email' | 'webhook' | 'bark' | 'gotify'

/**
 * Parsed user settings
 */
export interface UserSettings {
  /** Notification type */
  notify_type?: NotifyType
  /** Quota warning threshold */
  quota_warning_threshold?: number
  /** Webhook URL */
  webhook_url?: string
  /** Webhook secret */
  webhook_secret?: string
  /** Notification email */
  notification_email?: string
  /** Bark URL */
  bark_url?: string
  /** Gotify server URL */
  gotify_url?: string
  /** Gotify application token */
  gotify_token?: string
  /** Gotify message priority (0-10) */
  gotify_priority?: number
  /** Accept unset model ratio model */
  accept_unset_model_ratio_model?: boolean
  /** Record IP log */
  record_ip_log?: boolean
  /** Receive upstream model update notifications (admin only) */
  upstream_model_update_notify_enabled?: boolean
  /** Preferred interface/API response language */
  language?: string
}

/**
 * User update request
 */
export interface UpdateUserRequest {
  display_name?: string
  password?: string
  original_password?: string
}

export interface AccountSecurityResult {
  notification_warning?: boolean
}

export interface EmailBindingFlow extends AccountSecurityResult {
  flow_token: string
  email: string
  current_email?: string
  old_email_required: boolean
  expires_at: number
  resend_at: number
}

/**
 * User settings update request
 */
export interface UpdateUserSettingsRequest {
  notify_type?: string
  quota_warning_threshold?: number
  webhook_url?: string
  webhook_secret?: string
  notification_email?: string
  bark_url?: string
  gotify_url?: string
  gotify_token?: string
  gotify_priority?: number
  accept_unset_model_ratio_model?: boolean
  record_ip_log?: boolean
  upstream_model_update_notify_enabled?: boolean
}

/**
 * Account binding item
 */
export interface BindingItem {
  id: string
  label: string
  icon: React.ElementType
  value?: string
  isBound: boolean
  isEnabled: boolean
  onBind: () => void
}

/**
 * Two-Factor Authentication Status
 */
export interface TwoFAStatus {
  enabled: boolean
  locked: boolean
  backup_codes_remaining: number
}

// ============================================================================
// Checkin Type Definitions
// ============================================================================

/**
 * Checkin record for a specific date
 */
export interface CheckinRecord {
  /** Check-in date (YYYY-MM-DD) */
  checkin_date: string
  /** Quota awarded for this check-in */
  quota_awarded: number
}

/**
 * Checkin statistics
 */
export interface CheckinStats {
  /** Whether user has checked in today */
  checked_in_today: boolean
  /** Total number of check-ins */
  total_checkins: number
  /** Total quota earned from check-ins */
  total_quota: number
  /** Current month check-in count */
  checkin_count: number
  /** Check-in records for the queried month */
  records: CheckinRecord[]
}

/**
 * Check-in status response
 */
export interface CheckinStatusResponse {
  /** Whether check-in feature is enabled */
  enabled: boolean
  /** Check-in statistics */
  stats: CheckinStats
}

/**
 * Check-in action response
 */
export interface CheckinResponse {
  /** Quota awarded for this check-in */
  quota_awarded: number
}
