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
import { create } from 'zustand'

import type { LoginChallenge } from '@/features/auth/secure-verification/types'
import type { AdminCapabilities } from '@/lib/admin-permissions'

export type UserPermissions = {
  sidebar_settings?: boolean
  sidebar_modules?: Record<string, unknown>
  admin_permissions?: AdminCapabilities
}

export interface AuthUser {
  has_password?: boolean
  id: number
  username: string
  display_name?: string
  email?: string
  role: number
  status?: number
  group?: string
  quota?: number
  used_quota?: number
  request_count?: number
  aff_code?: string
  aff_count?: number
  aff_quota?: number
  aff_history_quota?: number
  inviter_id?: number
  github_id?: string
  discord_id?: string
  oidc_id?: string
  wechat_id?: string
  telegram_id?: string
  linux_do_id?: string
  language?: string
  setting?: Record<string, unknown> | string
  stripe_customer?: string
  sidebar_modules?: string
  permissions?: UserPermissions
}

export interface LoginSession {
  sid: string
  current: boolean
  login_method: string
  ip: string
  user_agent: string
  created_at: number
  last_active_at: number
  expires_at: number
}

export interface AuthBundle {
  access_token: string
  token_type: 'Bearer' | string
  access_expires_at: number
  user: AuthUser
  session: LoginSession
}

export type AuthBootstrapState = 'idle' | 'checking' | 'complete'

export interface PendingLoginVerification {
  challenge: LoginChallenge
  redirectTo?: string
}

interface AuthState {
  auth: {
    user: AuthUser | null
    accessToken: string | null
    accessExpiresAt: number | null
    session: LoginSession | null
    pendingLoginVerification: PendingLoginVerification | null
    bootstrapState: AuthBootstrapState
    setBundle: (bundle: AuthBundle) => void
    setUser: (user: AuthUser | null) => void
    setPendingLoginVerification: (
      pending: PendingLoginVerification | null
    ) => void
    setBootstrapState: (bootstrapState: AuthBootstrapState) => void
    reset: (bootstrapState?: AuthBootstrapState) => void
  }
}

export const useAuthStore = create<AuthState>()((set) => ({
  auth: {
    user: null,
    accessToken: null,
    accessExpiresAt: null,
    session: null,
    pendingLoginVerification: null,
    bootstrapState: 'idle',
    setBundle: (bundle) =>
      set((state) => ({
        ...state,
        auth: {
          ...state.auth,
          user: bundle.user,
          accessToken: bundle.access_token,
          accessExpiresAt: bundle.access_expires_at,
          session: bundle.session,
          pendingLoginVerification: null,
          bootstrapState: 'complete',
        },
      })),
    setUser: (user) =>
      set((state) => ({
        ...state,
        auth: {
          ...state.auth,
          user,
          pendingLoginVerification:
            state.auth.user?.id === user?.id
              ? state.auth.pendingLoginVerification
              : null,
        },
      })),
    setPendingLoginVerification: (pendingLoginVerification) =>
      set((state) => ({
        ...state,
        auth: { ...state.auth, pendingLoginVerification },
      })),
    setBootstrapState: (bootstrapState) =>
      set((state) => ({
        ...state,
        auth: { ...state.auth, bootstrapState },
      })),
    reset: (bootstrapState = 'complete') =>
      set((state) => ({
        ...state,
        auth: {
          ...state.auth,
          user: null,
          accessToken: null,
          accessExpiresAt: null,
          session: null,
          pendingLoginVerification: null,
          bootstrapState,
        },
      })),
  },
}))
