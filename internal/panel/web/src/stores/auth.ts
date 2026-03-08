/* ============================================================
 * 认证状态管理 — Zustand Store
 *
 * 职责:
 * 1. 持久化 token / refreshToken / user 到 localStorage
 * 2. 提供 login / logout / refresh 等认证操作
 * 3. 派生 isAuthenticated / isAdmin 计算属性
 *
 * 注意: token 实际存储由 api/client.ts 的 setAccessToken
 * / setRefreshToken 负责，此处也同步写入以保证一致性。
 * ============================================================ */

import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import {
  login as apiLogin,
  type User,
  type LoginResponse,
} from '../api/auth'
import {
  setAccessToken,
  setRefreshToken,
  clearTokens,
  getRefreshToken as getStoredRefreshToken,
} from '../api/client'

/* ----------------------------------------------------------
 * 类型定义
 * ---------------------------------------------------------- */

interface AuthState {
  /** 当前登录用户 */
  user: User | null
  /** JWT Access Token */
  token: string | null
  /** JWT Refresh Token */
  refreshToken: string | null

  /** 计算属性 */
  isAuthenticated: boolean
  isAdmin: boolean

  /** 登录 */
  login: (username: string, password: string) => Promise<void>
  /** 登出 */
  logout: () => void
  /** 用登录响应直接设置状态（注册后复用） */
  setFromLoginResponse: (data: LoginResponse) => void
  /** 刷新 Token */
  refresh: () => Promise<void>
  /** 手动更新用户信息 */
  setUser: (user: User) => void
}

/* ----------------------------------------------------------
 * Store 实现
 * ---------------------------------------------------------- */

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      user: null,
      token: null,
      refreshToken: null,
      isAuthenticated: false,
      isAdmin: false,

      login: async (username: string, password: string) => {
        const res = await apiLogin(username, password)
        const { user, tokens } = res.data

        // 同步写入 localStorage（供 api/client 拦截器读取）
        setAccessToken(tokens.access_token)
        setRefreshToken(tokens.refresh_token)

        set({
          user,
          token: tokens.access_token,
          refreshToken: tokens.refresh_token,
          isAuthenticated: true,
          isAdmin: user.role === 'admin',
        })
      },

      logout: () => {
        clearTokens()
        set({
          user: null,
          token: null,
          refreshToken: null,
          isAuthenticated: false,
          isAdmin: false,
        })
      },

      setFromLoginResponse: (data: LoginResponse) => {
        const { user, tokens } = data
        setAccessToken(tokens.access_token)
        setRefreshToken(tokens.refresh_token)
        set({
          user,
          token: tokens.access_token,
          refreshToken: tokens.refresh_token,
          isAuthenticated: true,
          isAdmin: user.role === 'admin',
        })
      },

      refresh: async () => {
        const storedRefresh = getStoredRefreshToken()
        if (!storedRefresh) {
          get().logout()
          throw new Error('无 Refresh Token')
        }

        const res = await fetch(
          window.location.origin + '/api/v1/auth/refresh',
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ refresh_token: storedRefresh }),
          },
        )

        if (!res.ok) {
          get().logout()
          throw new Error('Token 刷新失败')
        }

        const data = await res.json()
        setAccessToken(data.access_token)
        setRefreshToken(data.refresh_token)
        set({
          token: data.access_token,
          refreshToken: data.refresh_token,
        })
      },

      setUser: (user: User) =>
        set({
          user,
          isAdmin: user.role === 'admin',
        }),
    }),
    {
      name: 'community-auth-storage',
      // 仅持久化必要字段
      partialize: (state) => ({
        user: state.user,
        token: state.token,
        refreshToken: state.refreshToken,
        isAuthenticated: state.isAuthenticated,
        isAdmin: state.isAdmin,
      }),
    },
  ),
)
