/* ============================================================
 * 用户数据状态管理 — Zustand Store
 *
 * 管理用户维度的业务数据：个人信息、额度、凭证、统计等。
 * 使用 api/user.ts 中的封装方法发起请求。
 * ============================================================ */

import { create } from 'zustand'
import type { User } from '../api/auth'
import {
  getProfile as apiGetProfile,
  getQuota as apiGetQuota,
  getStats as apiGetStats,
  getCredentials as apiGetCredentials,
  type QuotaInfo,
  type UserStats,
  type CredentialItem,
} from '../api/user'

/* ----------------------------------------------------------
 * 类型定义
 * ---------------------------------------------------------- */

interface UserState {
  /** 用户个人信息 */
  profile: User | null
  /** 额度信息 */
  quota: QuotaInfo | null
  /** 使用统计 */
  stats: UserStats | null
  /** 凭证列表 */
  credentials: CredentialItem[]

  /** 各数据的独立加载态 */
  profileLoading: boolean
  quotaLoading: boolean
  statsLoading: boolean
  credentialsLoading: boolean

  /** 错误信息 */
  error: string | null

  /** 数据拉取方法 */
  fetchProfile: () => Promise<void>
  fetchQuota: () => Promise<void>
  fetchStats: () => Promise<void>
  fetchCredentials: () => Promise<void>
  /** 清空所有数据（退出登录时调用） */
  reset: () => void
}

/* ----------------------------------------------------------
 * 初始状态
 * ---------------------------------------------------------- */

const initialState = {
  profile: null,
  quota: null,
  stats: null,
  credentials: [],
  profileLoading: false,
  quotaLoading: false,
  statsLoading: false,
  credentialsLoading: false,
  error: null,
}

/* ----------------------------------------------------------
 * Store 实现
 * ---------------------------------------------------------- */

export const useUserStore = create<UserState>()((set) => ({
  ...initialState,

  fetchProfile: async () => {
    set({ profileLoading: true, error: null })
    try {
      const res = await apiGetProfile()
      set({ profile: res.data, profileLoading: false })
    } catch (err) {
      const message = err instanceof Error ? err.message : '获取个人信息失败'
      set({ profileLoading: false, error: message })
    }
  },

  fetchQuota: async () => {
    set({ quotaLoading: true, error: null })
    try {
      const res = await apiGetQuota()
      set({ quota: res.data, quotaLoading: false })
    } catch (err) {
      const message = err instanceof Error ? err.message : '获取额度信息失败'
      set({ quotaLoading: false, error: message })
    }
  },

  fetchStats: async () => {
    set({ statsLoading: true, error: null })
    try {
      const res = await apiGetStats()
      set({ stats: res.data, statsLoading: false })
    } catch (err) {
      const message = err instanceof Error ? err.message : '获取使用统计失败'
      set({ statsLoading: false, error: message })
    }
  },

  fetchCredentials: async () => {
    set({ credentialsLoading: true, error: null })
    try {
      const res = await apiGetCredentials()
      set({
        credentials: res.data.credentials ?? [],
        credentialsLoading: false,
      })
    } catch (err) {
      const message = err instanceof Error ? err.message : '获取凭证列表失败'
      set({ credentialsLoading: false, error: message })
    }
  },

  reset: () => set(initialState),
}))
