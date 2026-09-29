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
import type { AuthBundle } from '@/stores/auth-store'

import type { LoginResult } from './secure-verification/types'

// ============================================================================
// API Payloads
// ============================================================================

export interface LoginPayload {
  username: string
  password: string
  turnstile?: string
  passwordEncryptionEnabled?: boolean
}

export interface TwoFAPayload {
  code: string
  flow_token: string
}

export interface RegisterPayload {
  username: string
  password: string
  email?: string
  verification_code?: string
  aff_code?: string
  turnstile?: string
}

export interface PasswordResetPayload {
  email: string
  turnstile?: string
}

export interface EmailVerificationPayload {
  email: string
  turnstile?: string
}

export interface BindEmailPayload {
  flow_token: string
  new_code: string
  old_code?: string
}

// ============================================================================
// API Responses
// ============================================================================

export interface LoginResponse {
  success: boolean
  message: string
  data?: LoginResult
}

export interface Login2FAResponse {
  success: boolean
  message: string
  data?: AuthBundle
}

export interface ApiResponse<T = unknown> {
  success: boolean
  message: string
  data?: T
}

// ============================================================================
// System Status
// ============================================================================

export interface SystemStatus {
  success?: boolean
  message?: string
  data?: {
    version?: string
    system_name?: string
    logo?: string
    github_oauth?: boolean
    github_client_id?: string
    discord_oauth?: boolean
    discord_client_id?: string
    oidc_enabled?: boolean
    oidc_authorization_endpoint?: string
    oidc_client_id?: string
    oidc_display_name?: string
    linuxdo_oauth?: boolean
    linuxdo_client_id?: string
    telegram_oauth?: boolean
    telegram_oauth_configured?: boolean
    telegram_bot_name?: string
    passkey_login?: boolean
    wechat_login?: boolean
    wechat_qrcode?: string
    wechat_qr_code?: string
    wechat_qrcode_image_url?: string
    wechat_qr_code_image_url?: string
    wechat_account_qrcode_image_url?: string
    WeChatAccountQRCodeImageURL?: string
    turnstile_check?: boolean
    turnstile_site_key?: string
    email_verification?: boolean
    self_use_mode_enabled?: boolean
    display_in_currency?: boolean
    display_token_stat_enabled?: boolean
    quota_per_unit?: number
    quota_display_type?: string
    usd_exchange_rate?: number
    custom_currency_symbol?: string
    custom_currency_exchange_rate?: number
    demo_site_enabled?: boolean
    user_agreement_enabled?: boolean
    privacy_policy_enabled?: boolean
    oauth_register_enabled?: boolean
    register_enabled?: boolean
    password_login_enabled?: boolean
    password_login_encryption_enabled?: boolean
    password_register_enabled?: boolean
    custom_oauth_providers?: CustomOAuthProviderInfo[]
    [key: string]: unknown
  }
  // Allow direct access to common properties
  version?: string
  system_name?: string
  logo?: string
  github_oauth?: boolean
  github_client_id?: string
  discord_oauth?: boolean
  discord_client_id?: string
  oidc_enabled?: boolean
  oidc_authorization_endpoint?: string
  oidc_client_id?: string
  oidc_display_name?: string
  linuxdo_oauth?: boolean
  linuxdo_client_id?: string
  telegram_oauth?: boolean
  telegram_oauth_configured?: boolean
  telegram_bot_name?: string
  passkey_login?: boolean
  wechat_login?: boolean
  wechat_qrcode?: string
  wechat_qr_code?: string
  wechat_qrcode_image_url?: string
  wechat_qr_code_image_url?: string
  wechat_account_qrcode_image_url?: string
  WeChatAccountQRCodeImageURL?: string
  turnstile_check?: boolean
  turnstile_site_key?: string
  email_verification?: boolean
  self_use_mode_enabled?: boolean
  display_in_currency?: boolean
  display_token_stat_enabled?: boolean
  quota_per_unit?: number
  quota_display_type?: string
  usd_exchange_rate?: number
  custom_currency_symbol?: string
  custom_currency_exchange_rate?: number
  demo_site_enabled?: boolean
  user_agreement_enabled?: boolean
  privacy_policy_enabled?: boolean
  oauth_register_enabled?: boolean
  register_enabled?: boolean
  password_login_enabled?: boolean
  password_login_encryption_enabled?: boolean
  password_register_enabled?: boolean
  custom_oauth_providers?: CustomOAuthProviderInfo[]
  [key: string]: unknown
}

// ============================================================================
// OAuth
// ============================================================================

export interface OAuthProvider {
  name: string
  type: 'github' | 'discord' | 'oidc' | 'linuxdo' | 'telegram' | 'wechat'
  enabled: boolean
  clientId?: string
  authEndpoint?: string
}

export interface CustomOAuthProviderInfo {
  id: number
  name: string
  slug: string
  icon: string
  client_id: string
  authorization_endpoint: string
  scopes: string
}

// ============================================================================
// Form Props
// ============================================================================

export interface AuthFormProps extends React.HTMLAttributes<HTMLFormElement> {
  redirectTo?: string
}
